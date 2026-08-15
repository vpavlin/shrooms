# Open questions from the 2026-08-15 security audit

Decisions I did not want to take alone while working through
`SECURITY_AUDIT.md`. Each one is skipped in the code for now, with the item it
belongs to. Nothing here is blocking: the security gap each finding names is
closed by the part that *was* built, and these are the choices about how far to
go beyond that.

## H1 — should a revocation also be re-published when a peer joins?

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

## H1 — retention: revocations are kept for a fixed 30 days

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

## M9 — how promptly should an expired credential drop a live tunnel?

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

## M1 — what should `ui_listen` actually serve?

Making it read-only is agreed. The question is what "read" includes: `/status`
alone, or `/status` and `/logs`. Logs carry peer names, addresses and mesh
labels, and the listener is reachable by any local user on a multi-user host.
`/status` carries the same information in a tidier form, so the honest answer
may be that both are equally sensitive and the split should be by *verb* rather
than by path.
