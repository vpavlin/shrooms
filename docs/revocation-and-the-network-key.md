# Revocation, the network key, and the middle ground

Vaclav, 2026-08-22, on the audit finding against ADR-018: *"the adr is tough — is
there some middle ground?"*

Yes. This note sets out the gap, why the obvious fix is as expensive as it looks,
and the cheaper thing that gets most of the value.

## The gap

[ADR-018](adr/018-credentials-instead-of-a-shared-key.md) says:

> The revoked device keeps whatever it already had — nothing can reach into it —
> but it **stops receiving announces, so it cannot follow anyone who moves**, and
> no member will establish a new tunnel with it.

That is false as written. It presumes announces are wrapped per recipient, so
that dropping a device from the roster drops its ability to decrypt.
Per-recipient wrapping was never built — [ADR-020](adr/020-one-announce-many-readers.md)
records the decision not to, and ADR-018's own scope note says so — but the
revocation section was never reconciled with it.

Announces are sealed under the shared network key alone. Revocation removes the
device from *our* roster and refuses *its* announces: the inbound direction only.
Nothing on the outbound side consults the roster or the revocation list.

So a revoked device — a stolen laptop, a phone handed on — keeps deriving the
rendezvous topic and decrypting every announce on the mesh: every peer's name,
overlay address, external endpoints, relay use, bound ports and service list,
refreshed every 45 seconds, **for as long as the mesh exists**. It cannot join a
tunnel, because no member will peer with an expired or revoked credential. It
can watch indefinitely.

`SECURITY.md` is honest about the network key exposing the control plane. ADR-018
is not, and it is the document somebody reads to find out what revocation buys.

## Why the obvious fix is expensive

Rotate the network key on revocation. Everything downstream derives from it —
the rendezvous topic, the announce seal, the overlay prefix, each device's
address — so a rotation is not a key change, it is a new mesh wearing the old
one's name. Every peer re-keys at once, every offline device strands, and the
recovery path for a stranded device is a fresh invite, in person.

That is a real cost to impose on every revocation, most of which are "I sold the
old phone" rather than "somebody has my laptop".

## The middle ground

**Separate the two things the network key is doing.** It is both the *identity*
of the mesh (prefix, addresses, topic) and the *reading key* for the control
plane. Only the second needs to change when somebody is revoked.

Announces already take an epoch: `Seal(nk, epoch, …)`, `topic.Current(nk, now)`.
The epoch is a clock, so the key already rotates in time — but any holder of the
network key derives every epoch, which is why that rotation buys nothing against
a former member.

Give the derivation a second input: a **membership generation**, a counter that
only current members learn.

    announceKey = HKDF(nk, epoch ‖ generation)

- The mesh identity — prefix, addresses, DNS names — stays derived from `nk`
  alone, so **nothing renumbers** and a generation bump is invisible to users.
- On revocation the admin bumps the generation and publishes the new value
  **wrapped once per current member**, to each device's key. That is the
  per-recipient wrapping ADR-020 declined — but ADR-020 declined it *per
  announce*, tens of times a minute, forever. Per *rekey* it runs once per
  revocation, on a roster of a dozen devices. The cost that made it a bad idea
  is not present here.
- The revoked device holds `nk` and cannot derive the new generation, so it goes
  deaf at the next epoch. Which is what ADR-018 already claims.

**Stragglers.** A device that was offline for the rekey cannot read the new
generation, and cannot ask for it on a topic it can no longer derive. Two
mitigations, both needed:

1. **Honour the previous generation for a grace window** — a week, say. The
   revoked device keeps reading for that week, which is a deliberate trade: it
   is the difference between "revocation is slow" and "revocation strands your
   own devices". For the stolen-laptop case, see the override below.
2. **Rekey messages are republished** on the old generation's topic for the
   window, so a device that wakes on day three catches up without a human.

**`--rotate-now`**, for when the grace window is the wrong answer. Skips it,
accepts that anything offline needs a fresh invite, and says so before doing it.
That is the stolen-laptop path, and it should be a deliberate keystroke.

**A `--no-admin` mesh cannot do any of this.** There is nobody to sign a rekey,
and the network key *is* membership there ([ADR-008](adr/008-network-key.md)).
Revocation on such a mesh means re-inviting everyone onto a new mesh, and the
honest thing is to say so in `admin revoke` rather than implying otherwise.

## What is cheap and should happen regardless

**Correct ADR-018 now.** The claim is false today and will stay false until the
above is built. It costs nothing to say what revocation actually buys — no new
tunnels, no data-plane access — and what it does not: the former member keeps
reading the control plane.

## Recommendation

Correct the ADR immediately. Build the generation counter when there is appetite:
it is a contained change — one extra input to a derivation, one new signed
message type, one grace window — and it converts a documented promise that is
currently untrue into one that is true.

Not urgent for the mesh as it stands, where every device belongs to one person.
It becomes urgent the first time a mesh has a member who might leave badly.
