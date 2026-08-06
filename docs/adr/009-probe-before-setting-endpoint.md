# 009. Probe candidates before setting a WireGuard endpoint

**Status:** accepted

## Context

A peer announces several candidate endpoints: LAN addresses, a reflexive
address, maybe a configured one. Only some will work. The obvious approach —
"spray WireGuard handshakes at all of them and let the best win" — is what
Nebula does.

## Decision

Probe with small encrypted packets first (`internal/disco`), then set the one
that answered as WireGuard's endpoint.

## Why

**WireGuard holds exactly one endpoint per peer.** You cannot spray at five
candidates concurrently; you would be overwriting the endpoint under yourself
and could not tell which attempt succeeded. Nebula can spray because it owns its
own protocol end to end; we are driving WireGuard.

Probing separately also buys two things:

- **Liveness.** A candidate is only usable once it has answered, which
  distinguishes "the peer announced this address" from "packets actually reach
  the peer there". Tests cover a black-holed candidate never being selected.
- **Reflexive discovery.** Every pong echoes the source address it was observed
  at, so peers tell each other their public ip:port. With N peers that is N−1
  independent vantage points and no STUN server at all. Tailscale's source calls
  its equivalent "effectively a STUN response".

**The rule that silently breaks punching if wrong:** a ping is answered to the
address it *arrived from*, never to an announced one. Under endpoint-dependent
mapping a peer creates a different external port per destination, so the
observed address is the only one that can reach it.

Packets are encrypted rather than merely authenticated — see the commit history;
MAC-only left the sender's 32-byte device key in cleartext on every probe, a
stable identifier that follows the device between networks.

## Consequences

- Path establishment is slower than blind spraying: one probe round trip before
  WireGuard is configured. Mitigated by falling back to the first announced
  candidate, which is the M1 behaviour and works whenever one side is reachable.
- A separate protocol to version and maintain.
