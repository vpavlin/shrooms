# 001. WireGuard for the data plane, not libp2p streams

**Status:** accepted

## Context

The obvious design, given that the project is built on a libp2p-based messaging
network, is to tunnel IP over libp2p streams: one substrate, one identity
system, NAT traversal inherited from the library.

## Decision

Carry traffic over WireGuard on its own UDP socket. Use libp2p/Waku only for
rendezvous.

## Why

The performance gap is not marginal. EdgeVPN, which tunnels over libp2p streams,
was measured against WireGuard on identical hardware:

| | libp2p streams | WireGuard |
|---|---|---|
| gigabit LAN | 106 Mb/s | 893 Mb/s |
| real WAN | ~30 Mb/s | ~859 Mb/s |
| CPU | 200% | 1–2% |

Raising the MTU to 9000 *collapses* the libp2p case to 8.6 Mb/s. libp2p's own
maintainers put IP-over-libp2p at 2–10× overhead
([libp2p/specs#626](https://github.com/libp2p/specs/issues/626), open since
Aug 2024 with no implementation).

The cause is structural: every existing libp2p VPN serialises all flows onto one
mutex-guarded ordered stream, so one lost segment stalls every flow — the
classic TCP-over-TCP meltdown that WireGuard's own documentation cites as the
reason it refuses to run over TCP.

For calibration, a Raspberry Pi 4 does 777 Mb/s–1.02 Gb/s of kernel WireGuard.
Any home WAN is slower than that, so WireGuard makes throughput a non-issue
while libp2p streams would make it *the* issue.

## Consequences

- Two transports to operate rather than one.
- NAT traversal must be built rather than inherited (see ADR-009). This is less
  of a loss than it appears: libp2p's DCUtR punches a hole for *libp2p's* socket,
  not WireGuard's, so it was never going to help.
- The data plane is boring and fast, which is what a daily driver needs.

## What would change our mind

A libp2p datagram API landing with measured throughput within ~2× of WireGuard.
`quic.SendDatagram` is reachable today via `conn.As`, but quic-go's own docs say
the datagram send path is unoptimised.
