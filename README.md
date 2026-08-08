# logos-vpn

An overlay mesh VPN between your own machines, using
[Logos Messaging](https://github.com/logos-messaging) for rendezvous
instead of a coordination server.

WireGuard carries the traffic. Logos Messaging is only used to find each other — cold
start, roaming, and repair. Once tunnels exist they sustain themselves, so
there is nothing to keep running and nothing to pay for.

**Status: working over the real internet.** A laptop behind NAT and a VPS
discovered each other over the public logos.test fleet and brought up a direct
WireGuard tunnel — no coordination server, one shared key:

```
$ ssh root@vps.mesh          # names come from the mesh, not DNS
$ ping fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf
4 packets transmitted, 4 received, 0% packet loss
rtt min/avg/max/mdev = 31.883/63.188/94.992/25.589 ms
```

See [Status](#status) for what is proven and what is not.

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
            │    VPS    │      │  logos.test  │
            │   relay   │◀────▶│  rendezvous  │
            └───────────┘      └──────────────┘
```

Three planes, deliberately separate:

- **Data** — userspace WireGuard, direct peer-to-peer where NAT allows.
- **Rendezvous** — Logos Messaging pub/sub, used intermittently: cold start, network
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

### 2. Bring up the first node

Two ways. **If the machine can build** — Ubuntu 24.04+, Fedora, anything with
glibc ≥ 2.38 — the simplest path is to build there directly, with no docker and
nothing to ship:

```console
$ git clone https://github.com/vpavlin/logos-vpn && cd logos-vpn
$ make deps-release && make logos-vpn
$ sudo make install                       # binary, libraries, systemd unit
$ sudo logos-vpn init --relay --name vps
$ sudo systemctl enable --now logos-vpn
```

`make install` relinks the binary with an rpath pointing at
`/usr/local/lib/logos-vpn`, so it keeps working after you delete the checkout.
To run it in the foreground instead, skip the install and use
`sudo ./bin/logos-vpn daemon -v`.

**Otherwise, push from your machine** with `deploy.sh`, which builds a container
image and ships it over ssh:

### 2b. Deploy remotely

`--init` creates the mesh and `--relay` makes it forward for peers that cannot
reach each other directly.

```console
$ ./scripts/deploy.sh root@VPS_IP --init --relay --name vps
```

**You do not normally need `--advertise`.** A VPS has its public IP on a local
interface, so the node enumerates and announces it by itself. Pass it only when
your public address is *not* on any local interface — a home server behind a
port-forwarding router, or a cloud instance that sees `10.x` with the public IP
NAT'd in front:

```console
$ ./scripts/deploy.sh root@HOST --init --relay --name home \
    --advertise 203.0.113.4:51820
```

`init` tells you which case you are in: if the machine has no globally routable
address it says so, and otherwise stays quiet.

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

Leave that running (or `sudo make install` and use systemd here too). On first start it generates a device identity, derives its
overlay address, connects to the fleet, and announces itself.

### 4. Check it works

```console
$ sudo ./bin/logos-vpn status
network  fd48:d107:3fce::/48          peers 1 (1 up)
self     laptop  fd48:d107:3fce:2b84:226b:ac:f2c3:9f30

NAME  OVERLAY IP                              ANNOUNCE  TUNNEL  ENDPOINT            RX/TX
vps   fd48:d107:3fce:a332:855c:1059:8060:59d7 online    up      203.0.113.4:51820   1.2K/900

$ ping fd48:d107:3fce:a332:855c:1059:8060:59d7
```

Expect discovery in 15–25 s from cold (most of it the messaging node joining the
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

### 6. Use names instead of addresses

```console
$ sudo logos-vpn hosts            # preview
$ sudo logos-vpn hosts --write    # update /etc/hosts

$ ssh root@vps.mesh
```

Entries go in a marked block, written atomically, and re-running is safe.

To keep it current without re-running anything, let the daemon do it:

```toml
manage_hosts = "true"      # in /etc/logos-vpn/config.toml
```

It rewrites the block whenever the roster changes, and leaves the file alone
when nothing has. Off by default, since a VPN editing a system file that
cloud-init and NetworkManager also touch should be a deliberate choice.

Still a hosts file, so it needs root and does not exist on Android — the DNS
server that replaces it is [ADR-013](docs/adr/013-name-resolution.md).

### 7. Add more machines

```console
$ sudo ./bin/logos-vpn join <NETWORK-KEY> --name office     # locally
$ ./scripts/deploy.sh user@host --key <NETWORK-KEY> --name nas   # remotely
```

Any node with a reachable address can also relay — add `relay = "true"` to its
config and restart it. Nothing else needs configuring: the relay advertises
itself in its ordinary announce and every other node picks it up, so there are
no relay addresses to distribute or keep up to date. You do not need a VPS if
one of your own machines is reachable; see
[ADR-012](docs/adr/012-relay-hosting.md).

### Troubleshooting

| Symptom | Likely cause |
|---|---|
| `tun: ... (need CAP_NET_ADMIN)` | run with `sudo`, or the host has no `/dev/net/tun` |
| peer `online` but `no handshake` | traversal, not discovery — check `paths` and whether the port is open |
| `!! rendezvous:` warning in `status` | the fleet is unreachable. Discovery is stalled; established tunnels keep working. Confirm with `make s1` |
| `!! rendezvous: peers connect and are dropped immediately` | preset or `cluster_id` mismatch — you are on a different cluster than the fleet. Confirm with `different clusterId reported: N vs M` in the daemon log |
| peer shows `stale 12m` | the tunnel is dead — the peer has not rekeyed within WireGuard's 180 s session lifetime |
| no peers at all after 60 s | the daemon is not reaching the fleet; check outbound connectivity |
| `missing liblogosdelivery.h` | run `make deps-basecamp` |
| the daemon exits immediately | `libpq` missing — deploy the container rather than a bare binary |

---

## Do I need a VPS?

**Probably not.** Any mesh node with a reachable address can relay for the
others — set `relay = "true"` in its config; the rest of the mesh finds it on
its own, and its IP can change freely. If you have an office box with a
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

## Android

A phone as a full mesh participant, not a client of something else. The app is a
Compose UI and a `VpnService` around the same Go core — nothing in `internal/`
is reimplemented. See [ANDROID.md](ANDROID.md) for the plan and
[ADR-016](docs/adr/016-android-reuses-the-go-core.md) for why.

Prototype in progress.

## Status

| | |
|---|---|
| **S1** cgo binding to liblogosdelivery | ✅ publish→receive over the real fleet |
| **S3** rotating topics stay on one shard | ✅ 6 epochs, all to `/waku/2/rs/2/3` |
| **M0** WireGuard sharing a socket with control traffic | ✅ tunnel + control packets, no root |
| **M1** discovered peers replace static config | ✅ **verified over the real internet**: NATed laptop ↔ VPS, direct tunnel, ssh across it |
| **M2** NAT traversal | 🟨 reflexive discovery proven on real NAT; punching between two NATed nodes unproven |
| **M3** relay fallback | 🟨 auto-discovered and working in containers; untested on real infrastructure |
| **M6** name resolution | 🟨 `logos-vpn hosts` **verified for real** (`ssh root@vps.mesh`); DNS server planned |
| **M4** seamless operation · **M5** credentials | ⬜ not started |

`make m0` / `make m1` / `make m3` / `make s1` / `make s3` reproduce these.

### Adding a machine

On the machine itself, with only docker installed:

```console
$ curl -fsSLO https://raw.githubusercontent.com/vpavlin/logos-vpn/master/scripts/install.sh
$ sudo bash install.sh join <NETWORK-KEY> --name laptop
```

Or to create a new mesh, on the first machine:

```console
$ sudo bash install.sh init --relay
```

Everything after `init`/`join` goes straight to `logos-vpn`, so its flags are
whatever that version supports — `--name`, `--relay`, `--advertise`, `--port` —
rather than a copy in the script that drifts out of date. The device name
defaults to this machine's hostname.

That is the whole thing: it pulls the image, generates the config, installs a
systemd unit and **starts the daemon**, which then comes up on boot. There is no
separate step to run it. `init` and `join` are the setup performed inside, not
something you invoke separately.

It also installs a `logos-vpn` wrapper so `logos-vpn status` works on the host. No Go toolchain,
no checkout, no liblogosdelivery — everything is in the image. Re-running is
safe: the device identity is never replaced by accident, because losing it means
a new overlay address and looking like a different device to every peer.

Afterwards:

```console
$ systemctl status logos-vpn      # is it up
$ logos-vpn status                # who is on the mesh
$ journalctl -u logos-vpn -f      # follow the log
```

Read the script before running it as root, as you should with anything fetched
this way.

Use `scripts/deploy.sh` instead when you want to push to a remote host *from* a
machine that has the repo — for example to deploy an unpushed change.

CI publishes a container image to `ghcr.io/vpavlin/logos-vpn` on every push to
`master`, so `deploy.sh` and `m3-remote` pull it rather than building locally
and pushing ~200 MB over ssh. Set `BUILD_IMAGE=1` (or `FORCE_IMAGE=1`) to build
from your working tree instead — which you want whenever you are testing changes
that are not yet pushed. The package must be made public in the repository's
package settings before an unauthenticated host can pull it.

`make m3-remote HOST=user@vps` runs the relay test over the real internet
instead of between containers: it starts a third node behind a NAT on your
relay host, which by construction cannot reach this machine directly, and
measures the tunnel from here. It proves the relay path, **not** hole punching
— see the header of `scripts/m3-remote.sh` for what it does and does not
establish. `--down` removes the test node again.

### Known gaps, honestly

**The relay has not been tested on real infrastructure.** Relays now announce
themselves and are discovered from the roster, so no node needs to be told where
one is — but that has only been exercised in containers. The path that matters,
two NATed devices meeting through a VPS over the real internet, is untested.

**Punching between two NATed peers is unproven.** A NATed node reaching a public
one works for real. Two NATed nodes reaching *each other* has only been
attempted in containers, where the harness turned out to be the obstacle: plain
Linux `MASQUERADE` is not endpoint-independent, so it tests the hard case while
claiming to test the easy one.

**A fleet migration broke everything once, silently.** On 2026-08-07 logos.dev
moved to cluster 3 while the preset compiled into our pinned
liblogosdelivery still said cluster 2. Every peer connected, compared metadata,
disagreed and hung up — which looks exactly like an outage. The default is now
`logos.test`, whose preset is correct, and `cluster_id` exists as an override
for the next time a fleet moves ahead of the library. `make s1` detects this.

**Nothing has been through M4.** Roaming, restarts, a VPS reboot, the fleet
being briefly unreachable — none of it is tested. Treat this as working rather
than dependable.

**Testing on real infrastructure has found two bugs that containers hid**, both
timing- or NAT-dependent. Prefer hardware for anything in those categories.

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
- Some things leak inherently and cannot be fixed here: the messaging layer leaves the content
  topic and timestamp in cleartext, relay peers see your IP, and WireGuard is
  identifiable as WireGuard.

---

## Documentation

| | |
|---|---|
| [DESIGN.md](DESIGN.md) | architecture and the research behind each decision |
| [PROTOTYPE.md](PROTOTYPE.md) | build plan, milestones, what each proved |
| [SECURITY.md](SECURITY.md) | what is protected, what leaks, what is deferred |
| [docs/adr/](docs/adr/) | why each significant decision was made (13 records) |

---

## Scope

Linux first. Android is designed for but deliberately deferred — see DESIGN §6,
which covers the constraints that decide it (Doze tears down messaging connections on
every cycle, so intermittency is forced rather than chosen). **iOS is out of
scope**: its NetworkExtension memory cap makes an embedded libp2p node
impractical.
