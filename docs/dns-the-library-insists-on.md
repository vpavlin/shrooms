# The resolver the library insists on

**Status:** proposed, not built. Found 2026-09-08, debugging a join that timed
out on a laptop whose network was fine. The machine ran Mullvad.

A device could not join a mesh. Its daemon repeated two lines, a few seconds
apart, forever:

```
ERR connectToRelayPeers: won't attempt new connections - node is offline  topics="waku node peer_manager"
INF Failed to query DNS  topics="libp2p dnsresolver" address=one.one.one.one error="(1) Operation not permitted"
```

Everything about that machine looked healthy. `host one.one.one.one` answered,
`getent hosts github.com` answered, the same image with the same host networking
resolved names fine, IPv6 was up, and the other laptop on the same wifi was
connected to the fleet with ten relay peers.

## What is happening

The delivery library does not use the system resolver. From its own
`AvailableConfigs`, via `./bin/wakuspike -probe`:

```
"dnsAddrsNameServers": seq[IpAddress](@[1.1.1.1, 1.0.0.1])
 desc: "DNS name server IPs to query for DNS multiaddrs resolution."
```

It speaks UDP/53 straight to Cloudflare, and `/etc/resolv.conf` never enters
into it. Two things depend on that:

- **The fleet's entry nodes**, which are `/dns4/` multiaddrs —
  `delivery-01.do-ams3.logos.dev.status.im` and five more. Unresolvable means
  undialable.
- **The library's own connectivity probe**, which resolves `one.one.one.one`.
  When it fails the node decides it is offline, and `connectToRelayPeers` then
  refuses to dial *anything*. The fleet is never tried, so "offline" is a
  verdict about a DNS query rather than about the network.

On that laptop, Mullvad's DNS-leak protection was rejecting traffic to any
resolver but its own. `dig +short @1.1.1.1 one.one.one.one` got `connection
refused` while ordinary resolution worked, because the two go through different
paths and only the direct one matters here.

Disconnecting the tunnel cleared it. `dig +short @1.1.1.1` answered again, the
daemon reached the fleet, and the invite went through.

If disconnecting ever turns out not to be enough, the place to look is
Mullvad's "block when disconnected" setting, which keeps its firewall rules
enforced while `mullvad-daemon` runs. That was not this case.

## Why this is ours to fix

"Turn the VPN off" is not a fix. It means shrooms does not run on that machine
while its owner is using a VPN, and a VPN is not an exotic setup. The same
shape covers corporate networks that permit only their own resolver, hosts
running a local DNS proxy on `1.1.1.1`, and anywhere DoT/DoH is mandatory.

`entry_nodes` does **not** substitute for it. Explicit IP multiaddrs would
remove the need to resolve `/dns4/` names, but the offline verdict comes from
the probe, and the peer manager will not dial while that verdict stands.

The symptom is also as unhelpful as it could be. Nothing in "node is offline"
points at DNS, nothing points at the VPN, and the first instinct — check the
network — confirms the network is fine.

## The proposal

Expose the option the library already has.

```toml
name_servers = ["10.64.0.1"]     # Mullvad's own; or the router, 192.168.0.1
```

- Device level, beside `preset` and `mode`, not per mesh. It describes how this
  machine reaches the world, and the machine has one answer.
- **Unset by default.** The library keeps its own default, so nothing changes
  for anybody who is not in this hole.
- Mapped to `dnsAddrsNameServers` where the node is constructed. That is three
  places, and missing one leaves a path that still fails: `nodeConfig` in
  daemon.go, the CLI's short-lived node for `invite` and a direct `join`
  (invite.go), and `waitingFleet`, which today copies only preset, mode,
  cluster and entry nodes.
- IP addresses, not names. The library's type is `seq[IpAddress]`, and a name
  here would need resolving by the thing that cannot resolve.

## Open questions

- **What JSON the FFI layer accepts** for a `seq[IpAddress]`. A list of
  strings is the guess; `wakuspike -config '{"dnsAddrsNameServers":["192.168.0.1"]}'`
  settles it before any of the above is written. The configuration layer
  rejects unrecognised keys outright, which makes this cheap to check and
  cheap to get wrong.
- **Whether the online probe honours it.** It resolves `one.one.one.one`
  through the same `libp2p dnsresolver`, so it should — but the whole failure
  turns on that probe, and a fix that repairs bootstrap while leaving the node
  convinced it is offline fixes nothing.
- **Whether to fall back to `/etc/resolv.conf` when the key is unset.** It
  would fix this class of failure for everybody without anybody configuring
  anything, and it is what a reader assumes already happens. It also changes
  behaviour for every existing node, including ones whose system resolver is
  worse than Cloudflare, and it silently couples the mesh to whatever DHCP
  handed the machine. Worth deciding deliberately rather than sliding into.

## Testing it

The two findings compose. A machine with Mullvad running is a ready-made test
rig for a node that cannot reach `1.1.1.1`, and `sudo systemctl stop
mullvad-daemon` is the control. Set `name_servers = ["10.64.0.1"]`, leave the
VPN connected, and the node should reach the fleet — which is the version of
this that matters, since the point is to work *with* the VPN rather than after
turning it off.

Until then, the symptom is in the troubleshooting table on the install page,
which is the only thing standing between a Mullvad user and a very long
afternoon.
