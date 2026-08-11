# 014. Relay discovery: a flag on the announce, not a separate message

**Status:** accepted; implemented and proven on real infrastructure (M3)

A phone on carrier-grade NAT reached a peer through a relay on a public VPS,
with neither end told the relay existed — both found it in the roster, and
selection is made per mesh. The deterministic-selection rule below has one
consequence worth stating here: candidates are drawn only from peers *believed
online*, so a node whose rendezvous plane fails silently stops being selected
and quietly stops relaying, with nothing reporting an error. That is what the
daemon's rendezvous watchdogs exist to catch.

## Context

"A small VPS is acceptable, but it must be auto-discovered — no hardcoded IPs"
was an original project constraint. Until now it was the one that was broken:
`relay_addr` was configured by hand, so a fresh client had no way to learn a
relay existed at all.

DESIGN §4 originally specced this as a distinct message on its own topic:

```
RelayAnnounce{relay_pk, addrports[], seq, expiry, sig} → /vpn/1/relay/
```

Store-backed, fetched with `waku_store_query` on start.

## Decision

**A relay is just a peer that is willing to forward, so it is advertised as a
`relay` boolean on the ordinary `EndpointAnnounce`.** No second message type, no
second topic, no second subscription, no Store dependency.

```go
// Relay says this device will forward traffic for peers that cannot reach
// each other directly.
Relay bool `json:"relay,omitempty"`
```

## Why this is better than the original spec

**Everything a relay needs is already what an announce carries.** Its keys, its
candidate addresses, its liveness. A separate message would have restated all of
it under a second signature.

**Relays inherit the whole peer pipeline for free.** Endpoint validation, path
probing, offline detection, replay protection, epoch rotation and padding all
apply unchanged. A relay address is only ever used after probes have actually
reached it — the ADR-009 rule, which we would otherwise have had to re-derive
for relays specifically.

**It removes the Store dependency from a critical path.** S2 (logos.dev
retention) has never been run; making relay discovery depend on Store would have
made an unmeasured property load-bearing. Relays are always-on by definition, so
the live announce stream is sufficient for them in a way it is not for
intermittent peers.

**One fewer subscription** on a shard we share with the rest of the fleet.

The cost is that a relay is not discoverable to a node that has not yet heard a
live announce — up to one announce interval. For an always-on relay this is
bounded and small, and the node has nothing to relay in its first seconds anyway.

## Selection must be deterministic across nodes

This is the non-obvious part and the reason the implementation is not simply
"pick the fastest".

The relay forwards by destination WireGuard key, and can only do so for a peer
that has registered with it. **If A and B pick different relays, their traffic
never meets** — each is registered somewhere the other is not sending. Selection
must therefore be a pure function of state both ends already share.

So: **lowest device ID among online, probe-confirmed relays.** Deliberately
*not* lowest RTT, which each side measures for itself and would disagree on.
`TestSelectRelayIsDeterministicAcrossNodes` pins this — it fails if the tiebreak
is changed to RTT, which is exactly the plausible-looking "improvement" someone
would otherwise make.

Two further rules fall out:

- **Only probe-confirmed addresses.** Unlike a direct endpoint, WireGuard cannot
  relearn a relay path from an inbound packet, so an unverified relay address
  blackholes silently and permanently rather than self-correcting.
- **A relay never selects an upstream relay.** It is publicly reachable by
  definition, and self-selection would loop.

## Consequences

- `relay_addr` survives as a config escape hatch to pin a relay — useful for
  bringing up a mesh whose relay has not announced yet, and for debugging — but
  is not needed in normal operation.
- **Any node can become a relay by setting one config value**, with nothing to
  distribute afterwards. This is what makes the volunteer-relay idea in
  [ADR-012](012-relay-hosting.md) a matter of incentives rather than code.
- A relay's IP may change freely; the announce carries the new one.
- Relay willingness is self-asserted, like names (ADR-008). A member that
  advertises `relay = true` and then drops traffic degrades the mesh for pairs
  that select it. Bounded by the fact that it is *already* a member — it could
  drop traffic anyway — and by relays being probe-confirmed before use. Ranking
  relays by observed reliability is the answer if this ever bites, but it must
  preserve cross-node determinism, which naive per-node scoring would not.
