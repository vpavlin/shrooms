# 021. A synthetic IPv4 address per peer

**Status:** accepted; built, wired in, and in daily use

`internal/v4` is carried by the daemon and by the Android app, not only by its
tests: every node holds a 198.18.0.0/15 alias per peer, split into a block per
mesh ([ADR-015](015-multiple-meshes-one-daemon.md)). This is what makes a
browser work on an IPv4-only network, which is the failure the rest of this
document is about.

## Context

Mesh names do not work in a browser, and they do work in a terminal on the same
phone, on the same tunnel, at the same moment. Measured rather than reasoned
about, from the app's DNS counters during one page load in Brave:

```
arrived 24 · answered 0 · refused 0 · forwarded 4
unanswerable 5 · ipv4-only 15 · other-type 5
```

15 + 5 + 4 = 24: every query is accounted for. The browser sent **fifteen `A`
queries, five of another type (`HTTPS`/type 65), and not one `AAAA`**. The
resolver answered every one correctly — NOERROR with no records, because the
overlay is IPv6-only and there is no A record to give — and the browser
concluded the name was unusable.

Nothing is broken. [ADR-005](005-derived-addressing.md) makes the overlay
IPv6-only because addresses are *derived* rather than allocated, which needs
128 bits; that decision is sound and is not in question here. The problem is
that Chromium probes for IPv6 connectivity and **stops sending AAAA queries
when the probe fails**. Our prefix is a ULA with no global route, so on a
v4-only network the probe fails and the browser asks only about IPv4.

This also retires a mystery that had been open for weeks and had been
misdiagnosed three times (qmldir, stale upstream, Private DNS). "Names work
sometimes" was never intermittent: on 5G the underlay is v6-native, the probe
succeeds, AAAA queries are sent, and everything works. On v4-only wifi it is
not. The variable was the underlay, not the mesh.

`getaddrinfo` has no such behaviour, which is why `ssh jimmy-crib.mesh` from
Termux has always worked.

**Connecting over IPv6 is fine.** `http://[fd93:…]/` loads in the same browser
that cannot resolve the name — confirmed, and it is how the mesh's web
interfaces are reached today. So the tunnel, the routes and the IPv6 data path
are all healthy. The gap is one DNS record type wide.

## Decision

**Answer `A` queries with a synthetic IPv4 address that maps 1:1 to the peer's
overlay address, and translate at the tun.**

```
ha.jimmy-crib.mesh.  AAAA  fd93:…:22a8      unchanged
ha.jimmy-crib.mesh.  A     198.18.x.y       synthetic, local to this device
```

Three properties make this much smaller than it sounds.

**The synthetic address never leaves the machine.** It is an alias this device
uses to talk about a peer; the packet is translated to IPv6 before WireGuard
encrypts it, and back on the way in. Nothing is announced, no peer needs to
agree, and two devices may pick different aliases for the same peer without
anyone noticing. There is therefore **no allocation problem and no
coordination** — which is the objection that would otherwise sink this, given
that avoiding coordination is the point of the project.

**The mapping is stateless; the inbound direction is not.** The mapping is a
deterministic function of the peer both ways. Translating *outbound* is
therefore a pure function. Translating inbound is not, and this was wrong in
the first draft of this ADR: a packet arriving from a peer is either the reply
to a translated flow, which must become IPv4 again, or ordinary IPv6 traffic,
which must not — and nothing in the packet distinguishes them. Translating
everything would break `http://[fd93:…]/`, which is how the mesh's web
interfaces are reached today.

So a flow is remembered when it is translated, and inbound packets matching one
are translated back. One entry per connection made to an alias, expiring on
idleness. The alternative that would restore statelessness — a second overlay
address per device marking translated traffic, in the manner of SIIT — needs
every node to widen its AllowedIPs, which makes it a wire-visible change
requiring the whole mesh to update. That trade was not worth it for a table
that holds a handful of entries.

**The surgery already exists.** `internal/dns.Intercept` reads and writes raw
packets on the tun and builds IPv6/UDP replies with correct checksums today
(that is how the resolver is reachable on Android at all). Translation is the
same kind of work in the same place.

### The address range

**198.18.0.0/15**, the RFC 2544 benchmarking range. 128k addresses, far more
than a personal mesh needs, and chosen mainly for what it avoids:

| range | why not |
|---|---|
| `100.64/10` | carrier-grade NAT. What Tailscale uses, and it collides with exactly the mobile networks a phone sits behind |
| `10/8`, `192.168/16`, `172.16/12` | somebody's home LAN, including yours |
| `240/4` | reserved; several stacks still drop it |

The alias is `198.18` ‖ the low 16 bits of `SHA256("mesh/v1/v4alias" ‖
device_pub)`. Collisions are possible and local, so each node resolves them by
rehashing with a counter until its own roster is unambiguous. With a handful of
devices the probability is negligible; the point is that a collision is a local
inconvenience rather than a protocol failure.

### What must be built

- The mapping, and its inverse, in a new `internal/v4` package: pure functions
  over a roster, testable without a tunnel.
- `A` answers in `internal/dns`, alongside the existing AAAA ones. This is the
  part that makes browsers work and is worth having even if translation lands
  later — a name that resolves to an unreachable address is at least a
  diagnosable failure rather than a silent one.
- Header translation both ways, per RFC 6145: rewrite the IP header, fix the
  TCP/UDP checksums, translate ICMP echo, and set DF so fragmentation is never
  needed rather than handled. Checksums are recomputed rather than adjusted
  incrementally — the delta form is the textbook answer and is also where this
  code usually goes wrong, and at this MTU a recompute is a few hundred
  additions.
- MSS clamping on translated SYNs. The IPv6 header is 20 bytes larger, so a
  segment sized for the v4 MTU no longer fits; without this a connection opens
  and then hangs on the first full-size response, which is the worst kind of
  bug to find later.

## Alternatives

**One local IPv4 for the name router.** Answer *every* name with a single
address and let the ADR-019 router demultiplex on `Host` and SNI. No mapping,
no translation. Rejected on the phone, which is where the problem is: something
has to accept a TCP connection on port 80, and an unprivileged Android app
cannot bind it. Doing so needs a userspace TCP stack on the tun — which is the
netstack work considered and set aside earlier — and it would still only cover
HTTP and TLS, leaving `ssh vps.mesh` broken. More machinery for less result.

**Do nothing, and document it.** Names work in terminals; browsers get a
bracketed IPv6 URL. That is the status quo, and it is what has been happening
by accident — including the weeks spent believing the resolver was faulty. The
names are the feature that makes the mesh pleasant to use, so this is a real
cost rather than a cosmetic one.

**Make the browser's probe succeed.** Route Chromium's probe destination
through the mesh. Dishonest, fragile, and specific to one browser's current
implementation.

**Serve A records for a NAT64 prefix and let the platform CLAT handle it.**
Requires a 464XLAT-capable platform and a real NAT64 gateway; Android has the
former and we would have to be the latter, which is this ADR with extra steps
and less control.

## Consequences

- Browsers reach mesh names on v4-only networks, which is the majority of wifi.
- `ssh`, `curl` and anything else keep working unchanged, over either family.
- Two addresses now name the same peer, and `status` should show both, or
  people will report the v4 one as a bug.
- The translator is on the data path, so it must be measured rather than
  assumed: an extra copy per packet is acceptable, a per-packet allocation is
  not.
- ICMP translation is where this kind of code is usually wrong, and PMTU
  discovery depends on it. Worth testing explicitly rather than by using it.
- If [ADR-015](015-multiple-meshes-one-daemon.md) lands, the alias must be
  derived per mesh as well as per device, or two meshes could hand the same
  device two names for one address.
