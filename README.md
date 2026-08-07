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

## Prerequisites

### On the machine you build from

| | |
|---|---|
| Go 1.23+ | with cgo enabled and a C toolchain (`build-essential`) |
| Docker | only if you deploy to a remote host |
| `liblogosdelivery` | see below — there is no canonical distribution |
| `/dev/net/tun` | to run a node locally |
| `CAP_NET_ADMIN` | i.e. `sudo`, to create the TUN device |

### On any node

| | |
|---|---|
| Linux | x86-64. Android is designed for but not yet built; iOS is out of scope |
| glibc ≥ 2.38 | Ubuntu 24.04+ works, **Debian 12 does not** (2.36). Irrelevant if you deploy the container |
| `/dev/net/tun` | **verify this on a cheap VPS** — OpenVZ/LXC hosts often disable it. Insist on KVM |
| 1 UDP port | default 51820, inbound only needed on nodes you want reachable |

**Resource use is negligible.** A measured Core-mode node acting as a relay sits
at **~20 MiB RSS and 1–2% of one core**. Any VPS tier will do; what matters is
bandwidth allowance and latency, since relayed traffic takes a detour through
the node.

### Getting liblogosdelivery

```console
$ make deps-release      # download a prebuilt copy — works anywhere
```

That is the one you want. The other two exist for completeness:

```console
$ make deps-basecamp HDR=/path/to/liblogosdelivery.h   # reuse a local Logos Basecamp install
$ make deps                                            # build from source in a container (broken, see below)
```

⚠️ **`make deps` currently fails** — upstream logos-delivery does not compile at
either master or the revision the Android bindings pin:

```
rest_api/endpoint/relay/handlers.nim: Error: waitFor withTimeout(...)
has an illegal effect: NestedPoll
```

So the library exists only where Logos Basecamp is installed — which would
make a fresh machine, CI, or a VPS unable to build at all. `make deps-release`
works around that by downloading a repackaged copy from
[this repo's releases](https://github.com/vpavlin/logos-vpn/releases/tag/deps-v1)
(Apache-2.0 OR MIT, unmodified).

Note the *toolchain* problem is solved: building in Debian bookworm (git 2.39)
resolves dependencies cleanly, where a current host (git 2.51) fails on a nimble
lockfile checksum. Only upstream's own compile error remains.

---

## Setting up a mesh, from scratch

This is the full sequence for a VPS plus your laptop. The VPS creates the mesh
and relays; the laptop joins.

### 1. Build

```console
$ git clone https://github.com/vpavlin/logos-vpn
$ cd logos-vpn

$ make deps-basecamp \
    HDR=$(find ~ -name liblogosdelivery.h 2>/dev/null | head -1)

$ make logos-vpn
$ make test-unit          # optional, ~5s
```

### 2. Deploy the VPS as the first node

Replace `VPS_IP` with its public address. `--init` creates the mesh, `--relay`
makes it forward for peers that cannot reach each other, and `--advertise`
tells peers where to find it.

```console
$ ./scripts/deploy.sh root@VPS_IP \
    --init --relay --name vps --advertise VPS_IP:51820
```

This checks the host, builds and ships a container image, writes
`/etc/logos-vpn/config.toml`, and starts it. **Copy the network key it prints** —
it is the only secret, and until the security roadmap's phase 1 lands it is a
permanent bearer credential.

Open the port:

```console
$ ssh root@VPS_IP 'ufw allow 51820/udp'    # or your firewall's equivalent
```

### 3. Join from your laptop

```console
$ sudo ./bin/logos-vpn join <NETWORK-KEY> --name laptop
$ sudo ./bin/logos-vpn daemon -v
```

Leave that running. On first start it generates a device identity, derives its
overlay address, connects to the logos.dev fleet, and announces itself.

### 4. Check it works

```console
$ sudo ./bin/logos-vpn status
network  fd48:d107:3fce::/48          peers 1 (1 up)
self     laptop  fd48:d107:3fce:2b84:226b:ac:f2c3:9f30

NAME  OVERLAY IP                              ANNOUNCE  TUNNEL  ENDPOINT            RX/TX
vps   fd48:d107:3fce:a332:855c:1059:8060:59d7 online    up      203.0.113.4:51820   1.2K/900

$ ping fd48:d107:3fce:a332:855c:1059:8060:59d7
```

Expect discovery in 15–25 s from cold (most of it the Waku node joining the
fleet) and a handshake shortly after.

### 5. What to look at

```console
$ sudo ./bin/logos-vpn paths
reflexive addresses (as peers observe us):
  203.0.113.9:41001
```

**This is the most informative single output.** One address means
endpoint-independent NAT, so direct connections between NATed peers should work.
Several means endpoint-dependent (symmetric) NAT, punching will not work, and
traffic falls back to the relay — which is exactly why the relay exists.

`status` separates **ANNOUNCE** (seen on the gossip bus) from **TUNNEL**
(WireGuard handshake completed). If a peer is online but has no handshake, the
problem is traversal, not discovery. A relayed peer shows a `relay:…` endpoint.

### 6. Add more machines

```console
$ sudo ./bin/logos-vpn join <NETWORK-KEY> --name office     # locally
$ ./scripts/deploy.sh user@host --key <NETWORK-KEY> --name nas   # remotely
```

Any node with a reachable address can also relay — add `relay = "true"` to its
config. You do not need a VPS if one of your own machines is reachable; see
[ADR-012](docs/adr/012-relay-hosting.md).

### Troubleshooting

| Symptom | Likely cause |
|---|---|
| `tun: ... (need CAP_NET_ADMIN)` | run with `sudo`, or the host has no `/dev/net/tun` |
| peer `online` but `no handshake` | traversal, not discovery — check `paths` and whether the port is open |
| no peers at all after 60 s | the daemon is not reaching logos.dev; check outbound connectivity |
| `missing liblogosdelivery.h` | run `make deps-basecamp` |
| the daemon exits immediately | `libpq` missing — deploy the container rather than a bare binary |

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

`scripts/deploy.sh` ships a **container image**, not a binary: `liblogosdelivery`
needs glibc 2.38, so a tarball fails on Debian 12 (2.36) and works only on
Ubuntu 24.04+. The image runs anywhere docker does.

It uses host networking on purpose. Behind docker's bridge the reflexive address
peers observe would be the docker gateway's and the source port would be
rewritten, so hole punching would fight a layer of NAT that does not exist in
reality.

```console
$ ./scripts/deploy.sh user@host [--init] [--relay] [--advertise IP:PORT] \
      [--name NAME] [--key KEY] [--force]
```

It refuses to overwrite an existing config without `--force`, and the remote
generates its own device identity, so no private key ever crosses the wire.

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
