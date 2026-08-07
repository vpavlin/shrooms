# logos-vpn

An overlay mesh VPN between your own machines, using
[Logos Messaging](https://github.com/logos-messaging) (Waku) for rendezvous
instead of a coordination server.

WireGuard carries the traffic. Waku is only used to find each other — cold
start, roaming, and repair. Once tunnels exist they sustain themselves, so
there is nothing to keep running and nothing to pay for.

**Status: working prototype.** Two nodes discover each other over the public
logos.dev fleet and bring up a tunnel. See [Status](#status) for what is proven
and what is not.

---

## How it works

```
   ┌──────────┐        ┌──────────┐        ┌──────────┐
   │   home   │        │  office  │        │  laptop  │
   └────┬─────┘        └────┬─────┘        └────┬─────┘
        │                   │                   │
        │  ═════ WireGuard, direct where NAT allows ═════
        │                   │                   │
        └─────────┬─────────┴─────────┬─────────┘
                  │                   │
            ┌─────┴─────┐      ┌──────┴───────┐
            │    VPS    │      │  logos.dev   │
            │   relay   │◀────▶│  rendezvous  │
            └───────────┘      └──────────────┘
```

Three planes, deliberately separate:

- **Data** — userspace WireGuard, direct peer-to-peer where NAT allows.
- **Rendezvous** — Waku pub/sub, used intermittently: cold start, network
  change, partition repair.
- **Steady state** — WireGuard relearns a peer's endpoint from any
  authenticated packet, so a node that roams and sends first needs no signalling
  at all.

Two properties fall out of that:

**No IPAM.** Your overlay address is derived from your device key inside a
prefix derived from the network key, so every node computes every other node's
address locally. No allocation, no conflicts, no split-brain.

**No coordination server.** Every node holds the full roster, assembled from the
announce stream. `logos-vpn status` is a real control plane with nothing
authoritative behind it.

---

## Quick start

Needs a built `liblogosdelivery` — see [Building](#building).

```console
# first machine
$ logos-vpn init --name home
Network key: K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5
  copy this to your other machines — it is the only secret
Overlay IP:  fd48:d107:3fce:2b84:226b:ac:f2c3:9f30
Mesh prefix: fd48:d107:3fce::/48

$ sudo systemctl enable --now logos-vpn

# every other machine
$ logos-vpn join K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5 --name office
$ sudo systemctl enable --now logos-vpn
```

That is the whole configuration: **one secret**. Device identity is generated
locally on first start and never leaves the machine.

```console
$ logos-vpn status
network  fd48:d107:3fce::/48          peers 2 (2 up)
self     home  fd48:d107:3fce:2b84:226b:ac:f2c3:9f30

NAME    OVERLAY IP                              ANNOUNCE  TUNNEL  ENDPOINT           RX/TX
office  fd48:d107:3fce:a332:855c:1059:8060:59d7 online    up      203.0.113.7:51820  1.2G/430M
laptop  fd48:d107:3fce:7e8:aea9:ff0f:a2a8:2de9  online    up      198.51.100.4:51820 22M/18M
```

`logos-vpn paths` shows which candidate endpoints answered a probe, which one
won, and whether your NAT is punchable.

---

## Building

`liblogosdelivery` has no canonical distribution, so pick a source:

```console
$ make deps-basecamp HDR=/path/to/liblogosdelivery.h   # reuse a Logos Basecamp install
$ make deps                                            # build from source in a container
$ make logos-vpn
```

⚠️ **`make deps` currently fails** — upstream logos-delivery does not compile at
either master or the revision the Android bindings pin:

```
rest_api/endpoint/relay/handlers.nim: Error: waitFor withTimeout(...)
has an illegal effect: NestedPoll
```

Use `make deps-basecamp` until that is fixed upstream. Notably the *toolchain*
problem is solved: building in Debian bookworm (git 2.39) resolves dependencies
cleanly, where a current host (git 2.51) fails on a nimble lockfile checksum.

---

## Do I need a VPS?

**Probably not.** Any mesh node with a reachable address can relay for the
others — set `relay = "true"` in its config. If you have an office box with a
port forward, a home server on a static IP, or anything with working UPnP, that
node covers every pair that cannot connect directly.

A VPS is only needed when *nothing you own* is reachable. See
[ADR-012](docs/adr/012-relay-hosting.md), which also covers why a public
volunteer relay network is architecturally easy — the relay cannot read what it
forwards — but blocked on incentives rather than code.

---

## Deploying

```console
$ ./scripts/deploy.sh user@vps --init --advertise 203.0.113.4:51820
$ ./scripts/deploy.sh user@other-vps --key <NETWORK-KEY>
```

Ships a **container image**, not a binary: `liblogosdelivery` needs glibc 2.38,
so a tarball fails on Debian 12 (2.36) and works only on Ubuntu 24.04+. The
image runs anywhere docker does.

It uses host networking on purpose. Behind docker's bridge the reflexive address
peers observe would be the docker gateway's and the source port would be
rewritten, so hole punching would be fighting a layer of NAT that does not exist
in reality.

---

## Status

| | |
|---|---|
| **S1** cgo binding to liblogosdelivery | ✅ publish→receive over the real fleet |
| **S3** rotating topics stay on one shard | ✅ 6 epochs, all to `/waku/2/rs/2/3` |
| **M0** WireGuard sharing a socket with control traffic | ✅ tunnel + control packets, no root |
| **M1** Waku-discovered peers replace static config | ✅ two containers, discovery + tunnel + ping |
| **M2** NAT traversal | ⚠️ reflexive discovery proven; the punch is not |
| **M3** relay fallback | ✅ two NATed nodes carry traffic through a relay |
| **M5** credentials, enrolment, revocation | not started |

`make m0` / `make m1` / `make s1` / `make s3` reproduce these.

**M2's caveat is worth reading before you rely on it.** Reflexive discovery
demonstrably works — a NATed node learns its own public address from a peer's
pong with no STUN server. What is unproven is two NATed nodes punching through
to each other. The container harness turned out to be the obstacle: plain Linux
`MASQUERADE` is *not* endpoint-independent, so it tests the hard case while
claiming to test the easy one. See PROTOTYPE.md §M2.

---

## Security

Read [SECURITY.md](SECURITY.md) before running this anywhere that matters. The
short version:

- Control messages and discovery probes are both **encrypted**, fixed-size and
  fixed-rate, on a rotating topic, and not archived.
- The network key is a **bearer credential**: anyone holding it is a member,
  permanently. There is no per-device revocation and no expiry.
  `logos-vpn key rotate` re-creates the mesh and forces everyone to re-join —
  blunt, and the only option today. **Treat the key as the whole security of
  the mesh.**
- There is a [concrete plan to fix this](SECURITY.md#roadmap), in four phases.
  The first — one-time invite tokens, so the copy/pasted secret stops being
  permanent — is small, has no dependencies, and should land before this runs
  anywhere exposed.
- Some things leak inherently and cannot be fixed here: Waku leaves the content
  topic and timestamp in cleartext, relay peers see your IP, and WireGuard is
  identifiable as WireGuard.

---

## Documentation

| | |
|---|---|
| [DESIGN.md](DESIGN.md) | architecture and the research behind each decision |
| [PROTOTYPE.md](PROTOTYPE.md) | build plan, milestones, what each proved |
| [SECURITY.md](SECURITY.md) | what is protected, what leaks, what is deferred |
| [docs/adr/](docs/adr/) | why each significant decision was made |

---

## Scope

Linux first. Android is designed for but deliberately deferred — see DESIGN §6,
which covers the constraints that decide it (Doze tears down Waku connections on
every cycle, so intermittency is forced rather than chosen). **iOS is out of
scope**: its NetworkExtension memory cap makes an embedded libp2p node
impractical.
