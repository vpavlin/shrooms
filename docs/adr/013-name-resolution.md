# 013. Name resolution: hosts file now, DNS server next

**Status:** accepted; hosts file implemented, DNS server planned (M6)

## Context

Overlay addresses are derived from device keys, so they are stable but
unmemorable — `fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf`. Reaching a machine by
name is the difference between a working mesh and a pleasant one.

The daemon already holds the full roster, with each peer's name and address, so
the information exists. The question is only how to expose it to the resolver.

## Decision

Two layers.

**Now: `shrooms hosts`** writes `/etc/hosts` entries in a marked block.
Zero dependencies, works immediately, useful for a fixed set of machines.

**Next (M6): a DNS server in the daemon**, authoritative for the mesh domain
only, with per-OS resolver wiring.

## Why the hosts file is not enough

- **Static.** Every roster change needs a regeneration, so a newly joined peer
  is unreachable by name until someone re-runs it.
- **Privileged.** Editing `/etc/hosts` needs root.
- **Absent on Android**, which is the platform this project is ultimately for.
  There is no writable hosts file without root.

## Why a DNS server is the portable answer

Every target platform has a supported hook for *domain-scoped* resolution, and
the ordering is the opposite of intuition:

| | Mechanism |
|---|---|
| **Android** | `VpnService.Builder.addDnsServer()` + `addSearchDomain()` — first-class API, declared while building the tunnel |
| **macOS** | `/etc/resolver/<domain>` containing `nameserver <overlay-ip>` |
| **Linux** | `resolvectl dns <iface> <ip>` + `resolvectl domain <iface> ~<domain>` under systemd-resolved |
| **Windows** | an NRPT rule for the domain suffix |

**Android is the easiest of the four**, which matters because it is the hardest
platform in every other respect. No root, no file editing, no resolver daemon to
negotiate with.

**Correction, found by shipping it.** Android is the easiest place to *install*
a resolver and the hardest place to scope one. `VpnService` has no split-DNS:
`addDnsServer` makes the tunnel's resolver receive **every** query the device
makes, and `addSearchDomain` does not narrow that. A resolver that answers only
for the mesh therefore removes the device's name resolution entirely — no
browsing, no app updates — which is what happened on the first build that
enabled it.

So on Android the resolver **must forward** what is not its own, and the
forwarding socket must be protected or it routes into the tunnel it is resolving
for. The queries are proxied verbatim and never logged; it is a pipe, not an
observation point. The "no forwarding" rule below still holds everywhere the
resolver is reached only for its own domain, which is every other platform.

The decisive property is that it is **live**: the roster is already in memory, so
a peer becomes resolvable the moment it announces, with nothing to regenerate.

## Why not mDNS

It is the obvious "names on a local network" answer and every OS supports it,
but it is multicast, and a WireGuard mesh is a set of point-to-point tunnels
with no multicast. Making it work would mean building multicast forwarding
first — more work than the DNS server, and worse.

## Constraints on the implementation

**Authoritative for the mesh domain only**, and refusing everything else where
the platform allows it to be scoped. A VPN that quietly becomes the system
resolver is a surprise nobody wants and a privacy leak besides. Android cannot
be scoped, so there it forwards instead — see the correction above. Names
outside the suffix are REFUSED rather than NXDOMAIN: REFUSED says "not mine",
NXDOMAIN would assert the name exists nowhere, which we cannot know.

**Bind port 53 on the overlay address only**, so it cannot clash with a resolver
on localhost. That needs `CAP_NET_BIND_SERVICE` alongside the existing
`CAP_NET_ADMIN`. A high port plus per-OS port configuration is possible —
macOS's `/etc/resolver` supports it — but systemd-resolved does not handle it
cleanly, so the capability is the simpler trade.

**Domain choice.** `.internal` is formally reserved by ICANN for private use and
is the correct answer; `.mesh` reads better and is what the hosts command
defaults to. Configurable either way, and worth settling before anyone depends
on it.

## Consequences

- The hosts command stays as a zero-dependency fallback rather than being
  removed. It needs nothing and works where a resolver cannot be configured.
- Names remain self-asserted (see ADR-008 — device identity is a key, not a
  name), so collisions are possible and must be disambiguated rather than
  resolved by last-writer-wins.
