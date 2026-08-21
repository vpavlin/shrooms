# Hiding that this is WireGuard

**Status:** design note, nothing built. One decision at the end.

SECURITY.md has always conceded this and pushed it away:

> **WireGuard traffic is identifiable as WireGuard.** Handshake packets have
> distinctive sizes (148/92/32). An ISP can tell you run a VPN, though not what
> is inside. Obfuscation is a different project; do not conflate it with this.

That is still the right instinct about scope. But "a different project" was
written before the data plane grew a custom `Bind`, and the architecture is now
much better placed for this than the note implies — while being, right now,
*worse* on the wire than the note admits.

## First, the bad news: we are more identifiable than plain WireGuard

Every control packet we send starts with four literal ASCII bytes:

```go
var Magic = [4]byte{0x6d, 0x76, 0x70, 0x6e} // "mvpn"
```

Disco probes and relay frames both ride that prefix, in the clear, on the same
port as the tunnel.

So a DPI box does not merely see "this is WireGuard". It sees WireGuard **plus a
unique string** identifying the software. Plain WireGuard tells an observer you
run *a* VPN; this tells them which one, and gives them a signature far cheaper
to match than WireGuard's own.

Obfuscating the tunnel while leaving `mvpn` in cleartext would be pointless. Any
work here starts with that prefix, and doing only that is a real improvement for
very little effort.

## Why WireGuard itself is easy to spot

Three fields, no cryptography required:

- byte 0 is the message type, `0x01`–`0x04`
- bytes 1–3 are reserved and always zero
- handshakes are fixed sizes: 148 bytes initiation, 92 response, 32 keepalive

Our own `Bind` documents this, because it is exactly how we demultiplex:

```
WireGuard : msg[0] in 0x01..0x04 and msg[1:4] == 0x000000
```

A censor writes the same rule.

## Why we are well placed to change it

Shrooms runs **userspace wireguard-go with a custom `Bind`** — not kernel
WireGuard. That was forced by NAT traversal: the tunnel and the hole punching
must share one socket, and the kernel module will not share. It is why Tailscale
runs userspace everywhere too.

The consequence is that every packet, both directions, already passes through
code in this repository. `Bind.Send` handles outbound and the wrapped
`ReceiveFuncs` handle inbound; the relay path already rewrites packets on the
way out. An obfuscation layer is one transform on each of those paths rather
than a fork of WireGuard.

Most projects doing this have to fork the protocol implementation. We would not.

## What "hiding it" can actually mean

Three levels, increasing in cost and in what they buy.

**1. Stop announcing ourselves.** Derive the control prefix from the network key
instead of shipping `mvpn`, so it differs per mesh and carries no project
identity. Cheap, and removes a signature that is currently more useful to an
observer than WireGuard's own.

**2. Break the WireGuard signature.** The AmneziaWG approach, which is the
reference implementation of this idea: randomise the reserved bytes, pad
handshakes away from their fixed sizes, and optionally send junk packets before
a handshake. A transform keyed on the network key would do it — the receiving
end reverses it before wireguard-go ever sees the packet.

This defeats signature matching. It does **not** defeat traffic analysis: packet
sizes, timing and volume still say "an encrypted tunnel" to anyone looking
properly, and a flow of 1400-byte UDP packets to one host is not innocent.

**3. Look like something else.** Wrap the whole thing in TLS or QUIC on 443 so
it resembles ordinary web traffic. This is what gets through hostile networks
that default-deny UDP. It also costs the most: an extra encryption layer, a
smaller MTU, worse latency, and TCP-over-TCP problems if the carrier is TLS.

## What it would cost us

- **A flag day.** Both ends must agree; a transformed packet is noise to an old
  node. Worth bundling with any other wire change rather than paying twice.
- **CPU per packet, and possibly throughput.** `Bind` deliberately preserves
  `StdNetBind`'s batching and GSO/GRO offload — the package comment calls that
  out as what makes the data plane fast, and why go-libp2p's shared-conn
  approach was rejected. A per-packet transform has to keep working inside that
  batching, and it should be measured rather than assumed.
- **An arms race we cannot win alone.** DPI vendors adapt; obfuscation that is
  widely deployed gets fingerprinted in turn. AmneziaWG works partly because
  many people use it and blocking it has collateral damage. A private mesh has
  no such cover.
- **It is obfuscation, not security.** It adds no cryptographic protection. The
  tunnel is exactly as safe, and exactly as unsafe, as before.

## An honest comparison

Products in this space split the difference in a way worth naming. Mixnet-based
designs get real metadata protection and pay latency for it, which is why the
ones that ship a VPN also ship a fast non-mixed mode — and most people use the
fast mode. The obfuscation-only designs keep VPN latency and buy defeat of
signature matching, not anonymity.

Shrooms is in the second category if it does anything here. It should not claim
to be in the first (see [ADR-011](adr/011-no-mixnet.md), and
[blind-relays.md](blind-relays.md) for why a mixnet cannot help the relay path).

## The decision

**Is the threat "my ISP can see I use a VPN", or "this network blocks VPNs"?**

- *The first* — level 1 is worth doing on its own merits and is nearly free.
  Level 2 adds little, because the observer already knows from timing and
  volume.
- *The second* — level 3 is the only one that helps, and it is a genuinely
  different piece of engineering with a permanent performance cost. It also
  wants to be optional per mesh, since most meshes do not need it.

Level 1 is a small, self-contained improvement regardless of the answer:
shipping a literal `mvpn` on the wire is a fingerprint nobody chose, and the
cheapest fix in this document.

**Blind relays sharpen this rather than complicating it.** A relay somebody else
runs holds no network key, so it cannot derive a per-mesh prefix — but it does
hold its own token, and so does every client using it. Blind-relay traffic
therefore takes a prefix derived from the *relay's* key rather than the mesh's:
different per relay instead of per mesh, still carrying no project identity, and
computable by exactly the two parties who need it.

The framing now lives in `internal/ctrl`, which is where that derivation belongs
when it is built. It is a flag day for whichever plane adopts it, since a prefix
only works if both ends agree — so it wants doing once, deliberately, rather
than drifting in.
