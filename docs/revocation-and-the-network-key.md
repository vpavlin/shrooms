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

Give the derivation a second input: a **membership generation**, which only
current members can learn.

### What the generation actually is

Not a counter. That is the first thing to get right, and it is easy to get
wrong: if the generation were an integer, a revoked device holding `nk` would
compute `HKDF(nk, epoch‖1)`, `‖2`, `‖3` and stay exactly as deaf as it is now,
which is not at all. **The generation must be unguessable to a holder of the
network key**, so it has to be a secret, not an index.

So it is a pair:

| | | |
|---|---|---|
| `N` | a small integer | **public.** Says *which* generation a message is under. Travels in the clear. |
| `S_N` | 32 random bytes | **secret.** Does all the work. Delivered wrapped to each current member. |

    announceKey = HKDF(nk ‖ S_N, epoch, "mesh/v1/announce")

`N` is a label so a reader knows which key to try, and so a node can tell "I am
behind" from "this is corrupt". `S_N` is what a revoked device does not have.

**Where `S_N` comes from.** The admin generates it at random when revoking. It
is not derived from anything — deriving it from `nk` or from the roster would
defeat the point, since a revoked device knows both. It does not need to be
derived from the admin key either, which means the admin stores nothing between
rotations: if the current value is ever lost, mint `S_{N+1}` and wrap that.

**How it reaches members.** Sealed to each current member's device key, one
small message per member, published on the rendezvous topic. That is the
per-recipient wrapping [ADR-020](adr/020-one-announce-many-readers.md) declined
— see below for why the objection does not apply here.

**What does *not* change.** The rendezvous topic stays `topic.Current(nk, now)`,
derived from the network key alone. It has to: a device that missed a rotation
must still be able to find the place where rekeys are published, and a device
being revoked is meant to lose the ability to *read*, not the ability to see
that a mesh exists. Splitting it this way also matches what `SECURITY.md`
already says — rendezvous genuinely needs a shared secret, because every member
must compute the same topic with no coordination.

The revoked device therefore still finds the topic, still sees sealed traffic on
it, and can no longer open any of it.

### It is a re-invite, to a degree — and that is the useful way to see it

Vaclav's observation, and it is the right one: a rekey is a secret sealed to each
device, which is structurally what an invite is. Worth being precise about where
the analogy holds, because both halves matter.

**Where it holds.** One small sealed message per device, to a key that device
alone can open. Same shape, and it should reuse the same sealing primitive
rather than growing a second one.

**Where it breaks — which is the whole argument for doing it.** An invite needs
a person running `shrooms invite`, a token carried to the other machine, and
both ends live inside fifteen minutes. It hands over a *new* identity: new
address, new DNS name, new credential. A rekey needs none of that. Nothing
renumbers, no credential is reissued, no human is present, and a device that was
asleep picks it up when it wakes. "Re-invite every device" is a weekend; this is
a keystroke and a fan-out.

### Who does the wrapping

The analogy pays off here. **Every current member already holds `S_N`** — so any
member can wrap it for any other member, and the admin does not have to.

That matters because the admin key is meant to be offline, and increasingly on a
card ([ADR-022](adr/022-keycard-for-the-admin-key.md)). A card can sign a short
statement. Asking it to perform key agreement against a dozen recipients is a
different proposition, and it would tie every rotation to having the card in
hand.

So split it:

- **The admin authorises**, with one signature over
  `{mesh_id, N, H(S_N), not_before}` — a statement, not a secret. This is what
  a card is for, and it is the same shape as a revocation.
- **Members distribute.** Any member seals `S_N` to any other member's device
  key. The recipient checks `H(S_N)` against the admin-signed commitment before
  accepting it, so a rogue member cannot inject a generation of its own
  choosing — the worst it can do is stay silent.
- **Members refuse non-members.** A revoked device asking a peer for `S_N` is
  refused by the same membership check that already rejects its announces
  (`checkMembership`).

**The honest cost of that split.** Confidentiality of the new generation now
rests on every member refusing correctly, rather than on the admin alone. One
buggy or malicious member re-admits the revoked device.

That is a real weakening and should be written down — but it is a small one,
because a malicious member can hand over the network key itself today, and
nothing in this design has ever defended against a member who wants to. It is
the difference between "a member can leak the mesh" and "a member can leak the
mesh", which is to say no difference at all.

### Catching up

A device that missed generations `N+1…N+3` and is handed `S_{N+4}` cannot read
the backlog, because the values are independent. That is fine for announces,
which are refreshed every 45 seconds and carry no history worth recovering — it
only needs the current one.

If reading the backlog ever matters, a reverse hash chain gives it: pick a seed,
let `S_i = H^(K−i)(seed)`, and release in reverse. Holding `S_{N+4}` then yields
every earlier value and no later one. The cost is that the admin must keep the
seed between rotations, and `K` bounds how many rotations a mesh can ever have.
Not worth it for announces; noted because it is the obvious next question.

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
