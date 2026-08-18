# 029. Disco proves the device, not just the mesh

**Status:** accepted and built, both halves. Written as the first half of what
SECURITY.md calls Phase 4, with the relay half deferred as "separate and
larger"; it turned out to be neither. See the amendment at the end.

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

**Anything about traffic.** Disco has never been able to read or redirect
traffic; WireGuard's handshake is what authenticates a peer. This closes an
influence on path *selection*, not a hole in the data plane.

## Amendment: the relay half, and why it cost almost nothing

*Added after building it — the estimate above was wrong in a useful direction.*

This ADR predicted that closing the relay registration hijack needed a
credential on the register frame, an authority held by the relay, and that a
mesh with no admin keys could not have the check at all. Built, it needed none
of those.

The register frame carries the device's public key, a timestamp, and an ed25519
signature over both — the same shape as the disco change, one layer down. The
relay then asks a question it can already answer: **does this device own this
WireGuard key?** The roster knows, because every member learned that binding
from a signed announce long before any relay registration arrived. So ownership
is checked against state the relay already holds, not against a credential the
frame carries.

What that buys, beyond the smaller diff:

- **It works on a mesh with no admin.** The check reads the roster, and the
  roster exists whether or not anybody ever minted an authority. The predicted
  design would have left exactly the meshes with the least ceremony — a couple
  of machines sharing a network key — with no protection.
- **The relay stays a forwarder.** It verifies a signature and consults
  membership it already tracks. Had it needed to hold an authority and validate
  credentials, a relay would have become a thing with opinions about who is a
  member, which is the coordinator this project exists to avoid.
- **The timestamp does the rest.** `RegisterSkew` (two minutes) means a captured
  register frame cannot be replayed later to point a key somewhere stale.

The general lesson, which is why this is written down rather than quietly fixed:
the credential was reached for because the question sounded like an
authorisation question. It was an *identity* question, and identity was already
established by the announce. Reach for the roster before reaching for a
credential.

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
  an instructive way: neither half needed credentials at all.
- Audit M5 (relay registration hijack) is closed by the amendment above, on
  every mesh rather than only on meshes with an admin.
