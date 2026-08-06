# 005. Overlay addresses derived from keys, no IPAM

**Status:** accepted

## Context

Every mesh VPN must assign each node an overlay address. Tailscale and ZeroTier
allocate from a coordinator; Nebula bakes it into the certificate at signing
time; innernet uses a CIDR tree on a server. All of these need something
authoritative.

## Decision

Derive it:

```
prefix = fd || SHA256("mesh/v1/ula" || NK)[0:5]          → a /48
addr   = prefix : SHA256("mesh/v1/addr" || device_pub)[0:10]   → /128
```

## Why

**It deletes an entire subsystem.** No allocation messages, no conflict
resolution, no leases, no split-brain when two nodes join concurrently. Every
node computes every other node's address locally from a public key it already
has, which also makes WireGuard's `AllowedIPs` self-enforcing with zero shared
state.

**80 bits is ample because the address is not an authenticator.** cjdns and
Yggdrasil need 113–120 committed bits because their addresses *are* the security
boundary. Here, authentication comes from WireGuard's handshake and (later) the
admin-signed credential; the address is just a stable, collision-free name.
Collision probability across 10 devices is ~4×10⁻²³.

**No vanity mining.** cjdns brute-forces keys for its `0xfc` prefix and RFC 3972
CGA does hash extension only because someone else fixes their prefix. We derive
our own from the network key, so the full host part is available directly.

`fd00::/8` per RFC 4193 — deliberately not `fc00::/8` (cjdns squats it and it
collides with real ULA space) nor `0200::/7` (Yggdrasil).

## Consequences

- Addresses change if the network key rotates. Acceptable: rotation is already
  a re-enrolment event.
- No human-friendly addressing; names come from the announce and are advisory.
- A device's address is computable by anyone holding its public key. Not a leak
  — the address is not secret — but worth knowing.
