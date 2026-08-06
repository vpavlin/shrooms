# 004. The public logos.dev fleet, not our own cluster

**Status:** accepted

## Context

Waku supports private networks: pick a cluster ID in 16–65535, run your own
bootstrap nodes, and the mandatory metadata handshake hard-disconnects anyone on
a different cluster. That gives a genuinely isolated network.

## Decision

Use the public `logos.dev` fleet (cluster 2).

## Why

- **Nothing to run.** The six entry nodes are `/dns4/` multiaddrs hardcoded in
  the library — DNS names, not IPs. The bootstrap anchor costs nothing and
  survives the VPS moving.
- **No RLN.** Cluster 2 has `rlnRelay: false`, which deletes per-device
  memberships (RLN's Shamir sharing leaks the key if one membership publishes
  twice in an epoch), the ~$5/6mo/device cost, and ZK proving on phones.
- **Crowd cover.** Sharing a shard with other applications is the only anonymity
  set available.
- It is the user's own ecosystem's fleet.

## Consequences

- **It is a dev fleet with no SLA.** It may be reset or reconfigured. This is a
  real dependency for a daily driver.
- Being a public bus makes the rotating-topic design (ADR-006) and payload
  encryption load-bearing rather than optional.
- Mix is enabled on cluster 2, which matters for Edge-mode publishes — see
  DESIGN §9 R1.

## Mitigation

Nothing is pinned but one 32-byte mesh public key, so moving to another cluster
(or our own) is a config change rather than a redesign. Keep that path warm.
