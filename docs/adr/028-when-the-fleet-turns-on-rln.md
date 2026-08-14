# 028. When the fleet turns on RLN

**Status:** proposed; nothing to build yet, and one thing to ask

## Context

[ADR-004](004-public-fleet-not-own-cluster.md) chose the public fleet over a
cluster of our own, and one of the four reasons it gave was:

> **No RLN.** Cluster 2 has `rlnRelay: false`, which deletes per-device
> memberships (RLN's Shamir sharing leaks the key if one membership publishes
> twice in an epoch), the ~$5/6mo/device cost, and ZK proving on phones.

That is a load-bearing assumption, and it is one somebody else controls. RLN —
rate-limiting nullifiers, the spam protection Logos Delivery is built towards —
is being actively rebuilt, so this records what is true today, what it would
cost us, and what to do about it. It is a decision record with no decision in
it yet: the point is to have the analysis written down before the day the
messages stop arriving.

Because that is the failure mode. Under RLN a message without a valid proof
**must be discarded**, so if the shard we publish on is switched to enforcing
it, shrooms does not degrade — peers simply never appear, with no error
anywhere. It is exactly the shape of the fleet migration that broke everything
silently on 2026-08-07.

## What is true today

Read from source on 2026-08-14, not from documentation:

**The rate model is per membership, not one message per epoch.** RLN-v2
("RLN-Diff") attaches a `user_message_limit` to each membership.
`logos-co/logos-lez-rln` bounds it:

```rust
pub const MIN_RATE_LIMIT: u64 = 100;
pub const MAX_RATE_LIMIT: u64 = 600;
```

The **epoch length is a messaging-layer parameter, not a chain one** — nwaku
carries `epochSizeSec` (code default 1) while the chain stores only the rate.
So "600 per epoch" means nothing until the deployment says how long an epoch
is, and that number is not fixed anywhere we could find.

**It is moving off Ethereum onto LEZ.** `logos-lez-rln` is the membership
registry as a RISC Zero guest program with its own sequencer, pushed within the
week. This matters to us twice over: the Logos Execution Zone wallet already
lives in Basecamp, and there is a `lez-faucet` for funding testnet accounts.

**It is not wired into delivery yet.** `logos-rln-e2e` lists its `delivery`
scenario as *planned*: "RLN-gated delivery once logos-core wires RLN-on-LEZ
into it". nwaku's own RLN still targets an EVM chain — its on-chain group
manager carries `linea-sepolia` workarounds — and there is no LEZ reference in
the delivery tree at all. We are early, which is the only reason this ADR can
be written calmly.

**Memberships are bought by the unit and they expire.**

```rust
pub fn calculate_payment_amount(rate_limit: u64, price_per_unit: u128) -> u128 {
    price_per_unit * (rate_limit as u128)
}
```

`MembershipState` carries `active_duration` and `grace_period_duration`, so a
membership lapses and must be renewed — a second expiry clock beside the
credential one ([ADR-018](018-credentials-instead-of-a-shared-key.md)), on
every device, with its own failure mode.

## What it would cost us, concretely

Our publishing profile is one sealed announce per device per 45 seconds, plus a
services message every five minutes, plus bursts during enrolment. That is far
inside any tier: **rate is not our problem.** Registration is.

- **Every device that publishes needs its own membership.** The laptop, the
  phone, the VPS, k11, and every fren's device. Not one per mesh.
- **They cannot be shared.** The nullifier *is* Shamir sharing: two messages
  from one membership in one epoch reveal the secret. Our devices announce
  independently and reply to each other's announces, so a shared membership
  would be burned within minutes.
- **The phone proves.** Android publishes on the same 45-second loop and
  lightpush is no exemption — nwaku's own constants carry a
  "trigger proof refresh and publish retry in the lightpush client" path.
- **It creates the registry we deliberately do not have.** Addresses are
  derived, membership is a credential nobody else can see, and there is no
  directory anywhere. An on-chain membership is a permanent, enumerable public
  record that a key publishes on a shard.

That last point is the one that should decide this, not the money.

## The option that keeps the flow intact

There is a specification for exactly our problem:
[LIP-158, RLN Membership Allocation](https://github.com/logos-co/logos-lips/blob/master/docs/anoncomms/raw/rln-membership-service.md)
(`/logos/rln/membership/1.0.0`), whose motivation names it:

> "clients to have a wallet with sufficient funds to pay for gas fees, and
> access to a node for the blockchain network"

A **membership provider registers identity commitments on behalf of clients**.
The client generates its own commitment and keeps the secret — so no shared
membership and no transferred slashing risk — and sends it with a pluggable
authentication payload and an optional requested rate limit. The provider pays.

The fit is close enough to be suspicious, and it is worth stating plainly: **the
authentication mechanism we would plug in is the credential we already issue.**
A device holding an admin-signed credential for a mesh is precisely "an
eligible client", and the provider is a node that already has to exist — the
relay ([ADR-012](012-relay-hosting.md)) or Basecamp, where the LEZ wallet
already is. `shrooms join <token>` stays one command and the membership happens
behind it.

What it costs: somebody funds the provider, and the provider learns which
network identity asked for which commitment. The specification says that
linkability is out of scope and points at stealth commitments as future work.
For a mesh whose provider is the same person who runs the relay and holds the
admin key, that is not a new party learning anything new.

## Our own cluster, and why the spam answer is not PoW

Running our own cluster with RLN off remains open, and reopens ADR-004 on its
own terms: nodes to run, and the loss of crowd cover. The second cost gets
sharper rather than softer under RLN, since on-chain memberships are
enumerable.

The instinct that a private cluster still needs spam protection is right. The
instinct that it should be proof of work is, we think, wrong:

**The question is admission, not rate.** Proof of work taxes the honest phone
on every publish — ours publishes every 45 seconds, forever — while an attacker
with a GPU does not notice it. We would be paying a continuous cost to impose a
negligible one.

**We already have the machinery.** Every control message is sealed under the
network key, and every member holds an admin-signed credential. A relay can
require proof of membership *to peer at all* and drop what does not decrypt.
That is cheaper than proof of work, strictly stronger, and reuses ADR-018
instead of inventing a second, weaker spam story beside it.

Free-riding on our wires is then a membership question, which is the one part
of this system that is already solved.

## What to do now

**Nothing to build.** Delivery is not RLN-gated, the LEZ work is weeks old, and
building against parameters that do not exist is how you get a second
implementation to keep correct.

**Two questions for the Delivery team**, neither answerable from source:

1. What `epochSizeSec` does the delivery deployment intend? A rate of 100–600
   is meaningless without it, and it is the only number that could make our
   enrolment bursts a problem.
2. Are `free_quota` and the faucet meant to cover ordinary users, or only the
   testnet? The answer decides whether allocation is a product or a demo.

**One thing to watch.** `logos-rln-e2e`'s `delivery` scenario going from
*planned* to *in progress* is the signal that this stops being theoretical.

## Consequences

- If RLN arrives and allocation is available, the user flow is unchanged and we
  gain a dependency on a provider being funded — which is the same shape as
  depending on a relay, and the same person usually runs both.
- If it arrives and allocation is not available, the honest choices are our own
  cluster or telling people to fund a wallet per device. The second one ends
  the project as pitched.
- Either way, a second expiry clock exists per device, and it can take a device
  off the mesh exactly like a lapsed credential can.

## What would change our mind

A deployment note saying cluster 2 keeps `rlnRelay: false` indefinitely for
low-rate applications would close this ADR unbuilt. So would delivery-layer
support for publishing through a relay that holds the membership, which is
LIP-158 one layer lower and would make the whole question somebody else's.
