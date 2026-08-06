# 003. Waku as rendezvous, not a live control plane

**Status:** accepted

## Context

The natural reading of "use a messaging network instead of a coordination
server" is to put the control plane on it: peers continuously subscribed,
endpoint updates gossiped in real time.

## Decision

Waku is a **bootstrap and repair** channel. It is used at cold start, on network
change, and to repair a partition. It is not required in steady state.

## Why

**WireGuard relearns a peer's endpoint from any correctly-authenticated packet.**
A node that roams and sends first is relearned by every peer with no signalling
at all. With `PersistentKeepalive` set, that is self-healing.

So the steady state needs no control plane, and once any tunnel exists the mesh
can carry its own control traffic — the pattern wesher and tinc use.

Three consequences make this more than an optimisation:

- **Android forces it.** Both nwaku and go-waku call `disconnectAllPeers()` if
  the keepalive loop is late by more than ~30 s. Doze suspends the SoC, so every
  Doze cycle tears down every Waku connection. Persistent connectivity is not an
  available behaviour on a phone.
- **Core mode is incoherent intermittently.** Gossipsub is a *maintained*
  overlay: GRAFT to `dLow=4` needs seconds of heartbeats, the message cache holds
  ~6 s of history, and scoring punishes graft-then-vanish. A node appearing for
  3 s every 15 minutes never forms a mesh and is a liability to its peers.
- **It makes the battery question disappear** rather than needing to be tuned.

## Consequences

- Waku Store becomes load-bearing: intermittent peers may never be online
  simultaneously, so the async mailbox matters.
- Revocation is slower, bounded by how often a node checks in. Mitigated by
  short-expiry credentials (ADR-008).
- Discovery code must be **periodic and idempotent**, never session-shaped, or
  the Android port becomes a rewrite.
