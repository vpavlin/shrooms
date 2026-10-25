# 071. The delivery node resolves names the way the machine does

**Status:** accepted 2026-10-25 by the owner; built. Settles the open question
in [dns-the-library-insists-on.md](../dns-the-library-insists-on.md).

## Context

A laptop running Mullvad could not join a mesh. Its daemon said `node is
offline` beside `Failed to query DNS … address=one.one.one.one`, while every
other program on it resolved names and the other laptop on the same wifi was
on the fleet.

The delivery library does not use the system resolver. Its
`dnsAddrsNameServers` defaults to Cloudflare, queried directly on UDP/53, and
that is how it resolves the fleet's `/dns4/` entry nodes and probes whether it
is online. Mullvad's DNS-leak protection drops DNS to any resolver but its own,
so both fail, the library decides it is offline, and the peer manager dials
nothing. Pinning entry nodes by IP does not help: the verdict comes from the
probe. Measured with `wakuspike` on 2026-10-25: with `dnsAddrsNameServers` set
to an unreachable address, all six fleet dials fail on DNS timeout; set to the
machine's own stub resolver, all six connect.

Nothing about this is particular to Mullvad. Most VPN clients with a kill
switch do the same (Proton, IVPN, NordVPN among them), as does a WireGuard or
OpenVPN setup that forces DNS through the tunnel, a corporate network that
permits only its own resolver, a router that redirects port 53 to a local
resolver, and any host with a DNS proxy holding 1.1.1.1. A VPN is simply the
most common way a laptop ends up on such a network.

## The question

Such a VPN wants everything through its tunnel: a full default route, its own
resolver set as the system's, and a firewall that drops what tries to go
elsewhere. Should Shrooms run over such a VPN, or avoid it?

## Decision

**Over it.** The VPN is, to Shrooms, another network the machine is on: a NAT
with a restrictive firewall and its own resolver. That is how Tailscale
coexists with Mullvad, and ADR-030 chose that shape. Routing around the tunnel
would send the machine's real address to the fleet every 45 s and its tunnel
packets past the ISP, which is the leak a Mullvad user pays to prevent. If
someone wants the mesh outside their VPN, the VPN's own split tunnelling does
it, on their terms, with no help from us.

So the delivery node resolves the way the machine does:

- `name_servers`, a device-level config key, is handed to the library as
  `dnsAddrsNameServers`. IP addresses only; a name here would need resolving by
  the thing that cannot resolve.
- Unset, the daemon reads `/etc/resolv.conf` and hands over what it names.
  Under Mullvad that is Mullvad's resolver, or the systemd-resolved stub on
  loopback, which forwards to it; either is allowed through. The container runs
  with host networking, so it sees the host's file.
- No resolv.conf, or none naming a usable address, and the library keeps its
  default. A phone has none, so Android honours only an explicit setting.
- Every place a node is built does this: the daemon, the CLI's short-lived
  node for `invite` and `join`, and the waiting node. A path that still used
  the default would still fail.
- The daemon logs which resolvers it handed over and where they came from,
  because the failure this prevents reads as "offline" with nothing pointing
  at DNS.

Falling back to resolv.conf changes behaviour for every existing node. That is
the point: it is what a reader assumes already happens, and a node that
resolves differently from the machine it runs on is the surprise, not the
other way round. Where the machine's resolver is worse than Cloudflare,
`name_servers` pins a better one.

## Consequences

- The library reads its resolvers once, at start. A VPN that rewrites
  resolv.conf when it connects leaves a running daemon on the old list until
  restart; the systemd-resolved stub does not move, which is why it is kept.
- Over a VPN, peers see the VPN's exit address. Mullvad has no port forwarding,
  so expect more relay fallback and fewer direct paths. Peers on the same LAN
  stay directly reachable only with the VPN's local-network sharing on.
- Mesh addresses are `fd00::/8`. Mullvad's firewall allows that range only
  with local-network sharing on. Not yet tested with a tunnel up, and the
  thing to test next: the join never got past DNS before.
- WireGuard inside WireGuard: a smaller MTU under the mesh. Not yet measured.

## What would change our mind

A VPN whose policy cannot be honoured from resolv.conf, such as one that
accepts DNS only over TLS. Then `name_servers` is not enough, and the library
would need a resolver of its own.
