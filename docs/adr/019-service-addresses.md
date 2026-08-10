# 019. An address per service

**Status:** proposed — the general form of what the name router does for HTTP

## Context

A device publishes services (`services = ["immich:2283"]`) and they are reached
as `<service>.<device>.mesh`. Every service on a device resolves to that
device's single overlay address, because that is the only address it has.

That makes the port part of the name unavoidable in the general case. Two
services cannot both be port 80 on one address, and *which service a connection
wants* has to be carried somewhere.

The shipped answer carries it in the protocol: the daemon listens on 80 and 443
and routes on the HTTP `Host` header or the TLS SNI. That covers a browser,
which is where the port hurts most, and it covers nothing else. `ssh`,
syncthing, Postgres and every other protocol name nothing on the wire and still
need `<service>.<device>.mesh:<port>`.

The clean answer is for each service to have its own address. Then a port is
only ever ambiguous within one service, which is the ordinary situation
everywhere else.

## Decision (proposed)

**Reserve the low bits of the host part for services.**

Today (ADR-008):

```
prefix   fd || SHA256("mesh/v1/ula" || NK)[0:5]        48 bits
host     SHA256("mesh/v1/addr" || device_pub)[0:10]    80 bits
```

Proposed:

```
prefix   fd || SHA256("mesh/v1/ula" || NK)[0:5]        48 bits   unchanged
device   SHA256("mesh/v1/addr" || device_pub)[0:8]     64 bits
service  SHA256("mesh/v1/svc" || device_pub || name)[0:2]  16 bits, zero for the device itself
```

Three properties follow, and they are the whole argument:

**Any peer can compute a service address without being told.** It needs the
device's public key, which the roster already carries, and the service name,
which is in the name the user typed. Nothing has to be announced — which
matters, because the announce is already within ~12 bytes of its 512-byte
padding (ADR-014) and a service list would not fit.

**Routing stays per-device.** A peer's WireGuard `AllowedIPs` becomes
`<prefix>:<device>::/112` instead of a `/128`. One entry, covering that device
and every service it will ever publish, computed from its key alone. No dynamic
reconfiguration, and nothing to keep in sync.

**The port stops being part of the name.** `immich.home-server.mesh` resolves to
an address only immich answers on, so the daemon can forward port 80 there — and
port 22, and 5432 — with no ambiguity and no protocol sniffing. The name router
becomes unnecessary rather than merely limited.

### What it costs

**Every overlay address changes.** The device host part goes from 80 bits to 64,
so every device gets a new address. Not a rotation of the network key — names,
keys and membership are untouched — but every node must upgrade before any node
does, or two nodes compute different addresses for the same device and no
tunnel forms. For a personal mesh of a handful of machines that is an afternoon;
it is still a flag day, and it gets worse with every device added.

**64 bits of device hash instead of 80.** Collision needs ~2^32 devices by the
birthday bound. Irrelevant at personal-mesh scale, and worth writing down
because it is the kind of margin that is quietly spent and never revisited.

**16 bits of service hash.** Collision between two services *on one device* at
around 256 services, which is far past what the config is for — and it is
detectable locally, at config load, by the one machine that knows both names.
So it can be a refusal rather than a mystery.

## Alternatives

**Keep the name router and stop here.** What is built. Covers browsers, which is
most of the demand, at zero migration cost. The case against is that it is
protocol sniffing: it works because HTTP and TLS happen to announce the name,
and every protocol that does not is left needing a port forever.

**Announce service addresses instead of deriving them.** Removes the layout
change entirely — each device says what it publishes and at which address. It
does not fit: the announce is nearly at its padded size, and growing that size
changes the fixed-length property the padding exists for. It also makes adding a
service a distributed operation, where deriving keeps it local to one config
file.

**Add addresses to `AllowedIPs` when a name is resolved.** No layout change and
no announce: the resolver computes the service address, adds a `/128` for it,
and answers. Rejected because the route then depends on a DNS query having
happened on that device — anything using a cache, a hosts file, or a bare
address fails, and the failure is intermittent, which is the worst kind.

**A SOCKS or HTTP proxy on each device.** Solves it for clients configured to
use a proxy and not for anything else. A VPN whose services need proxy settings
has given up the property that makes it a VPN.

## Consequences

- The name router (`internal/service/router.go`) becomes redundant and should be
  deleted rather than kept as a second path to the same thing.
- `<service>.<device>.mesh` works for every protocol, with the service's own
  port, and `ssh immich.home-server.mesh` means what it looks like.
- A service name that does not exist gets an address that nothing answers on,
  so it fails as a refused connection rather than as a 404 from the router.
  Slightly worse diagnostics for a slightly more honest model.
- Best done before the mesh grows, and before ADR-018 credentials, since both
  touch enrolment and one flag day is cheaper than two.
