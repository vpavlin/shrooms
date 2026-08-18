# 029. Disco proves the device, not just the mesh

**Status:** accepted; this is the first half of what SECURITY.md calls Phase 4.
The relay half is separate and larger — see "What this does not close".

## Context

A disco packet is encrypted and authenticated under a key derived from the
network key, so an outsider can neither read nor forge one. Inside, it names the
device that sent it:

```go
type Message struct {
    Type      byte
    SenderPub [32]byte
    TxID      TxID
    Observed  netip.AddrPort
}
```

Nothing proves that name. Every member holds the same disco key, so any member
can compose a packet claiming to be any device. `HandlePong` compares
`SenderPub` against the peer it probed — which stops a *different* peer's reply
being credited, and does nothing about a member who simply writes the right name
in.

The consequence is bounded and real. Disco selects a candidate path; WireGuard's
own handshake still authenticates the peer and encrypts the traffic, so this is
not a route to reading anything. What it is:

- **Path steering.** A member answers our probes as somebody else and decides
  which address we treat as working.
- **Reflexive poisoning** (audit M6). A pong carries the address the peer claims
  to have observed us at, and we advertise that to everyone. A hostile member
  seeds addresses of its choosing; if it names a third party, every honest member
  probes there. Addresses that could not be an external view of this device are
  refused now, which removes the arbitrary-target case and not the rest.

SECURITY.md has documented this as deferred since the beginning, on the reading
that a personal mesh is mutually trusted. That reading stops being true the day
a mesh is shared with somebody, which is precisely what this project is being
launched to do.

## Decision

**Sign the disco payload with the device key it already names.**

The signature covers the inner plaintext — type, sender, transaction id and
observed address — and is made with the ed25519 device key whose public half is
`SenderPub`. A receiver verifies against that key before acting on anything in
the packet.

### Why this is the cheap half

It needs no credentials and no authority. The claim being checked is "this
message came from the holder of the key it names", which is a property of the
key alone. So it works identically on a mesh whose membership is the network key
and on one with an admin — unlike the relay's registration problem, where the
useful claim is "this WireGuard key belongs to that device", a binding that only
a credential carries.

The key is already there. Every disco message names its sender, every peer we
probe came from an announce we verified, and the announce is where we learned
that device key. Nothing new has to be distributed, cached or looked up.

### Size, and why the constant stays constant

A packet goes from 104 bytes to 168 — one ed25519 signature — and remains a
fixed size regardless of type or address family, which is the property that
makes ping and pong indistinguishable by length. That property was never about
the number being small.

The cost is a signature per probe and a verification per reply, on a path that
runs every few seconds per peer while a path is being found and every few
seconds to keep one alive. That is comfortably affordable on a phone; it is
worth measuring rather than assuming, and the measurement belongs in the commit
that lands it.

### What a receiver does with an unverifiable packet

Drops it, silently, and counts it. A packet whose signature does not check is
either a bug or somebody trying, and neither deserves a log line per packet on a
receive path — but a counter that says "some arrived" is what makes the
difference between "the mesh is quiet" and "somebody is doing that".

A packet from a device we have never seen is also dropped. We only probe peers
we learned from a verified announce, so a sender we cannot identify is a sender
we have no reason to answer.

## What this does not close

**The relay registration hijack** (audit M5). A member can still tell a relay
that another device's WireGuard key is reachable at its own address, blackholing
that peer's relayed traffic and learning who was trying to reach it. Fixing that
needs the relay to check a binding between a WireGuard key and a device, which
lives in the credential — so the register frame must carry one, the relay must
hold the authority, and a mesh with no admin keys cannot have the check at all.
Separate ADR, separate wire change, and the larger of the two.

**Anything about traffic.** Disco has never been able to read or redirect
traffic; WireGuard's handshake is what authenticates a peer. This closes an
influence on path *selection*, not a hole in the data plane.

## Consequences

- The disco wire format changes. Both ends must agree, so this is a flag day for
  a mesh mid-upgrade: an old node's packets fail verification at a new node and
  a new node's packets are the wrong length at an old one. Worth spending once,
  now, while every mesh in existence belongs to the people making the change —
  and worth bundling with the revocation format change rather than paying twice.
- A member can no longer steer another member's path selection or seed its
  reflexive addresses, which removes the two footholds the threat model
  currently concedes.
- SECURITY.md's "Disco authenticates mesh membership, not device identity" moves
  from deferred to closed, and its "resolved by M5 credentials" note is wrong in
  an instructive way: this needed no credentials at all.
