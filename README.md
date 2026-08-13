# Shrooms 🍄

<img src="assets/logo.png" alt="Shrooms" width="128" align="right">

**The mycelial mesh VPN.** Nodes find each other through the network's own
gossip and move data directly between one another. No coordination server, no
account, no vendor in the path.

Yes, it is a mushroom pun. Deal with it.

Discovery and signalling run over **Logos Delivery**, a censorship-resistant
pub/sub transport from the Logos stack. **WireGuard** carries the traffic —
the real thing, in userspace, not something WireGuard-shaped. Delivery is used
only to find each other: cold start, roaming, and repair. Once a tunnel exists
it sustains itself, so there is nothing to keep running and nothing to pay for.

Addresses are derived from device keys, so there is no allocation, no
conflicts, and nothing to coordinate. Every node holds the full roster and none
of them is authoritative.

**Status: working over the real internet**, on Linux and Android, daily:

```console
$ ssh root@vps.mesh                      # names come from the mesh, not DNS
$ curl http://ha.jimmy-crib.mesh         # a LAN device that never joined
$ ping fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf
4 packets transmitted, 4 received, 0% packet loss
rtt min/avg/max/mdev = 31.883/63.188/94.992/25.589 ms
```

---

## What Shrooms is not

Every mesh VPN README claims to be decentralised. Here is where the seams
actually are, because a promise you cannot keep is worse than one you never
made.

**It is not relay-free.** Two nodes behind hostile NAT sometimes cannot reach
each other however well you punch — carrier-grade NAT that rewrites the port
per destination makes it impossible, not merely hard. Shrooms relays through
**a node you already run**, chosen automatically from its own announcement
([ADR-012](docs/adr/012-relay-hosting.md)). The difference from a DERP-style
service is not that no relay exists; it is that yours is a peer you own, and
nobody else's machine is ever in the path. Claiming "no relay" would be a lie
told by a project that had not yet met a real phone.

**Rendezvous is somebody else's infrastructure.** Discovery rides a public
Logos fleet on a shard shared with other applications. That is a dependency
and a metadata surface: the shard is visible, and the crowd on it is the only
cover there is. Your *traffic* never touches it — tunnels keep working with
the fleet unreachable — but "no third party anywhere" would be false.

**The network key is still a shared secret.** Membership itself is now an
admin-signed credential that expires and can be revoked
([ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md), built), so
removing a device no longer means rotating for everyone. But the network key
still derives the rendezvous topics, the announce payload key and the pairwise
PSKs, so leaking it still exposes the control plane.
[ADR-020](docs/adr/020-membership-is-a-seam.md) explains why the rewrite that
would remove it is deliberately not built.

**It is a prototype**, used daily by its author and nobody else. See
[Status](#status) for exactly what is proven and what is not, and
[SECURITY.md](SECURITY.md) for what is deliberately deferred.

**Migrating from `logos-vpn`?** Nothing breaks. The daemon prefers
`/etc/shrooms` and `/var/lib/shrooms` but keeps using the pre-rename paths when
only those exist — which matters, because `/var/lib/logos-vpn` holds the device
identity, and losing it gives a node a new overlay address and makes it a
stranger to every peer. Move the files when convenient:

```console
$ sudo systemctl stop shrooms
$ sudo mv /etc/logos-vpn /etc/shrooms && sudo mv /var/lib/logos-vpn /var/lib/shrooms
$ sudo systemctl start shrooms
```

The Android application id is still `dev.logos.vpn`, renamed separately: it
means an uninstall and a rejoin rather than an update.

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
- **Rendezvous** — Logos Delivery pub/sub, used intermittently: cold start, network
  change, partition repair.
- **Steady state** — WireGuard relearns a peer's endpoint from any
  authenticated packet, so a node that roams and sends first needs no signalling
  at all.

Two properties fall out of that:

**No IPAM.** Your overlay address is derived from your device key inside a
prefix derived from the network key, so every node computes every other node's
address locally. No allocation, no conflicts, no split-brain.

**No coordination server.** Every node holds the full roster, assembled from the
announce stream. `shrooms status` is a real control plane with nothing
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
| Linux | x86-64. Android is built and in daily use; iOS is out of scope |
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
[this repo's releases](https://github.com/vpavlin/shrooms/releases/tag/deps-v1)
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
$ git clone https://github.com/vpavlin/shrooms
$ cd shrooms

$ make deps-basecamp \
    HDR=$(find ~ -name liblogosdelivery.h 2>/dev/null | head -1)

$ make shrooms
$ make test-unit          # optional, ~5s
```

### 2. Bring up the first node

Two ways. **If the machine can build** — Ubuntu 24.04+, Fedora, anything with
glibc ≥ 2.38 — the simplest path is to build there directly, with no docker and
nothing to ship:

```console
$ git clone https://github.com/vpavlin/shrooms && cd shrooms
$ make deps-release && make shrooms
$ sudo make install                       # binary, libraries, systemd unit
$ sudo shrooms init --relay --name vps
$ sudo systemctl enable --now shrooms
```

`make install` relinks the binary with an rpath pointing at
`/usr/local/lib/shrooms`, so it keeps working after you delete the checkout.
To run it in the foreground instead, skip the install and use
`sudo ./bin/shrooms daemon -v`.

**Otherwise, push from your machine** with `deploy.sh`, which builds a container
image and ships it over ssh:

### 2b. Deploy remotely

`--init` creates the mesh and `--relay` makes it forward for peers that cannot
reach each other directly.

```console
$ ./scripts/deploy.sh root@VPS_IP --init --relay --name vps
```

**You almost certainly do not need `--advertise`.** A node announces the
addresses peers *actually observed* it at, learned from their replies to its own
probes, and those come first in what it advertises. So a VPS with a public IP, a
home server behind a port-forwarding router, and a cloud instance that sees
`10.x` all end up announcing something dialable without being told anything.

It exists for the case reflexive discovery cannot cover: when the address peers
must dial is not the one your traffic appears to come from. A router that
rewrites the source port gives an observed port that is not the forwarded one,
and no amount of watching can discover the right answer — it has to be stated:

```console
$ ./scripts/deploy.sh root@HOST --init --relay --name home \
    --advertise 203.0.113.4:51820
```

It also saves the first announce interval, since a node has nothing reflexive to
advertise until a peer has answered it once.

`init` tells you which case you are in: if the machine has no globally routable
address it says so, and otherwise stays quiet.

This checks the host, builds and ships a container image, writes
`/etc/shrooms/config.toml`, and starts it. **Copy the network key it prints** —
it is the only secret, and until the security roadmap's phase 1 lands it is a
permanent bearer credential.

Open the port:

```console
$ ssh root@VPS_IP 'ufw allow 51820/udp'    # or your firewall's equivalent
```

### 3. Join from your laptop

On the machine that is already a member — with its daemon running, since that is
what holds the invite open — ask it to admit one device. It prints a token and
waits:

```console
$ shrooms invite
Invite valid for 15m0s. On the joining device:

  shrooms join --invite BEGUZ-N4WOX-PYMTR-CYKWT-QBYSX-U

Waiting...
```

Then run that here, while the other machine is still waiting:

```console
$ sudo ./bin/shrooms join --invite BEGUZ-N4WOX-PYMTR-CYKWT-QBYSX-U --name laptop
$ sudo ./bin/shrooms daemon -v
```

If a daemon is already running on this machine and has not joined anything, the
join goes through it and it brings the mesh up itself — no second command, no
restart. That is what a freshly installed machine looks like: the unit is
enabled, the daemon waits, and `join` is the only thing you run.

`sudo ./bin/shrooms join <NETWORK-KEY> --name laptop` also works, and is what to
use when nothing is running on the other end.

Leave that running (or `sudo make install` and use systemd here too). On first start it generates a device identity, derives its
overlay address, connects to the fleet, and announces itself.

### 4. Check it works

```console
$ sudo ./bin/shrooms status
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
$ sudo ./bin/shrooms paths
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

### 5b. Stop needing sudo to look at it

The daemon needs `CAP_NET_ADMIN`, so it runs as root and its control socket is
`root:root`. Name a group and `shrooms status` works as you:

```toml
socket_group = "your-username"      # in /etc/shrooms/config.toml
```

The socket has always been `0660`; this is what makes the group half of that
mean anything. Doing it by hand with `chgrp` works too, and is undone by the
next restart — which is how this ended up in the config.

### 6. Use names instead of addresses

Nothing to do — the daemon runs a resolver for the mesh and registers it with
the system on startup:

```console
$ ssh root@vps.mesh
$ ping6 laptop.mesh
$ resolvectl query vps.mesh        # names shrooms0 as the link that answered
```

It is authoritative for one suffix and answers nothing else: no forwarding, no
recursion, no upstream. A VPN that quietly becomes the system resolver is a
surprise nobody asked for and a privacy leak besides, so `.mesh` is scoped to
the interface (`resolvectl domain shrooms0 '~mesh'`) and every other name keeps
going wherever it went before.

If names do not resolve, that registration is the thing to check — serving DNS
and *being asked* are different, and this shipped once doing only the first:

```console
$ resolvectl status shrooms0       # expect DNS Servers: <overlay> and Domain: ~mesh
```

Port 53 needs `CAP_NET_BIND_SERVICE`, which the packaged unit grants. A failure
there is logged and never fatal — losing names is smaller than losing tunnels.

**`/etc/hosts` is the fallback**, for hosts without systemd-resolved:

```console
$ sudo shrooms hosts --write       # once, into a marked block
```

```toml
manage_hosts = "true"              # or let the daemon keep it current
```

Off by default: a VPN editing a file that cloud-init and NetworkManager also
touch should be deliberate. It also needs root, cannot do
`<service>.<device>.mesh`, and does not exist on Android — which is why the
resolver replaces it ([ADR-013](docs/adr/013-name-resolution.md)).

### 6b. Give services their own names

A machine can publish what it runs, so it is reached by name rather than by a
port number someone has to remember:

```toml
# in /etc/shrooms/config.toml on the machine that runs them
services = ["immich:2283", "jellyfin:8096"]
```

From any other device on the mesh — including the phone's browser:

```console
$ curl http://immich.home-server.mesh          # no port
$ curl http://immich.home-server.mesh:2283     # the declared port also works
```

The name is `<service>.<device>.mesh`. `grafana:443->3000` publishes 443 and
forwards to 3000 when the two should differ.

**How the bare name works.** Every service on a device shares that device's one
overlay address, so port 80 cannot simply belong to one of them — something has
to say which service a connection wants. Both protocols that matter in a browser
already do: HTTP puts the name in the `Host` header and TLS in the SNI
extension, before anything else on the wire. So the daemon also listens on 80
and 443 and forwards by the name asked for. Nothing is terminated and no
certificate is involved — the TLS ClientHello is cleartext by construction, the
name is read out of it, and the bytes go on untouched to the application, which
does its own TLS exactly as before.

The limit is worth being plain about: it works for HTTP and TLS, because
nothing else announces the name it dialled. `ssh`, syncthing and databases still
need `<service>.<device>.mesh:<port>`, which always works.

**Why this is not just a DNS entry.** Mesh addresses are IPv6, and a great many
self-hosted applications bind `0.0.0.0` and nothing else. Pointing a name at the
overlay address would then resolve perfectly and connect to nothing. So the
daemon listens on the overlay address itself and forwards to `127.0.0.1`, which
is where the application actually is. Nothing about the application changes, and
it does not need to know the mesh exists.

If something already holds that port on the overlay address — an application
that binds `::` and is therefore reachable already — the daemon says so and
stays out of the way rather than taking the port.

### 6c. The other way round: bind the service to the mesh

`services` forwards a mesh connection to a local port, which means the
application still listens where it always did — usually on `127.0.0.1`, often
with no authentication, on the reasoning that only a local user can reach it.
Publishing it makes every mesh member a local user.

There is a stronger arrangement, and it needs no forwarding at all: **bind the
service to the mesh address**. Only members can route to that prefix, so the
bind itself is the access control.

`sshd`, for instance. Take the address from `shrooms status`:

```console
$ shrooms status | head -2
network       fd3b:ffe9:f81::/48
self          laptop  fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69
```

and give it to sshd:

```
# /etc/ssh/sshd_config.d/mesh.conf
ListenAddress fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69
```

Now `ssh laptop.mesh` works from your phone on mobile data, and ssh is not
listening on your LAN, on café wifi, or on the internet at all.

**One wrinkle worth knowing before it bites you.** That address exists only
while the daemon is running, and a service that binds an address which is not
there yet fails to start. Either order it after the mesh:

```
# /etc/systemd/system/ssh.service.d/mesh.conf
[Unit]
After=shrooms.service
```

or let the kernel accept the bind regardless, which also survives the daemon
restarting underneath it:

```console
$ echo 'net.ipv6.ip_nonlocal_bind=1' | sudo tee /etc/sysctl.d/99-shrooms.conf
$ sudo sysctl --system
```

Peers cannot see any of this until you say so, because a bound port is
*discovered* rather than declared — so `shrooms bound` shows exactly what would
be announced before anything is:

```console
$ shrooms bound
MESH     WOULD ANNOUNCE  REACHED AS
default  ssh:22          laptop.default.mesh:22
default  dev:3000        laptop.default.mesh:3000
test     http-alt:8080   laptop.test.mesh:8080

3 would be announced with announce_bound = "true".
They are already reachable by every member; this is about being told.
```

On a node with more than one mesh the name carries the label, because it has
to: the short form is answered by the first mesh alone, so `laptop.mesh:22`
would reach an address on the wrong network where nothing is listening. A node
with one mesh sees `laptop.mesh:22` and no label, as before.

**The label is the one asking, not the one answering.** Bind sshd to a second
mesh's address and the same port is `laptop.test.mesh:22` from this machine and
`laptop.home.mesh:22` from a device that filed the same mesh under `home` —
because labels are local and deliberately never announced ([ADR-015](docs/adr/015-multiple-meshes-one-daemon.md)).
Both resolve to the same address. It looks like a bug the first time and is the
reason no member can rename a mesh for everybody else.

Announcing is per mesh too — `mesh.<label>.announce_bound` — since telling your
own machines what you run and telling somebody else's are different decisions.

```toml
announce_bound = "true"     # list them in every member's roster
```

The daemon's own ports are never announced: the name router on 80 and 443 and
the resolver on 53 are plumbing rather than something you offer.
[ADR-026](docs/adr/026-announce-what-is-bound.md) has the reasoning, including
why this is off by default and why only sockets on *exactly* the mesh address
count — one on `::` is reachable from every network the machine is on, and
listing it as mesh-only would be a lie.

### Reaching things that are not on the mesh

The target does not have to be this machine. A service can point at anything
the publishing device can reach, which makes it a gateway for hardware that
will never run a mesh node — a Home Assistant box, a printer, a NAS web UI:

```toml
# on jimmy-crib, which is on the same LAN as 192.168.0.116
services = ["ha:8080->192.168.0.116:80"]
```

Then `http://ha.jimmy-crib.mesh` from any device on the mesh, including the
phone on mobile data. Nothing is installed on the Home Assistant box and it
needs no configuration; it sees an ordinary connection from jimmy-crib's LAN
address.

**Publish it on a port other than 80.** `ha:80->192.168.0.116:80` would work,
but the service takes port 80 on the overlay address and the shared-port name
router cannot then have it — so every *other* service on that device loses its
bare name. Publishing on 8080 leaves 80 to the router, and you get both
`http://ha.jimmy-crib.mesh` and `http://ha.jimmy-crib.mesh:8080`.

> **This is a hole from the mesh into your LAN, and it is worth naming.**
> Every device holding the network key can now reach that address and port,
> including a phone that leaves the house. The gateway device is doing exactly
> what you asked; the thing to be deliberate about is that mesh membership now
> implies access to a machine that never joined the mesh. Publish the specific
> port you want reachable, not a router's admin interface.

> **Publishing a loopback service exposes it to the whole mesh.** Plenty of
> things bind `127.0.0.1` *as* their access control, on the reasoning that only
> a local user can reach them. Listing one under `services` makes every device
> holding the network key a local user. Publish deliberately.

Services are not announced, so `shrooms status` lists what *this* machine
publishes and no node knows what any other one runs. That is why a name for a
service that does not exist still resolves — to the machine that would run it —
where an HTTP request gets a 404 listing the names that do exist. Adding a
service needs no coordination with any other device, and only TCP is forwarded.

Service names need the resolver, not `/etc/hosts`: a hosts file can only hold
names this machine already knows, and only the device running a service knows
that it does. `manage_hosts` keeps working for device names either way.

Giving each service its own address, so that any protocol works without a port,
needs a change to how addresses are derived; that is
[ADR-019](docs/adr/019-service-addresses.md), and it is not built.

### 7. Add more machines

```console
$ sudo ./bin/shrooms join <NETWORK-KEY> --name office     # locally
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
| `.mesh` names do not resolve on Linux | the resolver is running but not registered with the host. `resolvectl status <iface>` should show it under DNS Servers with `~mesh` as the domain; if not, the daemon log says why |
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
is reimplemented. See [ANDROID.md](ANDROID.md) for the build and
[ADR-016](docs/adr/016-android-reuses-the-go-core.md) for why.

**Working on a real phone**, not a demo: tunnels to peers, `.mesh` names through
an in-app resolver, and roaming between wifi and mobile data without user
action — direct on wifi, relayed on the carrier's CGNAT, and the transition
survived in both directions. Built with `make apk`, or published to an F-Droid
repo with `make fdroid`.

## Membership without a shared secret

Today one secret does everything: the network key derives the topics, the
payload key, the pairwise PSKs and the address prefix — so every device holds
it, and **holding it is what membership is**. A leak from any device is a leak
of the mesh, revocation means rotating for everyone, and every member can enrol
members. That is the largest weakness in the system and it is documented rather
than defended ([SECURITY.md](SECURITY.md)).

[ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md) separates the two
things the key conflates. The mesh's identity becomes an admin **public** key,
membership becomes an admin-signed credential naming one device, and the admin
key is needed only to enrol and revoke — so it can live offline, on a Keycard or
in a drawer, never on a participating node.

```console
$ shrooms init --name laptop               # a mesh: network key, admin keys,
                                           #   and this device's credential
$ shrooms invite                           # admit one more device, once
$ shrooms admin revoke --device <hex>      # withdraw one before it expires
```

**Creating a mesh is one command and adding a device is two** — `invite` here,
`join --invite` there. That matters more than it sounds: the intermediate
version had six steps and a credential blob copied between machines by hand,
which is the sort of thing people skip.

```console
laptop $ shrooms invite
        Invite valid for 15m0s. On the joining device:
          shrooms join --invite BEGUZ-N4WOX-PYMTR-CYKWT-QBYSX-U
        [QR code]
        Waiting...

vps    $ shrooms join --invite BEGUZ-N4WOX-PYMTR-CYKWT-QBYSX-U --name vps
        Asking to join as "vps"...
        Enrolled. Credential serial 1786439411, expires 2026-09-10T11:03:51+02:00.
```

The token is 128 bits, good for **one device and fifteen minutes**, and both
sides derive from it where to meet and what to encrypt with — so a wrong token
addresses a topic nobody answers rather than a guess anyone can grind against.
What comes back is the network key, the admin keys and a credential issued to
that device's keys, sealed to the device that asked
([ADR-017](docs/adr/017-invite-tokens.md)). The network key never appears on a
screen.

"Used once" needs no consensus: an invite is answered only by the machine that
issued it, so it is one machine's local decision. The price is that **the
inviter's daemon must be running while the other device joins** — which is also
the point, since it means a human is present.

The daemon is the end that listens and publishes, because it is the node already
connected to the fleet; the CLI only mints the token and signs the credential.
So the admin key never reaches the daemon and the network key never reaches the
CLI, and neither one can admit a device by itself.

**The phone joins the same way.** Scan the QR that `shrooms invite` prints, or
paste the token into the one field on the join screen — it takes an invite or a
network key and tells them apart itself. The credential lands in the app's state
and the phone is a member of a credentialled mesh, which it could not be before.

`shrooms join <NETWORK-KEY>` is still there for bootstrapping and recovery, and
`shrooms admin init` mints an authority separately for a mesh created with
`--no-admin`.

`init` prints a **recovery key once and never stores it**, and puts both public
keys in your config:

```toml
admin_keys = ["EGRWTGUF…", "3Y5HMGWB…"]    # public; belongs in git if you like
```

**Two keys, always, because the set is fixed at mint.** The mesh id commits to
it and the address prefix derives from the id, so adding a key later
re-addresses every node. One is recovery; the other becomes the renewal key that
lets credentials refresh while the root stays offline.

**Both schemes run at once.** A mesh with no `admin_keys` behaves exactly as it
does today. With them, a peer must also present a credential the set signed,
naming the same device and tunnel keys as the announce that carried it — so a
copied credential admits nobody.

That binding is needed because a credential holds nothing secret and **every
mesh member can read one**: it rides inside an announce, which is encrypted to
the mesh rather than to a particular recipient. So any member — or anyone who
obtains the network key — can lift a credential; they simply cannot use it,
because it names someone else's keys. A passing observer on the shard sees none
of this, only a fixed-size ciphertext.

**Credentials expire, and that is the point.** A gossip bus lets an attacker
suppress a revocation it cannot forge; expiry is what bounds that, and gossiped
revocation is only the fast path. The lifetime is the admin's choice per device
(30 days by default) and lives inside the signature, so a device cannot extend
its own membership.

**Revocations travel over the mesh.** `shrooms admin revoke` signs one and hands
it to the local daemon over the control socket — the admin key is offline by
design, so something with a rendezvous connection has to put it on the bus. From
there every node verifies it against the admin keys *itself*, drops the device,
and passes it on
— so a compromised node cannot un-revoke anyone by staying quiet, and a node
that was offline learns it from whoever is up. Entries are kept until the
credential they withdraw would have expired anyway; after that expiry does the
same job.

**The admin key is encrypted at rest** with a passphrase (scrypt + XChaCha20),
prompted once and confirmed. Not the system keystore: Secret Service needs
D-Bus and an unlocked session — so a stolen powered-on laptop still yields the
key — and does not exist on a headless machine. `--no-passphrase` is there for
a file you keep on an encrypted volume.

Renewal is a sweep rather than a ceremony per device: `shrooms admin renew`
asks a running node who is on the mesh, signs a fresh credential for everyone
inside ten days of expiry, and hands them back to be delivered over the control
plane. What is deliberately not built is renewal with nobody present, which
would need a signing key that is online — a different posture from an admin key
used a handful of times a year.

## More than one mesh

A node can belong to several networks at once — your own machines, and a shared
one with somebody else — without either being able to see the other
([ADR-015](docs/adr/015-multiple-meshes-one-daemon.md)).

```console
$ shrooms init --mesh shared          # mint a second network on this node
$ sudo systemctl restart shrooms
$ shrooms invite --mesh shared        # admit one device to it
```

```console
$ shrooms status
mesh default  fd69:bd41:d9bc:7fb7:…  fd69:bd41:d9bc::/48  shrooms0   peers 2
mesh shared   fdfb:6ad9:cb3f:2e1a:…  fdfb:6ad9:cb3f::/48  shrooms01  peers 1
```

Each mesh gets its own key, its own identity, its own interface and its own
port. The identity matters: the overlay's host bits are a hash of the device
key, so reusing one identity would carry the same 80-bit suffix into every mesh
and let anyone in two of them correlate you. It is also forced — WireGuard
allows one preshared key per peer and ours is per mesh, so a peer you share two
meshes with cannot be one entry on one device.

**Joining a mesh does not bridge it to another.** Each node is an endpoint, not
a router: there is no forwarding, and `AllowedIPs` bounds what each peer may
send. The obvious fear — "I joined a shared mesh and exposed my home network" —
is not what happens.

Names take a mesh label when they need one:

```console
$ ping vps.shared.mesh     # unambiguous
$ ping nas.mesh            # fine while only one mesh has a `nas`
```

The short form is answered only when exactly one mesh has that name, so a node
with one mesh — which is most of them — sees no change, and ambiguity removes
the short name rather than silently picking a network for you.

The first mesh in a config keeps the interface, port and identity it always had,
so adding a second changes nothing about the first.

### Seeing them, and switching one off

`shrooms mesh` lists every mesh this device belongs to, running or not — which
is the point of it. `status` reports what the daemon is *doing*, and a mesh
that has been switched off has no instance to report, so it vanishes from the
one list that could have switched it back on. That is exactly how a mesh went
missing here for a day.

```console
$ sudo shrooms mesh
MESH     STATE  PREFIX                CREDENTIAL  RELAY  SERVICES
default  on     fd3b:ffe9:f81::/48    not needed  yes    2
test     OFF    fd7b:15fb:5ec1::/48   held

test is switched off. It keeps its key and credentials:
  sudo shrooms mesh enable test && sudo systemctl restart shrooms
```

Switching one off is the reversible half of leaving it: the key stays, the
credential stays, it simply does not run. Leaving discards the config entry and
needs a fresh invite to undo.

It reads the config directly rather than asking the daemon, so it works when
the daemon is down — which is when you most want to know what is in the file —
and needs `sudo`, because that file is full of network keys.



### HTTPS

The name router serves **http** by default, and browsers try **https** first.
That combination used to hang: 443 was accepted, the name matched, and the
browser's ClientHello was handed to a plain HTTP server, which answered with an
error no TLS client can read. Now 443 is not served at all unless a service says
it speaks TLS, so the browser's attempt is refused immediately and it falls back
to http.

```toml
services = ["jellyfin:8096", "vault:8200/tls"]
```

`/tls` means "this backend speaks TLS; route it on SNI". Everything else is
http, and `http://jellyfin.k11.mesh` is the address that works.

### Firewalls

A host firewall does not know the mesh interface is the mesh, and puts it
wherever it puts unknown interfaces. On Fedora and RHEL that is firewalld's
`public` zone, which allows ssh and little else — so `ssh vps.mesh` works while
a published service on port 80 is refused, which is a confusing pair of
symptoms to debug.

```console
$ sudo firewall-cmd --permanent --zone=trusted --add-interface=shrooms0
$ sudo firewall-cmd --reload
```

That says traffic arriving over the mesh is trusted, which matches how this
works: access is decided by membership and enforced by WireGuard, not by port.

**On a mesh you share with other people, do not do that.** `trusted` means their
devices reach everything on the machine, not only what you published. There,
open the specific ports instead:

```console
$ sudo firewall-cmd --permanent --add-port=80/tcp --add-port=443/tcp
```

The UDP port also has to be reachable for the tunnel itself — 51820, plus one
per additional mesh:

```console
$ sudo firewall-cmd --permanent --add-port=51820/udp && sudo firewall-cmd --reload
$ sudo ufw allow 51820/udp        # Debian and Ubuntu
```

## Bandwidth, and what a node contributes

A node joins a **public, shared cluster**, and by default it relays for it. That
is the neighbourly setting and it is not free. Measured on a home connection
against logos.test, idle — no VPN traffic at all — over ten minutes each:

| | Core (default) | Edge |
|---|---|---|
| received | 15.6 MB/h | 1.9 MB/h |
| sent | 4.7 MB/h | 1.6 MB/h |
| **total** | **20.3 MB/h — 0.49 GB/day** | **3.4 MB/h — 0.08 GB/day** |
| connections opened per 10 min | 139 | 90 |

**Almost none of that is yours.** Of 745 messages the Core node handled, 693
belonged to another application entirely and 14 were on this mesh's shard. The
reason is in the metadata exchange — `localShards="[0,1,2,3,4,5,6,7]"` — a Core
node subscribes to every shard in the cluster, so it carries the cluster, not
your mesh.

By comparison shrooms's own traffic is nothing: a 512- or 1024-byte announce
every 45s — the larger size once a mesh uses credentials, which do not fit
beside the endpoints in the smaller one —
a 104-byte probe per working path every 5s, and a WireGuard keepalive every 25s.
A three-node mesh sits well under 1 MB/h. The rendezvous relay costs 20–30× the
protocol it exists to serve.

```toml
mode = "Edge"      # subscribe and forward nothing
mode = "Core"      # relay for the network (default)
```

Edge uses filter and lightpush instead of gossipsub. It was verified receiving:
a Core publisher's message reached an Edge subscriber 281 ms later, same message
hash. Nothing else in the config changes, and the mesh behaves identically —
this is about what you carry for other people, not about how your own traffic
moves.

**Use Edge on anything metered or battery-powered**, and keep Core where
bandwidth is flat and the machine is always on — a VPS is the right place to
contribute relay capacity, a phone is not. On Android the setting is on the main
screen rather than in the config file.

Two honest costs. Edge leans on the fleet's service nodes for filter and
lightpush, which [ADR-003](docs/adr/003-waku-as-rendezvous-not-control-plane.md)
already accepts by treating messaging as rendezvous rather than a control plane;
a dropped subscription self-heals within an announce interval. And lightpush
means a service node sees you as the publisher directly, which is weaker than
the already-weak sender anonymity [SECURITY.md](SECURITY.md) describes.

**It is still Core by default**, including on Android. Someone has to relay, and
changing what a node contributes to a shared network should be a decision rather
than a default that quietly picks a side.

## A word about the daemon

The thing on each machine is the **shrooms daemon**, and that word is carrying
more than it looks.

A Unix daemon was named at MIT in the sixties after *Maxwell's demon* — the
being in the thought experiment who stands at a little door sorting molecules,
tirelessly, for no reward. The name stuck because that is what a background
process is. But the word underneath it is the Greek *daimōn*, which is not a
devil: it is an attendant spirit, an intelligence that goes with you. Socrates'
*daimonion* never told him what to do and only ever warned him off a mistake.
Not a master, not a servant. Along for the trip.

Which makes this almost embarrassingly literal. A mycelium under everything,
quietly connecting things that look separate. Something fruits where conditions
are right. A helpful spirit sits with each machine and does the sorting. Nobody
is in charge and it works anyway.

The good trips are the ones where the thing in the background is on your side —
so no coordination server, no company in the path, nothing that phones home.
Your daemon answers to you and to nobody else. That is the whole design, and
also the correct set and setting.

It is the practical difference from every mesh with somebody else's control
plane, too. Those have a daemon as well; it just does not work for you.

## Desktop monitoring and control

A Basecamp view — the same graph and list as the phone, and the same things you
can do from it. It reads the mesh, and it can change this device's own settings:
name, light or relay node, published services and whether they are announced,
which meshes run, joining a mesh with an invite token and leaving one
([ADR-025](docs/adr/025-control-from-a-desktop-app.md)).

Three things it gained so the two front-ends read as one product:

- **A services list**, grouped by mesh: everything every peer offers, as the
  address you would actually type. Announced services (ADR-023) come out as
  `http://immich.jimmy-crib.mesh`; bound ports (ADR-026) as
  `jimmy-crib.mesh:22`, marked as such, because one is a URL and the other is a
  host and a port.
- **A log pane**, the same tail the phone has always had. The daemon keeps its
  last two hundred lines in memory and serves them over the socket — Basecamp
  cannot read the journal, and "what is it doing" is the first question anybody
  asks a mesh that has not come up.
- **A restart button**, which is the other half of every setting whose result
  says "on the next restart". It refuses when nothing would start the daemon
  again, so it can never be a stop button by accident.

**Membership is shown, not managed.** Each mesh and each peer carries when its
credential runs out, which is the one failure here that happens on a schedule —
a known day, a device silently off the mesh, and nothing else on the page
hinting at it. A credential this node has never seen reads as "unknown" rather
than as expired, because those have opposite fixes.

**What it cannot do is admit anybody**, and that is the line the design draws.
On a mesh with `admin_keys`, membership is a credential signed by a key the
daemon has never held — a passphrase-protected file in your home directory — so
nothing reachable through this socket can make a device a member or remove one.
Admission still runs through `shrooms invite` and a passphrase prompt, which is
where the friction belongs.

> **Updating the core module needs Basecamp restarted; updating the view does
> not.** `shrooms` is a QML view and reloads when Basecamp installs a new one.
> `shrooms_core` is a native plugin, mapped into Basecamp's own process at
> startup — installing a new `.so` replaces the file and changes nothing that is
> running. Every method added to it therefore reports **"Invalid response"**
> until Basecamp is restarted, which reads as a broken feature rather than as a
> stale plugin. If a new control says that, restart Basecamp before looking
> anywhere else.

**The socket group is a real grant, like `docker`'s.** Anyone in it can read
your mesh's control plane and change this device's behaviour. Set it to a group
you would trust with that — on a personal machine, your own login — and leave it
unset on a shared one:

```toml
socket_group = "vpavlin"
```

It reads the daemon through **`shrooms_core`**, a companion module installed
alongside it. That indirection is not ceremony. A `ui_qml` app runs inside a
sandbox (Basecamp's spec, *QML App Sandboxing*): a deny-all network manager
blocks every outgoing HTTP request, and `XMLHttpRequest` refuses local files
unless the host sets `QML_XHR_ALLOW_FILE_READ`, which Basecamp does not. So
neither a status file nor a port is reachable from the view, whatever their
permissions — a status file with the right group, `ui_listen`, and a file
beside the view were all proposed here before anyone read the log that says so.

A Logos module runs in its own process and is not sandboxed, which is the route
Basecamp prescribes: UI apps reach the outside indirectly, through modules.
`shrooms_core` does one HTTP GET over the daemon's control socket and hands back
the JSON; the view calls it with `logos.callModule`, exactly as `qaku` calls
`qaku_core`.

The socket is `0660` and the daemon runs as root for `CAP_NET_ADMIN`, so name a
group you are in:

```toml
socket_group = "your-username"      # in /etc/shrooms/config.toml
```

Without it the view says precisely that, rather than showing an empty mesh.

Outside Basecamp — the offscreen check, or a plain `qml` runtime — there is no
sandbox and no core module, so the view falls back to a file beside itself, an
absolute `status_file`, then the daemon's endpoint. `basecamp-check` exercises
all three.

```console
$ make basecamp-check      # load the real view offscreen against a fixture
$ make basecamp-lgx        # build the installable package
$ make basecamp-publish    # and put it where a device can install it
```

`basecamp-publish` copies the package next to the F-Droid repo on the same
machine — a stable name and a versioned one — because an `.lgx` is a package
rather than a repository and has no update mechanism of its own. Installing a
newer one means downloading it again.

Packaged as an **LGX**: a gzipped tar of `manifest.json` plus
`variants/<platform>/`, with a content hash per directory. The build comes from
[logos-module-builder](https://github.com/logos-co/logos-module-builder) through
a twelve-line `basecamp/flake.nix`, so the manifest and its hashes are generated
rather than hand-maintained. CI builds `lgx-portable` on every push and uploads
it as an artifact — the plain `lgx` output can reference paths in the nix store
of the machine that built it, which is fine for a dev loop and useless as a
download.

## Status

| | |
|---|---|
| **S1** cgo binding to Logos Delivery | ✅ publish→receive over the real fleet |
| **S3** rotating topics stay on one shard | ✅ 6 epochs, all to `/waku/2/rs/2/3` |
| **M0** WireGuard sharing a socket with control traffic | ✅ tunnel + control packets, no root |
| **M1** discovered peers replace static config | ✅ **over the real internet**: NATed laptop ↔ VPS, direct tunnel, ssh across it |
| **M2** NAT traversal | 🟨 reflexive discovery proven on real NAT; punching between two NATed nodes still unproven — [T3](TESTING.md) settles it |
| **M3** relay fallback | ✅ **carrying real traffic**: a phone on carrier NAT reaches its peers through a VPS relay it discovered itself |
| **M6** name resolution | ✅ resolver registered with the host, on Linux and Android; `<service>.<device>.mesh` too |
| **services** | ✅ local ports and LAN devices published by name, including things that never joined the mesh |
| **Android** | ✅ a full participant, in daily use — tunnels, names, roaming between wifi and mobile data |
| **M4** seamless operation | 🟨 roaming survives, the mesh repairs itself after a rendezvous outage, and both daemon and app now restart themselves when that plane dies quietly; switching networks is still rough on the phone |
| **M5** credentials | ✅ issued, carried, verified, revoked over the control plane, and renewed by a sweep ([ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md)); a sweep reissued to a remote device on live hardware, 2026-08-13 |
| **multi-mesh** | ✅ several meshes in one daemon and one app, each with its own identity, addresses, relay and services ([ADR-015](docs/adr/015-multiple-meshes-one-daemon.md)) |
| **invites** | ✅ one device at a time, fifteen minutes, no key pasted ([ADR-017](docs/adr/017-invite-tokens.md)) |
| **Basecamp view** | ✅ published, with `shrooms_core` reading the daemon from outside the QML sandbox |

`make m0` / `make m1` / `make m3` / `make s1` / `make s3` reproduce these.

[TESTING.md](TESTING.md) has the scenarios worth running by hand — the ones
containers cannot honestly reproduce, including the one that settles whether
hole punching works between two real NATs.

### Adding a machine

On the machine itself, with only docker installed:

```console
$ curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms/master/scripts/install.sh
$ sudo bash install.sh join <NETWORK-KEY> --name laptop
```

To set a machine up **without the key passing through whoever is doing the
setup** — a colleague, a script, an AI agent — prepare it and paste the key
yourself afterwards:

```console
$ sudo bash install.sh prepare --name nas --relay
# then run `sudo shrooms set-key`, which reads it from a prompt so it never reaches
shell history
$ sudo systemctl start shrooms
```

The device identity is generated during `prepare`, so the machine's overlay
address is settled before the key arrives and does not change when it does.

Or to create a new mesh, on the first machine:

```console
$ sudo bash install.sh init --relay
```

Everything after `init`/`join` goes straight to `shrooms`, so its flags are
whatever that version supports — `--name`, `--relay`, `--advertise`, `--port` —
rather than a copy in the script that drifts out of date. The device name
defaults to this machine's hostname.

That is the whole thing: it pulls the image, generates the config, installs a
systemd unit and **starts the daemon**, which then comes up on boot. There is no
separate step to run it. `init` and `join` are the setup performed inside, not
something you invoke separately.

It also installs a `shrooms` wrapper so `shrooms status` works on the host. No Go toolchain,
no checkout, no liblogosdelivery — everything is in the image. Re-running is
safe: the device identity is never replaced by accident, because losing it means
a new overlay address and looking like a different device to every peer.

Afterwards:

```console
$ systemctl status shrooms      # is it up
$ shrooms status                # who is on the mesh
$ journalctl -u shrooms -f      # follow the log
```

Read the script before running it as root, as you should with anything fetched
this way.

Use `scripts/deploy.sh` instead when you want to push to a remote host *from* a
machine that has the repo — for example to deploy an unpushed change.

CI publishes a container image to `ghcr.io/vpavlin/shrooms` on every push to
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

**Relaying works on real infrastructure, as of 2026-08-11.** A phone on mobile
data reached a peer through a relay on a public VPS, over the real internet —
`shrooms status` shows the endpoint as `relay:<key>@<vps>:51820`. Relays are
discovered from the roster, so nothing is told where one is. Relaying is per
mesh: a mesh joined by invite has no relay until a member is told to be one.

**A NATed node can now be reached from outside without a relay**, as of
2026-08-12 — by asking the router rather than by punching. A laptop behind a
domestic NAT was granted a mapping over NAT-PMP, announced it, and a phone on
mobile data dialled it directly, on a mesh with no publicly reachable member
([ADR-024](docs/adr/024-ask-the-router.md)). Whether your router answers is
between you and your router; when it does not, nothing is worse than before.

**Punching between two NATed peers is still unproven**, and it is a different
mechanism from the above. The reasons it is hard are now clearer than they
were:

- *Both ends must choose the same relay, independently.* A relay forwards by
  destination WireGuard key and only for peers that have registered with it, and
  each side picks one only among peers it currently believes are online. A node
  whose rendezvous plane has quietly failed therefore stops relaying without
  anything looking wrong.
- *A NATed node learns its public address by reflection* — a peer's pong echoes
  the address it observed. That needs a peer outside your NAT. On a mesh whose
  members are all behind one NAT, no node can learn its own public address at
  all, and `advertise` has to be set by hand. Asking the router instead is the
  next thing on the roadmap.
- *Carrier-grade NAT cannot be punched or mapped.* A phone on mobile data is
  behind a NAT that is not yours. One publicly reachable member is enough for
  the whole mesh, which is why relays exist.

**The rendezvous plane can fail while everything looks fine.** Tunnels keep
carrying traffic, the roster stops changing, and peers age out one at a time
until the status page is a list of devices marked offline that are all healthy.
Both the daemon and the app now watch for it — including the case where a node
goes deaf to *some* publishers and not others, which a "nothing has arrived"
check sails straight past. The signal is a peer whose WireGuard tunnel is still
rekeying, which proves its daemon is running and therefore announcing, while its
announces have been missing for twelve minutes. Recovery is a restart in both
places, because the delivery library keeps process-global state and has never
survived being restarted inside a live process.

**Renewal has now been run against a real mesh.** On 2026-08-13 a sweep
reissued credentials on a three-member mesh: the admin key signed, the daemon
published, and a *remote* device — a phone, untouched — verified against the
same admin keys and stored its own, going from 28 days left to 30. The half
that had never been exercised was that one, and it works.

Two things it taught. The first run renewed nothing, correctly: every member
had 28 days and the window opens at 10, so the guard is doing its job and a
sweep that renewed eagerly would have looked identical on the surface. And the
one member that did not renew was off the network entirely — renewal travels
over the rendezvous plane, so a device whose tunnel is healthy and whose
Delivery connection is not cannot be renewed. `live` and `online` being
separate fields in the status payload is what made that a one-look diagnosis
rather than an investigation.

**A fleet migration broke everything once, silently.** On 2026-08-07 logos.dev
moved to cluster 3 while the preset compiled into our pinned
liblogosdelivery still said cluster 2. Every peer connected, compared metadata,
disagreed and hung up — which looks exactly like an outage. The default is now
`logos.test`, whose preset is correct, and `cluster_id` exists as an override
for the next time a fleet moves ahead of the library. `make s1` detects this.

**Testing on real infrastructure keeps finding bugs that containers hide**, all
of them timing-, NAT- or lossy-network-dependent. Prefer hardware for anything
in those categories.

## Security

Read [SECURITY.md](SECURITY.md) before running this anywhere that matters. The
short version:

- Control messages and discovery probes are both **encrypted**, fixed-size and
  fixed-rate, on a rotating topic, and not archived.
- **Binding a service to the mesh address is the strongest access control here**
  and costs nothing: only members can route to that prefix, so a service on it
  is unreachable from the LAN or the internet without any firewall rule
  ([ADR-026](docs/adr/026-announce-what-is-bound.md)).
- Membership is an **admin-signed credential** on a mesh with `admin_keys` set:
  bound to one device's keys, valid for thirty days, revocable, and renewed by
  a sweep from the machine holding the admin key. Removing a device costs that
  device rather than the mesh.
- The network key is **still a shared secret**, and still derives the topics,
  the announce payload key and the pairwise PSKs. It is no longer what
  *membership* is, but leaking it still exposes the control plane and still
  means rotating for everyone.
  [ADR-020](docs/adr/020-membership-is-a-seam.md) explains why the rewrite that
  would remove it is deliberately not built.
- Some things leak inherently and cannot be fixed here: the messaging layer leaves the content
  topic and timestamp in cleartext, relay peers see your IP, and WireGuard is
  identifiable as WireGuard.

---

## Documentation

| | |
|---|---|
| [shrooms.vpavlin.xyz](https://shrooms.vpavlin.xyz) | the website: what it is, why, install, guides |
| [DESIGN.md](DESIGN.md) | architecture and the research behind each decision |
| [PROTOTYPE.md](PROTOTYPE.md) | build plan, milestones, what each proved |
| [SECURITY.md](SECURITY.md) | what is protected, what leaks, what is deferred |
| [docs/adr/](docs/adr/) | why each significant decision was made (26 records) |

---

## Roadmap

Done, and in daily use:

- [x] Discovery over Logos Delivery — no coordination server
- [x] Derived addressing, so there is no IPAM and no collisions
- [x] WireGuard tunnels, direct where NAT allows
- [x] NAT traversal, with relay fallback through a node you run
- [x] Names — `<device>.mesh`, and `<service>.<device>.mesh` for what you host
- [x] Android, as a full participant rather than a client of something else
- [x] Roaming between wifi and mobile data without user action
- [x] A Basecamp view for watching a mesh

- [x] One-time invite tokens, so the key stops being pasted
      ([ADR-017](docs/adr/017-invite-tokens.md))
- [x] Admin-signed credentials, expiry, revocation and renewal, so a
      compromised device costs that device
      ([ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md))
- [x] Services published by name — `<service>.<device>.mesh`, routed by `Host`
      and SNI. An address *per service*, which is what makes any protocol work
      without a port, is still only designed
      ([ADR-019](docs/adr/019-service-addresses.md))
- [x] Several meshes from one daemon, each with its own identity, addresses,
      relay and services ([ADR-015](docs/adr/015-multiple-meshes-one-daemon.md))
- [x] A synthetic IPv4 address per peer, so browsers work on networks with no
      IPv6 ([ADR-021](docs/adr/021-synthetic-ipv4.md))
- [x] Ask the router for a port mapping, so a node behind a home NAT is
      reachable without `advertise` and a forwarding rule
      ([ADR-024](docs/adr/024-ask-the-router.md))
- [x] Announce what is bound to the mesh address, so binding a service to the
      mesh is discoverable ([ADR-026](docs/adr/026-announce-what-is-bound.md))

Next, roughly in order:


- [ ] An invite that carries a per-mesh identity, so a device joining a second
      mesh derives a fresh address rather than sharing a suffix
      ([ADR-017](docs/adr/017-invite-tokens.md))
- [ ] The admin key on a Keycard ([ADR-022](docs/adr/022-keycard-for-the-admin-key.md))

---

## Licence

Dual licensed under **Apache-2.0** ([LICENSE-APACHE-v2](LICENSE-APACHE-v2)) or
**MIT** ([LICENSE-MIT](LICENSE-MIT)), at your option — the same pair the Logos
projects use. Apache-2.0 carries an explicit patent grant, which matters for a
network protocol; MIT keeps the widest downstream compatibility.

---

## Scope

Linux and Android. The constraints DESIGN §6 sets out are the ones that shaped
the app rather than ones that deferred it: Doze tears down messaging connections
on every cycle, so intermittency is forced rather than chosen, and the design
assumes it. **iOS is out of scope**: its NetworkExtension memory cap makes an
embedded libp2p node impractical.
