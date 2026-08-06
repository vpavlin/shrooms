# logos-vpn — Prototype plan (Linux first)

What to build, in what order, and what to prove before building it.

See [DESIGN.md](DESIGN.md) for the architecture and its justification.

**Scope: Linux only.** Android comes after Linux works seamlessly. See §8.

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
# /etc/logos-vpn/config.toml
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
first start in `/var/lib/logos-vpn/`, never transmitted. Its public key is the
device's identity and the overlay address is derived from it — no allocation, no
registration, no IPAM.

The **name** is self-asserted inside the signed announce. It authenticates as
"the device holding key X calls itself home-nas", which is sufficient for a
personal mesh. No uniqueness enforcement in v1.

### Flow

```
# first machine
$ logos-vpn init --name home-nas
Network key: K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5
  copy this to your other machines — it is the only secret
Device:      home-nas
Overlay IP:  fd3a:9c21:7e04::8f21:aa03
Wrote /etc/logos-vpn/config.toml

$ systemctl enable --now logos-vpn

# every other machine
$ logos-vpn join K7QF3M2XVBNP8SDLR4WYZC6HAJT9EUG5 --name office-box
$ systemctl enable --now logos-vpn
```

### CLI

```
logos-vpn init [--name N]        generate identity + a new network
logos-vpn join <KEY> [--name N]  generate identity, join an existing network
logos-vpn status [--json]        roster + tunnel state (see §3)
logos-vpn peers                  roster from gossip, including offline devices
logos-vpn paths <name>           candidates gathered, which won, and why
logos-vpn ping <name>            overlay reachability
logos-vpn key show               print the network key (for adding a machine)
logos-vpn key rotate             v1 revocation: new NK, re-enrol everyone
```

The daemon exposes a unix socket (`/run/logos-vpn.sock`) with a small JSON API;
the CLI is a thin client over it. That also gives you a monitoring hook for free.

### systemd

```ini
[Unit]
Description=logos-vpn overlay mesh
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/logos-vpn daemon
AmbientCapabilities=CAP_NET_ADMIN
StateDirectory=logos-vpn
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
$ logos-vpn status
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

- Embed `wireguard-go` in `logos-vpn daemon`.
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
- `logos-vpn init` / `join` / `status`, and the systemd unit.

**Done when:** you can move a box to a new IP and the other side reconnects with
no config change, and `logos-vpn status` shows an accurate roster.

**~4–6 days.**

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
- `logos-vpn paths <name>` to make the racing legible.

**Test deliberately.** Home ↔ office may both sit behind cooperative NATs and
prove nothing. Get a hard case: a laptop tethered to a phone hotspot gives you
cellular CGNAT, where ~40% of mappings are symmetric.

**Done when:** two nodes behind separate NATs establish a direct tunnel and you
can see which candidate won.

**~1–2 weeks.**

### M3 — VPS relay, auto-discovered

Connectivity when punching fails, with nothing hardcoded.

- `logos-vpn relay` mode: DERP-style UDP reflector keyed by 32-byte pubkey,
  HMAC-authenticated header. ~200 lines.
- VPS publishes signed `RelayAnnounce` to a Store-backed topic every 30–60 s.
- Devices fetch it via `waku_store_query` on start, not by waiting for a live
  publish.
- Relay path established **concurrently** with punching, not after it fails, so
  traffic flows within one RTT.
- Path preference: direct beats relayed; re-race every ~60 s.

**Done when:** firewall-block direct UDP between two nodes, traffic keeps flowing
via the relay, and it recovers to direct when unblocked — with `status` showing
the transition.

**~4–6 days.**

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
- Useful logs and `logos-vpn status` accuracy under every failure above.

**Done when:** you stop thinking about it for a week.

**~1 week, spread over real use.**

### M5 — Credentials, enrolment, revocation

Replaces the bearer-token model. See DESIGN §7 for the rationale — the short
version is that `NK` currently conflates rendezvous, confidentiality and
*authorization*, and only the third needs to change.

**Split the network key.** `K_rdv` keeps deriving the topic, payload key and
per-pair PSKs. Authorization moves to an admin-signed credential verified against
`admin_pk`, which is a **public** value in config rather than a secret.

- `logos-vpn admin init` — generate `admin_k`, print the recovery backup. Keep it
  on one machine; it only signs at enrolment and renewal, so it can live offline.
- Credential: ~100 bytes of signed CBOR over `{device_pk, wg_pk, name,
  overlay_ip, not_before, not_after, caps}`, 7–30 day expiry, auto-renewed while
  the admin is reachable.
- `logos-vpn invite` — **one-time-use token, ~15 minute expiry.** This is the
  highest-value single change: the thing you copy/paste stops being a permanent
  credential, so a leaked clipboard or shell history is worth nothing.
- Redemption over Waku Noise Pairing (RFC 43): device generates its own keys,
  receives `K_rdv` + credential over the authenticated channel. It never receives
  another device's private key.
- Verify credentials at handshake; reject expired ones.
- Gossiped `Revocation{device_pk, serial, not_before, sig}`, monotonic serial,
  republished on epoch rotation. **Tear down live tunnels on receipt** — Nebula's
  documented gap is that its blocklist isn't distributed at all, and yours is.
- `logos-vpn revoke <name>`, `logos-vpn peers` showing credential expiry.

**Done when:** a new machine joins with a token that is dead 15 minutes later,
and a revoked device loses access within seconds while online, without touching
any other machine.

**~1 week.**

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
  logos-vpn/      single binary: daemon, CLI, relay mode
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

## 8. Android, later

Deferred, but keep these true so the port stays cheap:

- **Everything in `internal/` must be platform-neutral.** No `netlink`, no
  `/proc`, no systemd assumptions outside `cmd/` and `packaging/`.
- **Userspace WireGuard from day one** (M0) — this is the main reason.
- **Keep control messages small and idempotent** — the Android path will deliver
  them late, out of order, and sometimes not at all.
- **Don't assume a persistent Waku connection anywhere in `discovery/`.** Linux
  will have one; Android cannot (DESIGN §6). Model announce/punch as periodic
  idempotent operations, not as a session.

When you pick Android up, the remaining work is the two deferred spikes (mix
publishing from Edge mode, and the Nim→Go→JNI build chain), the intermittency
scheduler, and the VpnService app. DESIGN §6 has the landmines.

---

## 9. What to measure

Instrument from M1 rather than retrofitting:

- Cold start → first established tunnel.
- Direct vs relayed pairs over time.
- Which candidate won, how many were sprayed.
- **Waku publish→receive latency on cluster 2** — DESIGN §4's ~240 ms is derived
  arithmetic, not measurement. Replace it with real numbers.
- Reconnect time after each M4 failure mode.
