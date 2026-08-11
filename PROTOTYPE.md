# shrooms — Prototype plan (Linux first)

What to build, in what order, and what to prove before building it.

See [DESIGN.md](DESIGN.md) for the architecture and its justification.

**Scope note.** This plan was written Linux-first, with Android deferred to §8.
Android has since been built and ships (§8, [ADR-016](docs/adr/016-android-reuses-the-go-core.md));
the ordering below is left as it was written.

---

## Where we are (2026-08-11)

Scored against the **original constraints**, not the milestone list — those are
what the project is for.

| Constraint | Status |
|---|---|
| Android phones are full mesh participants | ✅ built and shipping. The app runs the same Go core through gomobile ([ADR-016](docs/adr/016-android-reuses-the-go-core.md)), joins by invite, carries several meshes at once through one `VpnService` with a demultiplexer, auto-connects on launch, restarts itself after boot and after its own update, and draws the mesh as a graph. Distributed from a self-hosted F-Droid repo, currently versionCode 16/17 |
| No self-run Waku cluster | ✅ running on the public logos.dev fleet |
| A VPS is acceptable, but **auto-discovered** | ✅ met in code **and on the real internet**. Relays advertise themselves in their ordinary announce and are picked up from the roster; nothing is hardcoded. A phone on 5G reached a peer through a relay on a public VPS |
| Daily driver, not a demo | ✅ in daily use — a laptop and a phone on two meshes each, a VPS and two LAN machines. Roaming, suspend/resume and outage recovery are still not measured, and where the rendezvous plane fails the daemon exits and is restarted rather than recovering in place (M4) |

**The remaining gap is one path.** Punching between two NATed peers is still
unproven, and the last month made it clear it is blocked on more than a test
rig — see M2. Everything else the constraints ask for now runs daily, including
the relay path that carries the pairs a punch would have served.

### Spikes and milestones

| | State |
|---|---|
| **S1** cgo → liblogosdelivery | ✅ round trip over the real fleet |
| **S2** logos.dev Store retention | ⬜ **never run.** Android shipped without depending on it, so it is no longer on the critical path — still unmeasured |
| **S3** rotating topics stay on one shard | ✅ 6 epochs → one shard |
| **S4** WireGuard throughput baseline | ⬜ never run, minor |
| **M0** data plane + shared socket | ✅ containers |
| **M1** Waku-discovered peers | ✅ containers **and real internet** |
| **M2** NAT traversal | 🟨 reflexive discovery proven on real NAT; punching between two NATed nodes still unproven, and now understood to need conditions we have not had — see below |
| **M3** relay | ✅ containers **and real internet** — a phone on carrier-grade NAT reached a peer through a relay on a public VPS, showing as `relay:<key>@<vps-ip>:51820` in `shrooms status`. Selection is per mesh |
| **M4** make it seamless | 🟨 partly. Six nodes run it daily, and two watchdogs restart the daemon when the rendezvous plane dies silently. Roaming, suspend/resume and VPS reboot are unmeasured |
| **M5** credentials, enrolment, revocation | ✅ built — `internal/cred`, `shrooms admin`, enrolment carried inside the invite exchange ([ADR-017](docs/adr/017-invite-tokens.md)), gossiped revocation. Renewal landed today and is verified by unit tests; the sweep has not been run against the live mesh |
| **M6** name resolution | ✅ the daemon serves the roster over DNS, across meshes, and answers `A` with synthetic IPv4 ([ADR-021](docs/adr/021-synthetic-ipv4.md)) so browsers work on v4-only networks. `shrooms hosts` and `manage_hosts` remain |

**Built outside this list:** several meshes in one daemon, with per-mesh
identity, WireGuard device, credentials, relay selection and address block
([ADR-015](docs/adr/015-multiple-meshes-one-daemon.md),
[ADR-021](docs/adr/021-synthetic-ipv4.md)); services published under their own
names with a Host/SNI router (`internal/service`); and the Android app.

### What I would do next, in order

1. **Prove the punch, or stop claiming it is close** (finishes M2). The last
   original property that is implemented but not demonstrated. The relay makes
   it an optimisation rather than a requirement, which is also why it keeps
   being deferred.

   The test needs two NATs that are genuinely different networks and neither of
   them carrier-grade — mobile data cannot be punched or port-mapped at all, so
   the tethering trick this file used to recommend tests the wrong thing. A
   synthetic NAT built to be endpoint-independent proves only that the code
   works against a NAT we made cooperative.
2. **Run the renewal sweep against the live mesh.** It is unit-tested and
   nothing else; credentials expire in 30 days, so the first real deadline
   arrives on its own.
3. **M4 properly** — roaming, suspend/resume, VPS reboot, logos.dev briefly
   unreachable. The watchdogs cover the failure that was actually found; the
   rest of the list is still assumption.
4. **S2** — half an hour, and no longer urgent.

---

## 0. Why Linux-first is much smaller

Restricting v1 to Linux removes two of the five open risks in DESIGN §9 and one
whole subsystem:

| | Status on Linux |
|---|---|
| **R1** — mix blocks Edge-mode publishes | **gone.** Linux nodes run Core mode; Core publishes via relay, which mix does not touch |
| **R3** — Android JNI is thin | **gone.** cgo directly against `liblogosdelivery` |
| Intermittent Waku / wake-on-event | **not needed.** Always-on machines, no Doze — hold a persistent connection |
| Doze landmines (5 s DNS beacon, 8.5 h backoff, `disconnectAllPeers`) | mostly dormant, but still worth defensive handling |
| Battery | irrelevant |

What remains load-bearing: the socket demux, NAT traversal, the relay, and
derived addressing. Those are the parts Android will reuse unchanged.

---

## 1. What you need

| | Purpose |
|---|---|
| 2 Linux boxes (home, office) | mesh nodes, Core Waku |
| 1 small VPS, public IPv4 | relay + echo anchor + Core Waku (+ Store fallback) |
| Go 1.23+, cgo, C toolchain | the daemon |
| Nim + logos-delivery build | to produce `liblogosdelivery` |

No domain (logos.dev entry nodes are hardcoded `/dns4/` in the library), no
cluster, no bootstrap infrastructure, no accounts.

A third Linux box or VM is useful for testing enrolment and revocation without
disturbing the working pair.

---

## 2. User-facing surface

### Configuration is one secret

```toml
# /etc/shrooms/config.toml
network_key = "K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5"   # the only secret
name        = "home-nas"                            # optional, defaults to hostname
```

The **network key** is 32 bytes and does three jobs (DESIGN §7): derives the
rotating rendezvous topic, the payload encryption key, and the per-pair WireGuard
PSKs. In v1 it is a **bearer credential** — anyone holding it is a member.
Revocation means rotating it and re-enrolling.

That is acceptable at 3–5 machines you control, and it is deliberately temporary.
M5 splits authorization out into admin-signed credentials and replaces the
copy/pasted secret with a one-time-use invite token. Two things to get right now
so that migration is cheap:

- **Config must already carry `admin_pk`** (empty in v1). Adding a field later is
  a config break; ignoring an empty one is not.
- **Announcements must already have a `credential` field** (empty in v1), inside
  the signed payload. Peers ignore it while `admin_pk` is unset.

The **device identity** needs no configuration: an Ed25519 keypair generated on
first start in `/var/lib/shrooms/`, never transmitted. Its public key is the
device's identity and the overlay address is derived from it — no allocation, no
registration, no IPAM.

The **name** is self-asserted inside the signed announce. It authenticates as
"the device holding key X calls itself home-nas", which is sufficient for a
personal mesh. No uniqueness enforcement in v1.

### Flow

```
# first machine
$ shrooms init --name home-nas
Network key: K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5
  copy this to your other machines — it is the only secret
Device:      home-nas
Overlay IP:  fd3a:9c21:7e04::8f21:aa03
Wrote /etc/shrooms/config.toml

$ systemctl enable --now shrooms

# every other machine
$ shrooms join K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5 --name office-box
$ systemctl enable --now shrooms
```

### CLI

```
shrooms init [--name N]        generate identity + a new network
shrooms join <KEY> [--name N]  generate identity, join an existing network
shrooms status [--json]        roster + tunnel state (see §3)
shrooms peers                  roster from gossip, including offline devices
shrooms paths <name>           candidates gathered, which won, and why
shrooms ping <name>            overlay reachability
shrooms key show               print the network key (for adding a machine)
shrooms key rotate             replace it: new mesh, everyone re-joins
```

The daemon exposes a unix socket (`/run/shrooms/shrooms.sock`) with a small JSON API;
the CLI is a thin client over it. That also gives you a monitoring hook for free.

### systemd

```ini
[Unit]
Description=shrooms overlay mesh
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/shrooms daemon
AmbientCapabilities=CAP_NET_ADMIN
StateDirectory=shrooms
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

`CAP_NET_ADMIN` for the TUN device — no need to run as root.

---

## 3. The control plane you get for free

Every device publishes a signed `EndpointAnnounce` every 30–60 s, so **every node
holds the full roster**. Compose that with local WireGuard state:

| Source | Gives you |
|---|---|
| gossip roster | who exists, their pubkeys, claimed names, overlay IPs, last announce |
| WireGuard | last handshake, rx/tx bytes, current endpoint per peer |
| your own path racing | direct vs relayed, v4 vs v6, which candidate won, RTT |

```
$ shrooms status
network  fd3a:9c21:7e04::/48          peers 4 (3 up)
self     home-nas  fd3a:...:8f21:aa03

NAME         OVERLAY IP            PATH            HANDSHAKE   RX/TX
office-box   fd3a:...:1c04:be91    direct v6       11s         1.2G / 430M
laptop       fd3a:...:9e77:0d12    direct v4       47s         22M / 18M
vps-relay    fd3a:...:4b12:77f0    direct v4       8s          890M / 1.1G
phone        fd3a:...:2a05:c831    offline (seen 6m ago)
```

**Reachability is per-observer.** Node A may hold a direct tunnel to B while C
reaches B only via relay, so `status` shows *your* view. A global topology map
would require nodes to gossip their own reachability over the mesh — cheap once
the mesh carries its own control traffic, but not v1.

---

## 4. Spikes — run these before writing the daemon

Two of the original five are Android-only and deferred. What remains:

### S1 — cgo → `liblogosdelivery` ✅ **PASSED** (2026-08-06)

Two Go processes, each embedding a Nim node, completed a publish → receive round
trip over the public logos.dev fleet:

```
message_received 0x821ada51c3e0974bd1... on /logosvpn-spike/1/final-.../proto (28 bytes)
RECEIVED "s1-spike-1786022842269494195"
dropped events: 0
S1 PASS (event path works)
```

Implemented in `internal/waku/` (`waku.go`, `callback.go`, `events.go`) and
`cmd/wakuspike/`. Run with `make s1` / `make probe`.

**What it proved:** the Nim and Go runtimes coexist in one process; request
callbacks and the async event callback both fire; the event thread never blocked
(0 dropped); 4 of 6 logos.dev entry nodes dialled; `rlnRelay=false` confirmed;
capabilities `[Relay, Filter, Lightpush, Mix]`.

**Findings that change how you write code:**

1. **Use `preset: "logos.dev"`, not `clusterId: 2`.** Both resolve the same
   network config, but only the preset loads the six entry nodes. With
   `clusterId` you get *"creating service discovery as seed node (no bootstrap
   nodes)"* and never connect. The library even warns you to use the preset.
2. **Payload encoding is asymmetric.** `logosdelivery_send` takes the payload as
   a **base64 string**; `message_received` delivers it as a **JSON array of byte
   values**. Handled in `internal/waku/events.go`; do not assume symmetry.
3. **Event callback shape depends on the build** — see §4a.
4. **Observed publish→receive latency ≈ 3.5 s** on logos.dev, well above the
   ~240 ms derived in DESIGN §4. Measure properly (§9) before relying on either
   number. Still comfortably inside the design's tolerance.
5. UPnP-IGD is present on the dev network (port mapping conflicted, but the
   device answered) — relevant to M2.

Observed event types: `message_received`, `connection_change`,
`connection_status_change`, `relay_topic_health_change`.

### S1a — Where to get liblogosdelivery ⚠️ read before building anything

**Do not build logos-delivery from source unless you have to.** On a current
Linux host (git 2.51, nimble 0.22.3, nim 2.2.4) it fails twice, neither in our
code:

- Nim's own lockfile checksum does not match what nimble computes from the git
  checkout (`68bb85cb…` expected vs `a092a045…` actual) — git-version-dependent.
- A nimble path bug on git-ref-pinned deps: for `bearssl_pkey_decoder` the temp
  clone dir name contains `#`, and nimble creates the directory truncated at the
  `#` then runs `git -C` on the full name.

The second cascades into a misleading `Couldn't find a solution for the packages`
solver dump — **and `make -j` makes it far worse**, turning a one-line checksum
error into 14,000 lines of noise. Build serially if you must build.

Switching to a release tag does not help: the commit the Android bindings pin
(`7a3a064b`, proven buildable) has **byte-identical** pins for both problem
packages. `v0.38.1` is from 2026-05-07 and predates this lockfile format.

**Use the artifact Logos Basecamp installs instead.** It is current, packaged,
and carries the full kernel surface:

```
lib: ~/.local/share/Logos/LogosBasecamp/modules/delivery_module/liblogosdelivery.so
hdr: <logos-workspace>/repos/logos-modules/logos-delivery-module/vendor/logos-delivery/liblogosdelivery/liblogosdelivery.h
```

These two match each other. The Makefile defaults to them.

**Use Basecamp/Logos Core as a source of the artifact, not as a runtime.** Logos
Core is a Qt6 desktop module host with per-module process isolation and QRO IPC;
we need a headless systemd daemon holding a TUN device with `CAP_NET_ADMIN`.
Linking the `.so` directly from Go gets the library without the framework, and
without putting Qt on a VPS.

### S1b — API skew between builds ⚠️ pin your header to your .so

The packaged build and logos-delivery master differ in ways that break
compilation, not just behaviour:

| | packaged (Basecamp) | master `a5d7818` |
|---|---|---|
| events | `logosdelivery_set_event_callback` — one global callback | `add_event_listener` / `remove_event_listener` per event |
| config JSON | flat `WakuNodeConf` fields: `{"mode":"Core","clusterId":2}` | nested `{mode, preset, messagingOverrides}` |

`internal/waku` currently targets the **packaged** build. Pin the header to the
`.so` you actually link, and re-check both when upgrading either.

### S2 — What is logos.dev's Store retention?

Publish a non-ephemeral message, then `waku_store_query` after 1 h, 6 h, 24 h.
Note which fleet nodes answer and whether they agree.

Matters less on Linux than it will on Android (always-on peers overlap, so the
async mailbox is less critical), but it determines whether the VPS needs to run
Store. Cheap to find out now.

### S3 — Rotating content topics stay on one shard ✅ **PASSED** (2026-08-06)

```
expected shard      : /waku/2/rs/2/3
topics published    : 6
distinct shards used: 1
  /waku/2/rs/2/3
S3 PASS: 6 rotated topics all routed to /waku/2/rs/2/3
```

Six consecutive epoch topics, published to a live logos.dev node, all routed to
one shard — matching the value `internal/topic` computed independently. The
rotation design holds and nwaku#2538 does not bite.

Implemented in `internal/topic/` + `cmd/s3topics/` + `scripts/check-s3.sh`;
run with `make s3`.

**Two traps found while building this:**

1. **`waku_pubsub_topic` is not the autosharding resolver.** It formats a
   *named* (static-sharding) topic — passing a content topic returns
   `/waku/2//app/1/name/proto`, i.e. plain concatenation. The autoshard mapping
   is `sha256(app‖version)[24:32] mod numShards`, implemented in `topic.Shard`.
2. **A Core node relays other people's traffic across all 8 shards**, so
   grepping the log for `pubsubTopic=` sees the whole cluster. The check has to
   match `start publish Waku message` lines carrying *our* app/version prefix.
   An unfiltered grep reports 8 shards and a spurious failure.

### S4 — WireGuard baseline (30 min)

`iperf3` over a plain `wg` tunnel between home and office. Establishes what
"good" looks like before you start measuring your own thing.

*(Deferred to the Android phase: the mix-publishing test and the Android build
chain.)*

---

## 5. Milestones

### M0 — Two boxes, static config, no Waku ✅ **PASSED** (2026-08-06)

Implemented in `internal/identity/`, `internal/wg/`, `cmd/m0demo/`. Run with
`make m0`; unit tests with `go test ./internal/...`.

Two userspace WireGuard peers in one process over loopback, each on a netstack
TUN so **no root and no real interface are needed**:

```
mesh prefix : fd5a:22e9:a17e::/48
A overlay   : fd5a:22e9:a17e:bf44:9753:491d:203:5b9e
B overlay   : fd5a:22e9:a17e:6407:54a6:5a06:c6a:c41f
  TCP echo 22 bytes A->B->A in 2ms
  B received "ping-from-A from 127.0.0.1:51820"
  A received "ping-from-B from 127.0.0.1:51821"
  TCP echo 22 bytes A->B->A in 1ms
M0 PASS
```

Proved: overlay addresses derived from device keys with no allocator, both
inside the network-key-derived `/48`; a real WireGuard tunnel carrying TCP;
control packets in both directions **on the same UDP socket**; and the tunnel
still healthy afterwards.

The demux (`internal/wg/bind.go`) wraps `StdNetBind`'s `ReceiveFunc`s and
filters in place, so batching and GSO/GRO survive — the data path never goes
through a channel. Magic is `"mvpn"` (`0x6d…`), chosen to sit outside
WireGuard's `0x01..0x04` type range and away from Tailscale disco's `0x54`.

Note on the harness: the *first* connection can take ~5 s while the WireGuard
handshake completes, then drops to 1–2 ms. Don't read the first number as
steady-state latency.

Unit tests cover the part most likely to break silently: compaction keeping
`packets`/`sizes`/`eps` aligned. Misalignment there attributes a packet to the
wrong peer and surfaces much later as an inexplicable handshake failure.

- Embed `wireguard-go` in `shrooms daemon`.
- `conn.Bind` wrapper with first-byte demux (magic > `0x04`, ≠ `0x54`), following
  NetBird's `ICEBind`. **Verify `ReadBatch` / `SplitCoalescedMessages` / GSO
  survive** — that is the entire point of the exercise.
- Hardcode both peers' keys and endpoints.
- Derive overlay addresses from pubkeys.
- Round-trip a control packet on the shared socket without disturbing WireGuard.

**Done when:** `iperf3` over the tunnel matches S4's baseline and control packets
round-trip on the same socket.

**~2–4 days.**

#### Userspace, not kernel — decide this now

Kernel WireGuard is faster and needs no embedding, but **it owns its UDP socket
and will not share it**, which is precisely what punching needs (DESIGN §3).
Starting with kernel WG means rewriting the data plane at M2.

Use userspace from the start. Your WAN is slower than wireguard-go anyway, and
Android will reuse the same code path unchanged. Kernel WG can return later as an
opportunistic fast path for nodes with a stable public endpoint.

### M1 — Waku announce replaces static config

Peers discover each other; Waku becomes load-bearing.

- Wire in `liblogosdelivery` via the S1 binding. **Core mode**, persistent
  connection — no intermittency logic on Linux.
- `EndpointAnnounce`: signed, AEAD-encrypted, fixed-size, monotonic `seq`, every
  30–60 s.
- Rotating topic derivation (or the S3 fallback).
- Replay rejection: `seq` must strictly increase per device.
- `shrooms init` / `join` / `status`, and the systemd unit.

**Also needed for a real-world test, and easy to under-scope:**

- **A real TUN device.** M0 used netstack precisely so it needed no privileges.
  The daemon needs `/dev/net/tun`, `CAP_NET_ADMIN`, and interface address/route
  setup.
- **An `advertise` config option.** A NATed box knows only its LAN addresses,
  which are useless to a remote peer — reflexive discovery is M2. So at M1 a
  publicly reachable node must be *told* its endpoint, or its announce carries
  nothing dialable.
- **Persist the sequence number.** A device that restarts and resets `seq` to 1
  is rejected by every peer's `ReplayGuard` until they forget it.

**Done when:** you can move a box to a new IP and the other side reconnects with
no config change, and `shrooms status` shows an accurate roster.

**~4–6 days.** ✅ **PASSED (2026-08-06)** — see below.

#### M1 verified on real infrastructure (2026-08-07)

A laptop behind residential NAT and a Hetzner VPS, discovering each other over
the public logos.dev fleet:

```
NAME  OVERLAY IP                             ANNOUNCE  TUNNEL  ENDPOINT              RX/TX
vps   fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf  online    up      128.140.55.128:51820  156/212

4 packets transmitted, 4 received, 0% packet loss
rtt min/avg/max/mdev = 31.883/63.188/94.992/25.589 ms
```

Direct endpoint, not relayed. Reflexive discovery gave the laptop its public
address (`178.213.45.235:51820`) with no STUN server, and a single reflexive
address confirmed the NAT is endpoint-independent.

**This found a bug the container tests could not.** See the peer-churn entry
below: `replace_peers=true` on every sync recreated peers constantly, and a
handshake initiation arriving mid-recreation is rejected as invalid. At 1 ms
container RTT the handshake always won the race; at 100-450 ms it always lost.

Two for two on the emulated network hiding real failures — the other being
`MASQUERADE` not being endpoint-independent in M2. Prefer real infrastructure
for anything timing- or NAT-dependent.

#### M1 result (containers)

Two containerised nodes, each in its own network namespace, discover each other
over the public logos.dev fleet and bring up a WireGuard tunnel. Run with
`make m1` (needs docker and `/dev/net/tun`).

```
    discovered peer after 18s
    handshake complete after 18s

NAME    OVERLAY IP                             ANNOUNCE  TUNNEL  ENDPOINT          RX/TX
node-b  fdae:ce35:48a3:3a6:edfa:3505:829b:173  online    up      172.22.0.3:51820  556/612

3 packets transmitted, 3 received, 0% packet loss
rtt min/avg/max/mdev = 0.498/0.841/1.058/0.245 ms
M1 PASS: discovery over logos.dev, tunnel up, overlay ping works
```

Observed discovery 18–20 s from cold start (most of which is the Waku node
connecting to the fleet); handshake 0–35 s after that.

**Three things this shook out:**

1. **`liblogosdelivery` dlopens `libpq` at runtime** for the Store backend, and
   the failure is fatal but only visible at startup. Ship *every* library
   Basecamp installs alongside it, not just `liblogosdelivery.so` + `librln.so`.
2. **Discovery completes well before the WireGuard handshake.** A test that
   pings as soon as the peer appears in the roster fails in a way that looks
   like a routing bug. `status` now separates ANNOUNCE (gossip) from TUNNEL
   (handshake) so the distinction is visible — which is what the milestone
   wanted anyway.
3. **Don't grep pretty-printed JSON.** `status --json` emits `"online": true`
   with a space; a `grep '"online":true'` never matches and the harness reports
   a system failure that isn't one. Parse it.

#### What M1 can and cannot do in the real world

| Path | At M1 |
|---|---|
| home ↔ VPS (VPS publicly reachable) | ✅ works — home dials out, WireGuard relearns home's endpoint from the first authenticated packet |
| two boxes on the same LAN | ✅ works — both announce usable local addresses |
| any pair where one side is reachable | ✅ works |
| **home ↔ office, both behind NAT** | ❌ needs M2 (punching) or M3 (relay) |

So M1 is genuinely testable over the internet, but the result is **hub-and-spoke
through the VPS, not a mesh**. Direct site-to-site is what M2 and M3 buy.

### M2 — NAT traversal

Direct connections without a public endpoint on either side. **The hardest
milestone.**

- Candidate gathering: local v4 + v6, plus observed addresses.
- **In-band reflexive echo** — every control packet echoes back the observed
  `AddrPort`.
- Spray-and-race: WireGuard initiations to all candidates, Nebula's backoff
  (100…1000 ms over 5.5 s, then 5 s × 30 s, then 30 s).
- `PunchRequest` publish/subscribe, deduped by nonce, fire-and-forget.
- **Always reply to the observed source, never an announced address.**
- Optional: UPnP/NAT-PMP/PCP mapping of the WireGuard port, advisory only.
- `shrooms paths <name>` to make the racing legible.

**Test deliberately.** Home ↔ office may both sit behind cooperative NATs and
prove nothing. Get a hard case: a laptop tethered to a phone hotspot gives you
cellular CGNAT, where ~40% of mappings are symmetric.

**Done when:** two nodes behind separate NATs establish a direct tunnel and you
can see which candidate won.

**~1–2 weeks.** ⚠️ **PARTIAL** — see below.

#### M2 status (2026-08-06)

Implemented and unit-tested: `internal/disco` (encrypted probes, path prober),
wired into the mesh loop, `shrooms paths` for diagnosis. `make m2` runs the
NAT harness.

**Proven end to end, over the real logos.dev fleet:**

- **Reflexive discovery works.** A node behind NAT learns its own public address
  from a peer's pong, with no STUN server. Observed directly in the announce
  stream:
  ```
  seq=1  endpoints=[10.91.0.100:51820]                    ← LAN only
  seq=2  endpoints=[10.90.0.20:51820 10.91.0.100:51820]   ← learned its NAT address
  ```
- **Discovery works through NAT.** All three nodes found each other and
  exchanged candidates.
- **A node behind NAT reaches a public node directly** — `node-pub` path
  confirmed, tunnel up, traffic flowing.

**Not proven: the punch itself.** Two nodes behind separate NATs never
established a direct path. The blocker is the *harness*, not obviously the
code:

> **Plain Linux `MASQUERADE` is not endpoint-independent.** Measured here: it
> allocates a different external port per destination, i.e. it behaves as
> symmetric NAT. A traversal harness built on bare MASQUERADE therefore tests
> the *hard* case while claiming to test the easy one.
>
> ```
> -s 10.90.0.20 --sport 51820 --dport 51820  →  0 packets
> -s 10.90.0.20            --dport 51820     →  8 packets
> ```
>
> Forcing endpoint-independent mapping with a fixed-port `SNAT` changes the
> behaviour but did not produce a punch either; packets stopped arriving at the
> far gateway entirely, which suggests a NAT-allocation conflict rather than a
> protocol failure.

**Recommendation: settle this on real infrastructure rather than emulation.**
Real home and VPS NATs are genuine; the emulation is a proxy that is proving
harder to get right than the thing it proxies. Step 3 of the plan (one public
VPS, one NATed VPS, plus a laptop) tests the same property without the
emulation in the way — and `shrooms paths` already reports whether the NAT is
endpoint-independent, which is the diagnostic that matters:
```
reflexive addresses (as peers observe us):
  203.0.113.4:41001
  203.0.113.4:41002
  note: 2 distinct addresses suggests endpoint-dependent NAT,
        where hole punching fails and a relay is needed.
```

#### Why this is harder than a missing test rig (2026-08-11)

Still unproven, and three things learned since say why it is not simply a
matter of finding a second NATed machine.

**Reflexive discovery needs a peer outside your NAT.** A node learns its public
address only because a peer's pong echoes the source address it observed. On a
mesh whose members are all behind the *same* NAT, no peer is outside it, so no
node can learn its public address at all and `advertise` has to be set by hand.
The property the design leans on is not "some peer is reachable" but "some peer
is on the other side of your NAT".

**Carrier-grade NAT cannot be punched or port-mapped.** Mobile data is the case
that most needs traversal and the one case where no amount of correct code
helps. It is a relay case, permanently.

**A relay only works if both ends pick the same one, and only online peers are
candidates** ([ADR-014](docs/adr/014-relay-discovery-via-announce.md)). So a
node whose rendezvous plane has failed silently stops being seen as online,
stops being selected, and stops relaying — without any component reporting an
error. That failure is what the M4 watchdogs are for.

### M3 — VPS relay, auto-discovered

Connectivity when punching fails, with nothing hardcoded.

- `shrooms relay` mode: DERP-style UDP reflector keyed by 32-byte pubkey,
  HMAC-authenticated header. ~200 lines.
- ~~VPS publishes signed `RelayAnnounce` to a Store-backed topic every 30–60 s,
  fetched via `waku_store_query` on start.~~ **Built differently:** a relay is
  just a peer willing to forward, so it is a `relay` boolean on the ordinary
  announce. No second topic, no second message type, no Store dependency — and
  relays inherit endpoint validation and path probing unchanged.
- Selection is deterministic across nodes (lowest device ID among online,
  probe-confirmed relays), because both ends must choose the *same* relay or
  their traffic never meets. See DESIGN §8.
- Relay path established **concurrently** with punching, not after it fails, so
  traffic flows within one RTT.
- Path preference: direct beats relayed; re-race every ~60 s.

**Done when:** firewall-block direct UDP between two nodes, traffic keeps flowing
via the relay, and it recovers to direct when unblocked — with `status` showing
the transition.

**~4–6 days.** ✅ **PASSED (2026-08-07)** — `make m3`

Two nodes behind separate NAT gateways, neither reachable from the other, with a
public node relaying. Neither NATed node is told the relay exists; both learn it
from its announce:

```
    node-a discovered node-b after 10s
    node-a <-> node-b handshake after 15s

NAME              OVERLAY IP                               ANNOUNCE  TUNNEL  ENDPOINT
node-b            fdf7:132b:4213:aeb2:d147:39f:c912:f4d1   online    up      relay:fa398866…@10.90.0.10:51820
node-pub (relay)  fdf7:132b:4213:68b8:…                    online    up      10.90.0.10:51820

3 packets transmitted, 3 received, 0% packet loss
```

This is the property that makes the mesh usable: any pair connects regardless of
NAT, whether or not punching works. It also sidesteps the M2 harness problem
entirely, since the relay path does not depend on endpoint-independent mapping.

**Two bugs this container run surfaced**, both invisible until the numbers were
read carefully:

*The harness was lying about its own timings.* `$SECONDS` is measured from
process start, so the reported connect time silently included the docker build —
a 5 s connect printed as 350 s, and looked like a regression from the relay
work. The earlier "19 s" figure was measured the same way and was equally
meaningless. Now measured from the start of the wait loop.

*Paths were only re-probed after they had already expired.* `probeAll` skipped
any peer with a fresh path, so nothing refreshed it until it went stale at
`PathFresh` — leaving a window, every 15 s, where a peer had no usable path at
all until the next pong returned. It showed up as a node logging `no relay
available` and re-acquiring the relay three seconds later. In a container that
gap costs ~0; across a real link it is a full round trip, on a node that had a
perfectly good path the whole time. Paths now refresh at `PathRefresh` (5 s),
while still good. Connect time went from 21 s/107 s to 10 s/15 s.

The pattern is the recurring one: **containers hide anything timing-dependent.**
Both of these were sitting in a passing test.

**The bug worth remembering:** `SetRelayIdentity` was defined but never called,
so relay endpoints were built with a zero key, the MAC was wrong, and the relay
dropped every frame — with no error logged anywhere. It presented as a network
failure. `ParseEndpoint` now returns an error instead of building an endpoint
that cannot work, with a regression test.

#### M3 on real infrastructure ✅ (2026-08-11)

A phone on 5G — carrier-grade NAT, which cannot be punched or port-mapped —
reached a peer through a relay running on a public VPS, over the real internet.
The endpoint appears in `shrooms status` as `relay:<key>@<vps-ip>:51820`, so the
path is visibly relayed rather than inferred. Neither end was told the relay
existed; both picked it out of the roster.

This closes the last "containers only" item on the relay, and it is the case the
design was for: a pair that can never punch still connects. Relay selection is
per mesh, so a node on several meshes may relay through a different node on each.

### M4 — Make it seamless

The difference between "works" and "daily driver". This is the milestone that
earns the project.

- Survive: link down/up, IP change, DHCP lease change, laptop suspend/resume,
  VPS reboot, logos.dev being briefly unreachable.
- Reconnect with sane backoff — and **cap it**. nwaku's peer backoff reaches 8.5 h
  after 5 failures; reset the peer store on network change.
- Never leave a node retry-looping: nwaku's `online_monitor` resolves
  `one.one.one.one` every 5 s at zero peers. Stop the node instead.
- Handle Waku being down entirely: existing tunnels must keep working, since
  Waku is only rendezvous (DESIGN §2).
- DNS for overlay names (`office-box.mesh`) — hosts-file sync is fine to start.
- Useful logs and `shrooms status` accuracy under every failure above.

**Done when:** you stop thinking about it for a week.

**~1 week, spread over real use.** 🟨 **PARTIAL** — see below.

#### M4 status (2026-08-11)

Six nodes run this daily, which is the only way most of the list above gets
exercised. DNS for overlay names is done and then some (M6). What has been
built deliberately is the recovery from one failure the list did not anticipate.

**The rendezvous plane can die while the tunnels keep working.** WireGuard has
no dependency on Waku once a tunnel is up, so a daemon that has stopped
receiving announces looks healthy from every direction: traffic flows, the
interface is up, nothing logs an error. Meanwhile the roster ages out, every
peer is judged offline, and — because a relay is only ever selected from peers
believed online (M2 above) — relaying quietly stops.

Two watchdogs, both of which exit and let systemd restart the daemon rather than
attempt repair in place:

- **Nothing arriving for 10 minutes** on the rendezvous plane.
- **Deaf for 12 minutes to a specific peer** whose WireGuard tunnel is still
  rekeying. The rekey proves that peer's daemon is alive and therefore
  announcing, so silence from it is our fault, not its absence. This catches the
  case the first watchdog misses, where some publishers still get through.

The Android app does the same, and additionally kills its own process when the
delivery node reports `Disconnected`, because the library holds process-global
state and cannot be restarted in place.

Restarting is a blunt answer, and it is the honest one for now: the failure is
inside a library we do not control, and a restart demonstrably recovers it.

**Not measured:** roaming, DHCP lease change, suspend/resume, VPS reboot, and
logos.dev being briefly unreachable. Those are the original list and they are
still assumption.

### M6 — Name resolution

`shrooms hosts` ✅ writes `/etc/hosts` entries from the live roster, in a
marked block, atomically. With `manage_hosts = "true"` the daemon keeps it
current as the roster changes, so it is no longer stale between runs.

Still not enough on its own: it needs root, edits a system file other tools also
manage, and **does not exist on Android**, which is the platform this is
ultimately for.

**The DNS server is the real answer.** The daemon already holds the roster, so
it serves it over DNS on the overlay address, authoritative for the mesh domain
only. Every platform then has a supported hook — and Android's is the easiest of
the four, via `VpnService.Builder.addDnsServer()`.

See [ADR-013](docs/adr/013-name-resolution.md) for the full reasoning, including
why mDNS is the wrong shape for a point-to-point mesh.

**Done when:** `ssh vps.mesh` works on Linux and macOS without editing a file,
and a peer that joins becomes resolvable without anything being regenerated.

**~3–5 days**, most of it per-OS resolver wiring rather than the server. ✅
**PASSED (2026-08-11)** — see below.

#### M6 status (2026-08-11)

`internal/dns` serves the roster from the daemon, resolves names **across the
meshes a node belongs to**, and forwards everything outside the mesh domain. On
Android it is wired in through `VpnService.Builder.addDnsServer`, which is the
platform hook this section predicted would be the easiest of the four.

One thing the plan did not foresee: **an AAAA-only answer is not enough for a
browser.** On a v4-only underlay Chromium-family browsers send `A` and `HTTPS`
queries and no `AAAA` at all, so a correct empty answer reads to them as an
unusable name. The resolver now also answers `A` with a synthetic 198.18.0.0/15
address per peer, translated at the tun — see
[ADR-021](docs/adr/021-synthetic-ipv4.md). That, not the DNS server, is what
made names work in a browser.

### M5 — Credentials, enrolment, revocation

Replaces the bearer-token model. See DESIGN §7 for the rationale — the short
version is that `NK` currently conflates rendezvous, confidentiality and
*authorization*, and only the third needs to change.

**Split the network key.** `K_rdv` keeps deriving the topic, payload key and
per-pair PSKs. Authorization moves to an admin-signed credential verified against
`admin_pk`, which is a **public** value in config rather than a secret.

- `shrooms admin init` — generate `admin_k`, print the recovery backup. Keep it
  on one machine; it only signs at enrolment and renewal, so it can live offline.
- Credential: ~100 bytes of signed CBOR over `{device_pk, wg_pk, name,
  overlay_ip, not_before, not_after, caps}`, 7–30 day expiry, auto-renewed while
  the admin is reachable.
- `shrooms invite` — **one-time-use token, ~15 minute expiry.** This is the
  highest-value single change: the thing you copy/paste stops being a permanent
  credential, so a leaked clipboard or shell history is worth nothing.
- Redemption over Waku Noise Pairing (RFC 43): device generates its own keys,
  receives `K_rdv` + credential over the authenticated channel. It never receives
  another device's private key.
- Verify credentials at handshake; reject expired ones.
- Gossiped `Revocation{device_pk, serial, not_before, sig}`, monotonic serial,
  republished on epoch rotation. **Tear down live tunnels on receipt** — Nebula's
  documented gap is that its blocklist isn't distributed at all, and yours is.
- `shrooms revoke <name>`, `shrooms peers` showing credential expiry.

**Done when:** a new machine joins with a token that is dead 15 minutes later,
and a revoked device loses access within seconds while online, without touching
any other machine.

**~1 week.** ✅ **BUILT (2026-08-11)** — see below.

#### M5 status (2026-08-11)

`internal/cred` carries it: an Ed25519 admin key set, **fixed at mint** rather
than configurable afterwards, so the set of keys that can authorize membership
is decided once; credentials binding device key, WireGuard key and name;
30-day expiry; signing over a digest; and revocation lists gossiped over the
control plane. Driven by `shrooms admin init` / `issue` / `renew` / `revoke` /
`show`. See [ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md).

**Enrolment came out differently from the plan above.** Not Waku Noise Pairing:
a one-time invite token is 128 bits, and those bits derive a rendezvous topic
and a payload key of their own, so the exchange happens on a topic nobody
without the token can find. Ephemeral X25519 over it, padded to 1024 bytes, and
the invitee is issued its credential in the same exchange rather than in a
second step. See [ADR-017](docs/adr/017-invite-tokens.md).

**Renewal landed today.** A `KindGrant` control message that any member may
relay — so the admin does not have to reach each device, only the mesh — an
`admin renew` sweep, and credential expiry shown in `shrooms status`.

**Verified by unit tests only.** The renewal sweep has not been run against the
live mesh. Invite enrolment has been run end to end; the sweep has not.

**Not built, deliberately:** the per-recipient announce wrapping, for the
reasons in [ADR-020](docs/adr/020-membership-is-a-seam.md).

**Deferred:** pairwise rendezvous topics (DESIGN §7) eliminate the group secret
entirely at the cost of N² topics. Worth doing if metadata privacy on the public
bus becomes a priority; not needed to fix the bearer-key problem.

---

## 6. Minimum viable

**M0 + M1 + M3** gives a self-configuring mesh with relay fallback and no NAT
traversal — roughly 2–3 weeks. Add M2 to make it fast, M4 to make it yours.

---

## 7. Repo layout

```
cmd/
  shrooms/        single binary: daemon, CLI, relay mode
internal/
  wg/             wireguard-go embedding, conn.Bind demux
  waku/           cgo bindings to liblogosdelivery
  control/        message types, signing, AEAD, replay guard
  discovery/      announce, punch, candidate gathering, roster
  nat/            upnp/pmp/pcp, reflexive echo
  identity/       key derivation, addressing, credentials
  relay/          reflector, both ends
  api/            unix-socket JSON API for the CLI
proto/            control message definitions
packaging/        systemd unit, config example
```

One binary with subcommands keeps the install story to "drop a file in
`/usr/bin`".

### Dependencies

`golang.zx2c4.com/wireguard` · `liblogosdelivery` (cgo) ·
`github.com/huin/goupnp` · `github.com/jackpal/go-nat-pmp` ·
`golang.org/x/crypto` · `google.golang.org/protobuf` or `fxamacker/cbor`

Not needed: `pion/*`, `go-libp2p`, anything RLN/zerokit.

---

## 8. Android, built ✅

The phone is a full mesh participant, not a client of one. It runs **the same Go
core** as the Linux daemon, exposed through gomobile rather than hand-written
JNI — see [ADR-016](docs/adr/016-android-reuses-the-go-core.md). It joins by
invite like any other device, carries **several meshes at once** through one
`VpnService` using the same demultiplexer the daemon uses, auto-connects on
launch, restarts itself after boot and after its own update, and draws the mesh
as a live graph. Distributed from a self-hosted F-Droid repo, currently
versionCode 16/17.

**The four rules below are why the port was cheap, and they held:**

- **Everything in `internal/` must be platform-neutral.** No `netlink`, no
  `/proc`, no systemd assumptions outside `cmd/` and `packaging/`.
- **Userspace WireGuard from day one** (M0) — this is the main reason.
- **Keep control messages small and idempotent** — the Android path will deliver
  them late, out of order, and sometimes not at all.
- **Don't assume a persistent Waku connection anywhere in `discovery/`.** Linux
  will have one; Android cannot (DESIGN §6). Model announce/punch as periodic
  idempotent operations, not as a session.

Two things this section expected turned out not to be needed. The Nim→Go→JNI
build chain became a gomobile `.aar`, and the mix-publishing question was
settled by decision rather than by spike
([ADR-011](docs/adr/011-no-mixnet.md)). S2 was never run and the app ships
anyway.

What Android needed that Linux did not: **synthetic IPv4**, because a browser on
a v4-only network will not resolve an AAAA-only name (M6,
[ADR-021](docs/adr/021-synthetic-ipv4.md)); and a **self-kill** when the
delivery node reports `Disconnected`, because the library holds process-global
state and cannot be restarted in place (M4). Mobile data is carrier-grade NAT,
so the phone is a permanent relay case — it cannot punch and it cannot map a
port, which is precisely the pair M3 now proves works.

---

## 9. What to measure

Instrument from M1 rather than retrofitting:

- Cold start → first established tunnel.
- Direct vs relayed pairs over time.
- Which candidate won, how many were sprayed.
- **Waku publish→receive latency on cluster 2** — DESIGN §4's ~240 ms is derived
  arithmetic, not measurement. Replace it with real numbers.
- Reconnect time after each M4 failure mode.
