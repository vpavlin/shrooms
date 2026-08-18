# Open questions from the 2026-08-15 security audit

**Status: decided 2026-08-18 unless marked otherwise.** Each section keeps the
question as it was asked, with the answer and its reasoning underneath, because
the reasoning is the part that stops the question being reopened from scratch.

Decisions I did not want to take alone while working through
`SECURITY_AUDIT.md`. Each one is skipped in the code for now, with the item it
belongs to. Nothing here is blocking: the security gap each finding names is
closed by the part that *was* built, and these are the choices about how far to
go beyond that.

## H1 — should a revocation also be re-published when a peer joins? — DECIDED: yes

**Decision.** The admin's node re-publishes the list when a new peer appears,
and any node may be configured to do the same, so the guarantee does not rest on
one machine being awake.

Two parts, and the second is the interesting one. Re-publishing on discovery
makes a revocation prompt rather than eventually-within-an-hour, which is the
difference between a lost laptop losing access now and losing it after somebody
notices. Letting other nodes opt in makes it survive the admin being off — a
mesh whose only durable machine is a laptop in a bag is exactly the mesh where
this matters, and the relay or a VPS is usually the node that is always up.

Optional rather than universal, deliberately: every node re-publishing on every
discovery is N× the traffic for a statement that is already re-sent each epoch,
and the cost falls on the phone. A setting — announce_revocations, off by
default, on for a relay — puts it where somebody has decided it is worth it.

Still needs the per-peer cooldown announces already use, or a newcomer draws one
publish per node simultaneously.

**Built:** revocations are persisted per mesh in the state dir, verified again
against the authority on load, and re-published on every epoch rotation.

That closes the gap: a node that was offline, or that joins later, learns every
withdrawal within one epoch instead of never. What it does not give is
*promptness* — a device revoked ten minutes ago is admitted by a node that
joins now, until the next rotation.

The audit also asks for re-publication "on join". The question is what that
should mean, because we do not have a join event, only "a peer we had not seen
appears":

- **Nothing further.** Up to one epoch of exposure for a node that joins in the
  gap. Simplest, and the window is bounded by something we already control.
- **Re-publish when a new peer is discovered.** Prompt, but every node that
  sees the newcomer publishes the whole list at once — N nodes × M revocations,
  triggered by anyone who can appear on the bus. Would need the per-peer
  cooldown `repliedTo` already implements for announces.
- **Answer on request.** The joiner asks; whoever hears it replies. Least
  traffic, most machinery, and a new message type on the control plane.

Worth noting the epoch is an hour, and `EpochSeconds` is ours to change.

## H1 — retention: revocations are kept for a fixed 30 days — DECIDED: match the credential

**Decision.** The revocation carries the expiry of what it withdraws, so
retention matches the credential's real life instead of a fixed 30 days.

This is the wire change flagged below, and it is cheap now and expensive later:
the format is versioned, nobody outside this house runs a mesh yet, and a
revocation that outlives the thing it revokes is only wasteful while one that
dies first re-admits a device.

Where the value comes from without asking anybody: the daemon already knows
every peer's expiry — it records NotAfter from each announce it verifies
(m.expiry) — so `admin revoke` can fill it in from the roster. When the device
has never been seen, fall back to now + the default life, which is exactly
today's behaviour and no worse.

Note this also makes List.Prune safe to wire up, which is the dead code the
audit flagged: it can drop an entry once the credential it names has expired,
because it will finally know when that is.

`List.Add` hard-codes `keepUntil = now + DefaultLife`, and `--life` is a
supported flag on `admin issue` and `invite`. A revocation for a credential
issued with a longer life would be dropped while that credential is still
valid — the device comes back.

This is latent rather than live, because `List.Prune` is never called (audit
Low), so nothing is dropped today. Fixing it properly means the revocation
carrying the withdrawn credential's `NotAfter`, which changes the signed
revocation format — a wire change, and therefore a decision rather than a
tidy-up.

Three options: put `NotAfter` in the revocation (wire change, correct); keep
the 30-day retention and refuse `--life` beyond it (no wire change, removes a
feature); or delete `Prune` and keep revocations forever, accepting unbounded
growth on admin-signed input only.

## M9 — how promptly should an expired credential drop a live tunnel? — DECIDED: immediately

**Decision.** No grace window. A device whose credential has expired stops being
carried at once, which is what is already built.

The reasoning that settles it: a machine that has been offline long enough to
miss a renewal sweep — which starts well before expiry, by design — is more
likely lost than merely asleep. A grace period protects the asleep case and
extends access for the lost one, and those are the same window. Expiry is the
backstop that cannot be suppressed; softening it makes it a suggestion.

Nothing to build. The strict behaviour is in syncPeers with a probe-tick check
to make it prompt.

The audit's fix is to drop the peer in `syncPeers` as soon as its newest
credential has expired, rather than waiting the ~6h `ForgetAfter`. That is
clearly right for a mesh that has an authority.

The decision is the clock. Expiry is enforced against *our* clock, and a device
whose credential expires while its owner is asleep loses the mesh at the moment
the admin's renewal sweep would have fixed it. `cred.RenewBefore` exists so
renewals happen well ahead, but a node that has been offline for a month comes
back to a hard cutoff rather than a grace period.

Options: drop immediately on expiry (strict, matches the docs); allow a grace
window before teardown (kinder, and a second number to justify); or drop
immediately but keep announcing to the expired peer so it can pick up a renewal
(which is what the grant path already does, and may make the grace window
unnecessary).

## M1 — what should `ui_listen` actually serve? — DECIDED: /status only

**Decision.** It stays as built: `/status` and nothing else. `/logs` is not
added.

The question was whether logs belong there too, since both carry the same peer
names and addresses. What settled it is that nothing uses this listener:
Basecamp reads the unix socket, the phone reads neither, and the setting was
unrecognised by the person who owns every mesh in it. An HTTP port on a VPN
daemon with no user is surface without a reason.

**Worth revisiting as deletion rather than extension.** If nothing has needed it
by the time there is a release, removing it is one fewer thing to get wrong —
the mutating-endpoints bug it caused is the kind of mistake that only exists
because the port exists.

Making it read-only is agreed. The question is what "read" includes: `/status`
alone, or `/status` and `/logs`. Logs carry peer names, addresses and mesh
labels, and the listener is reachable by any local user on a multi-user host.
`/status` carries the same information in a tidier form, so the honest answer
may be that both are equally sensitive and the split should be by *verb* rather
than by path.

## M5 — binding a relay registration to the device that owns the key

**Built:** the relay table is bounded (`MaxRegistrations`), one address holds
one key at a time, and the reverse index means filling in a forward's source is
a lookup rather than a scan of the table per packet. That closes the resource
exhaustion (M7) and stops one source accumulating entries.

**Not built:** the hijack itself. `TypeRegister` is authenticated only by the
mesh-wide MAC, which every member can compute, and nothing ties the key in the
frame to the sender. A member can still register somebody else's WireGuard key
against its own address, and honest peers forwarding to that victim then deliver
to the attacker. Confidentiality holds — the payload is WireGuard-encrypted end
to end — so this is availability plus "who talks to whom".

The obvious cheap mitigation does not work: refusing to *move* an existing
registration to a new address would stop an attacker stealing an active
victim's slot, but it also breaks roaming, and roaming is the case relays exist
for. A phone that changes network must be able to re-register.

So the fix is a signature over the key by the device that owns it, which is a
change to the register frame — a wire format decision, and one that wants doing
once rather than twice:

- **Sign the register frame now**, with the device key that already exists.
  Smallest change, and it closes this specific hole.
- **Wait for Phase 4** — per-device credentials on the control plane, which
  ADR-018 already anticipates and which closes this and the acknowledged disco
  gap in one move. Slower, and until then the hole stays open.
- **Both**: an interim signature that Phase 4 later subsumes, accepting that the
  frame format changes twice.

Worth noting the same root cause is documented in SECURITY.md as the Phase-4
disco gap, so this is not a new class of problem — it is the same one, in the
relay.
