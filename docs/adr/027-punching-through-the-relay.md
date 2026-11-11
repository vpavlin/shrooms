# 027. Punch through the relay we already have

**Status:** proposed; every part exists except the coordination. Reviewed
2026-11-08 (laptop/reviewer): the direction holds, on the conditions in
"Review, 2026-11-08" below; three of them are built ahead of the decision.

## Context

Two peers behind NAT cannot reach each other directly today. One of them dials,
the other's router drops the packet because nothing inside asked for it, and
after the probes time out the pair falls back to a relay. It works — that is
what [ADR-012](012-relay-hosting.md) is for — and it costs a round trip through
somebody else's machine for traffic that could have gone straight.

[ADR-009](009-probe-before-setting-endpoint.md) built the parts: peers report the address they
observe each other arriving from, the prober sends to every candidate, and
[ADR-024](024-ask-the-router.md) asks the router for a way in first. What is
missing is the one thing that makes two NATs open at once, and the README has
said so honestly for weeks: *punching between two NATed peers is unproven*.

**Logos Storage shipped this in v0.4.2**, which is what prompted writing it
down. They enabled libp2p's full set — AutoNAT v2 to decide reachability,
circuit relay v2 as a fallback, and DCUtR to upgrade a relayed connection to a
direct one — and notably had to *avoid* libp2p's `HPService` to keep the order
they wanted: "we want to control AutoNAT's result and try a port mapping before
starting the relay". That is our order too, arrived at independently.

We cannot use their implementation. [ADR-001](001-wireguard-not-libp2p-streams.md)
already noted why: DCUtR punches a hole for *libp2p's* socket, and ours is
WireGuard's. But the technique is not libp2p's property.

## Decision

**Coordinate a simultaneous open over the path the pair already share.**

When two peers are talking through a relay and neither has a direct path, they
exchange a short control message naming the address each has been observed
arriving from and a moment to begin. Both then send probes to the other's
observed address for a second or two. The first packet out of each NAT creates
the state its own router needs to accept the other's; whichever arrives second
is let through, the prober sees a reply, and the existing path machinery
promotes the direct route exactly as it does for a peer that was reachable all
along.

Three things make this smaller than it sounds:

**We already know where to aim.** Reflexive addresses are collected today —
`Mesh.Reflexive` returns what peers report observing — and `Reflexive` in the
status payload is what the UI shows for diagnosing symmetric NAT. That is the
same address a punch has to target.

**We already have somewhere to say it.** The pair are connected through a relay
by the time this matters, and the control plane is sealed per epoch under the
network key. A punch request is another control message, not a new channel.

**UDP is the easy case.** libp2p's DCUtR halves an RTT to line up two TCP
handshakes, because TCP simultaneous open is unforgiving. WireGuard is UDP: the
requirement is only that both sides send at roughly the same time, and sending
for a second or two costs a handful of packets and removes the need for
precision.

### It is a hint, not a mechanism

A punch that fails must cost nothing. The relay stays up throughout, the
existing path selection continues, and success shows up as an ordinary
direct path appearing in the roster. Nothing waits on it and nothing degrades
if it never works — which is the only honest posture for a technique that
[cannot work at all against symmetric NAT](009-probe-before-setting-endpoint.md), where each
new destination gets a different external port and the address a third party
observed is not the address the peer will be seen at.

Detecting that case is already possible and already reported: several distinct
reflexive addresses means endpoint-dependent mapping, and the status payload
carries them for exactly this reason.

## What we are not taking from them

**AutoNAT's reachability verdict, for now.** Their state machine — Reachable /
NotReachable / Unknown — decides whether to port-map and whether to relay. Ours
does both unconditionally and lets the result speak, which is functionally
equivalent and one fewer moving part. The verdict would be worth having as a
*diagnostic*: "this device is not reachable from outside, so peers reach it
through a relay" is currently invisible unless somebody reads endpoints. That
is a UI feature, not a traversal one, and it should be built as one.

**Configured relay and AutoNAT servers.** Theirs are named by flag
(`--relay-server`, `--autonat-server`) and must be reachable.
[ADR-014](014-relay-discovery-via-announce.md) discovers relays from
announcements instead, which is a hard requirement here: a relay address
compiled into every device is the coordination server this project exists to
avoid.

**libplum.** Their port mapping moved to a C library covering PCP, NAT-PMP and
UPnP-IGD. Ours ([ADR-024](024-ask-the-router.md)) does PCP and NAT-PMP in
stdlib Go and no UPnP, which is a real gap — plenty of consumer routers speak
UPnP and nothing else, and that is exactly the home network this is for.

The gap is worth closing and libplum is not the way to close it here. It is
MPL 2.0 with no dependencies and it builds for Android, so nothing about the
library objects. Our build does: one pinned native dependency already costs a
fetch script, a per-ABI payload in the APK, nix packaging, and a silent
outage when a fleet moved ahead of the version we had compiled in. A second C
library, cgo bindings and an Android cross-compile to gain one protocol that is
roughly three hundred lines of stdlib Go — SSDP discovery, fetch the device
description, one SOAP call — is the wrong trade for a best-effort convenience
whose failure is already handled. **UPnP-IGD in Go, in `internal/portmap`,
beside the other two.**

## Consequences

- Two NATed peers get a direct path where the NATs allow one, without a relay
  in the middle of every packet.
- One more control message, sealed like the rest, and one more reason for a
  node to send a burst of probes — bounded in time and count.
- Symmetric NAT on either side still means a relay, and no amount of
  coordination changes that. The reflexive addresses already say when.
- A test that matters is hard to run: it needs two distinct NATs, which
  containers do not provide. This is the same shape as
  [the fleet spikes](../../TESTING.md) and should be planned as hardware, not CI.

## What would change our mind

Measurement showing the relay path is cheap enough that the complexity is not
worth it. The mesh is small and relays are ours; if the round trip through a
VPS is not visible in practice, this is optimisation rather than function — the
honest reason to build it anyway is that a relay is a machine somebody has to
keep running, and a mesh that needs one less often is a mesh that survives its
owner losing interest.

Or measurement showing that today's probing already punches every pair whose
NATs allow it, and that the pairs left on a relay have endpoint-dependent
mapping on both sides, where coordination cannot help either (the review's
first point).

## Review, 2026-11-08

Asked for by the owner before deciding; done by laptop/reviewer against
master at ed75456. The direction holds. What it found, and what follows:

- **Probing already punches, continuously.** A relayed peer has no fresh
  disco path, so it is probed every round at all its candidates, reflexive
  ones and the address its probes came from included, from WireGuard's own
  socket. Two forgiving NATs already meet that way. A coordinated punch adds
  most for a peer that is not being probed (a backgrounded phone) and for a
  pair where one side maps per destination and the other filters by address
  only.
- **"Symmetric means relay" holds only when both sides are.** One forgiving
  side can be reached from a symmetric one once it has sent anything to the
  symmetric side's IP. Port prediction and spraying, for port-restricted
  against symmetric, are not to be built.
- **Authority.** The network key is held by every member and the epoch seal
  is not replay protection. A punch request must be signed by the requesting
  device (or carried inside its WireGuard tunnel), carry a nonce and an
  issued-at time, be refused when older than about 10 s or seen before, and
  come from a roster peer that is not revoked and is reached through a relay
  now.
- **Targets.** Never an address that is only in the request: one the relay
  attests to, else one in the peer's own signed announce, else where its
  probes came from. Public unicast only, at most two, not the relay's, not
  ours.
- **Rates.** About ten probes over two seconds per punch; one punch per peer
  a minute, backing off after failures to half an hour; a few a minute in
  all; a pair marked futile for an hour after repeated failures or when both
  sides map per destination.
- **Timing** relative, not by clock: the receiver starts on receipt and says
  "go", the sender starts on "go"; bursts last at least the longer of 2 s and
  three relay round trips.
- **UPnP-IGD** in Go as decided, answering only to the default gateway,
  bounded parsing, the WireGuard port alone, leased, never announcing a private
  or CGNAT "external" address, and through claimedByPeers like NAT-PMP.
- **Testing** in CI as well as on hardware: network namespaces with nftables
  emulate the NAT kinds, and the expected matrix is direct for independent ×
  independent and for address-restricted × dependent, relay for
  port-restricted × dependent and dependent × dependent, direct for two IPv6
  firewalls. The field matrix (home router, carrier NAT, venue Wi-Fi) stays a
  spike before release.

**Built ahead of the decision**, because each is wrong today whatever is
decided:

- **What a member can make the mesh probe and answer is bounded**
  (`internal/disco/bounds.go`). The same abuse the review describes for a
  punch request exists in announces: a member announcing a stranger's address
  made every member probe it. Announced endpoints are filtered to reachable
  addresses, deduplicated and capped at eight, public first; a peer is probed
  at most once a second; pongs to one source are a token bucket, against a
  ping replayed from a spoofed source.
- **A roamed endpoint is conceded to only with two-way evidence**
  (`yieldRoam`, `twoWay`): the relay, or an address a probe was answered from
  within `PathFresh`. Conceding to any roamed address pinned one-way paths,
  which punching would make more common.
- **Status reports each mesh's NAT and whether a peer's probes arrive**
  (`meshes[].nat`, a peer's `heard`; `shrooms paths`), so the measurement
  above can be made. The hint that existed looked at addresses with the
  disagreements already removed and could never fire.

