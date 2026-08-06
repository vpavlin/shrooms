# logos-vpn — Design

An overlay mesh VPN between personal devices (home, office, laptops, Android phones),
using Logos Messaging (Waku) as a rendezvous substrate rather than a central
coordination server.

Status: **design, pre-implementation.** Every claim here is traceable to research
recorded in §11. Anything unverified is marked.

---

## 1. Goal and constraints

Connect ~5–10 personal devices into a private mesh with no coordination server.

| Constraint | Source | Consequence |
|---|---|---|
| Android phones are full mesh participants | requirement | iOS out of scope entirely |
| No self-run Waku cluster | requirement | use the public `logos.dev` fleet (cluster 2) |
| A small VPS is acceptable, but auto-discovered | requirement | no hardcoded relay IPs; VPS announces itself in-band |
| Daily driver, not a demo | requirement | reliability and roaming beat novelty |

All peers are mutually trusted and owned by one person. The adversary is a network
observer or opportunistic scanner — **not** a global passive adversary. Design
accordingly; see §7.

---

## 2. Architecture

```
   ┌──────────────┐         ┌──────────────┐         ┌──────────────┐
   │  home box    │         │  office box  │         │   Android    │
   │  Linux/Core  │         │  Linux/Core  │         │  VpnService  │
   └──────┬───────┘         └──────┬───────┘         └──────┬───────┘
          │                        │                        │
          │  ══════ WireGuard, direct where possible ══════ │
          │                        │                        │
          └────────────┬───────────┴───────────┬────────────┘
                       │                       │
                 ┌─────┴──────┐         ┌──────┴─────────┐
                 │    VPS     │         │  logos.dev     │
                 │  relay +   │         │  Waku cluster 2│
                 │  echo +    │◀───────▶│  (rendezvous   │
                 │  Waku Core │ announce│   only)        │
                 └────────────┘         └────────────────┘
```

Three planes, deliberately separated:

- **Data plane** — userspace WireGuard, direct peer-to-peer where NAT allows,
  relayed via the VPS otherwise. Carries all real traffic.
- **Rendezvous plane** — Waku pub/sub, used *intermittently*: cold start, network
  change, partition repair, enrolment, revocation.
- **Steady-state control** — gossip over the WireGuard tunnels themselves, once
  any tunnel is up. Waku is not involved.

### The key insight

**Waku is a bootstrap and repair channel, not a live control plane.**

Once tunnels exist they are self-sustaining, because WireGuard relearns a peer's
endpoint from any correctly-authenticated packet. A phone that roams and *sends
first* is relearned by every peer with zero signalling. The mesh only needs
out-of-band rendezvous when *no* tunnel is up.

This is the wesher/tinc pattern, and it is what makes the Android story viable
(§6).

---

## 3. Data plane

**Userspace WireGuard (wireguard-go), not kernel.**

Two independent reasons:

1. **Socket sharing.** NAT-traversal logic and the tunnel must share one UDP
   socket, or the reflexive address you discover is not the port your data
   arrives on. Kernel WireGuard owns its socket and will not share. This is why
   Tailscale runs wireguard-go everywhere despite the kernel module existing.
2. **Android has no kernel WireGuard without root.** Since Android forces
   userspace anyway, kernel WG would be a Linux-only special case, not an
   architecture.

Kernel WireGuard may later be an opportunistic fast path on Linux nodes with a
stable public endpoint. Not in v1.

### Why not tunnel over libp2p

Measured: EdgeVPN over libp2p streams reaches **~30 Mb/s over a real WAN against
WireGuard's ~859 Mb/s** on identical hardware — 29×, at 200% CPU vs 1–2%. libp2p
maintainers put IP-over-libp2p at 2–10× overhead (libp2p/specs#626, open since
Aug 2024, no implementation). Every existing libp2p VPN serialises all flows onto
one mutex-guarded ordered stream.

For calibration: a Raspberry Pi 4 does 777 Mb/s–1.02 Gb/s of WireGuard. Your WAN
is the bottleneck, not the crypto.

### Socket demultiplexing

One UDP socket carries WireGuard, control packets, and optionally STUN.
Tailscale's discriminators (verified in `magicsock.go`):

| Protocol | Discriminator |
|---|---|
| WireGuard | `msg[0] ∈ 0x01..0x04` and `msg[1:4] == 0x000000` |
| Tailscale disco | `msg[0] == 0x54` (magic `"TS💬"`) |
| STUN binding | `stun.Is(msg)` and `msg[1] == 0x01` |

**Pick a magic prefix with first byte > `0x04` and ≠ `0x54`.** All three separate
cleanly on the first two bytes.

Implement as a `conn.Bind` wrapper following **NetBird's `ICEBind`**
(`client/iface/bind/ice_bind.go`), which filters control packets out while
preserving `ReadBatch` / `SplitCoalescedMessages` / GSO offload. Do **not** copy
go-libp2p's `SharedNonQUICPacketConn`, which routes non-QUIC packets through a
32-deep Go channel with no batching.

### MTU

WireGuard overhead is 60 B over IPv4, 80 B over IPv6. Default tunnel MTU **1420**
(survives an IPv6 underlay). Clamp MSS on any node that routes for others —
PMTUD blackholes are the most common cause of "WireGuard hangs on large
transfers".

---

## 4. Rendezvous plane (Waku)

### Network

**`logos.dev` fleet, cluster 2.** From `networks_config.nim`:

```
clusterId: 2, shards: AutoSharding × 8, maxMsgSize: 150 KiB
rlnRelay: false          ← no RLN, no memberships, no proving cost
mix: true                ← see the risk in §9
enableKadDiscovery: true, discv5Discovery: true, p2pReliability: true
entryNodes: 6 × /dns4/delivery-NN.<region>.logos.dev.status.im/tcp/30303/p2p/...
```

`rlnRelay: false` deletes an entire problem class: no per-device memberships
(RLN's Shamir sharing leaks the key if one membership publishes twice per epoch),
no ~$5/6mo/device cost, and no ZK proof generation on phones. It also removes
223–258 ms of publish latency.

**Bootstrap anchor cost is zero.** The six `entryNodes` are `/dns4/` multiaddrs
hardcoded in the library — DNS names, not IPs. Nothing to run, nothing to pin.

Trade-off accepted: logos.dev is a **dev fleet with no SLA**. It may be reset or
reconfigured. See §9.

### Topics

Rotating rendezvous topic derived from the mesh key:

```
epoch        = floor(unix_time / 3600)
tag          = HMAC-SHA256(K_topic, "mesh/v1/rendezvous" || be64(epoch))[0:16]
contentTopic = "/<generic-app>/1/" || base32(tag) || "/proto"
```

This works because autosharding computes `shard = sha256(application ‖ version)
mod numShards` — **the `{name}` field is not hashed.** Rotating it changes the
content topic while keeping the shard, and therefore the gossipsub mesh, stable.
No SUBSCRIBE/UNSUBSCRIBE churn announces the rotation.

*Confirmed independently by two research passes reading the nwaku source. Still
worth testing against a live node — see nwaku#2538, "autosharding resolves
content topics to wrong shard".*

Subscribers accept `epoch-1`, `epoch`, `epoch+1` to absorb clock skew.

### Messages

All payloads are XChaCha20-Poly1305 under a per-epoch key derived from the mesh
key, padded to a fixed size, with the Ed25519 signature **inside** the ciphertext
(Waku relay uses `StrictNoSign` at the libp2p layer to preserve what weak sender
anonymity exists; don't undo that).

| Message | Topic | Ephemeral | Cadence |
|---|---|---|---|
| `EndpointAnnounce{peer_pk, candidates[], wg_port, seq, ts, sig}` | `/vpn/1/endpoints/` | `false` (Store-backed) | 30–60 s, and on network change |
| `PunchRequest{from_pk, to_pk, candidates[], nonce, ts}` | `/vpn/1/punch/` | `true` | on demand, deduped, ≤1/s per peer |
| `RelayAnnounce{relay_pk, addrports[], seq, expiry, sig}` | `/vpn/1/relay/` | `false` (Store-backed) | 30–60 s from VPS |
| `Revocation{revoked_pk, serial, not_before, sig}` | `/vpn/1/revoke/` | `false` | on demand + on epoch rotation |

**Replay protection is mandatory.** A public bus means anyone can re-publish a
captured message they cannot decrypt — rolling your endpoint back to a stale IP,
or suppressing a revocation. Monotonic `seq` per device inside the signed
payload; reject anything not strictly greater. Monotonic serial per revocation.

### Store is load-bearing

Intermittent peers may never be online simultaneously, so Store is the async
mailbox — this is the single most important use of Waku in the design. New and
waking devices `waku_store_query` the relay and endpoint topics rather than
waiting for a live publish.

`autoStoreResume` clamps to `max(lastOnline, now - 6h)` and **silently truncates**
past that — no error. Fine for endpoints (stale ones are worthless), fatal if you
ever treat it as a log. Prefer explicit `startTime` over `autoStoreResume`.

### The roster is a free by-product

Because every device publishes a signed `EndpointAnnounce` on a shared topic,
**every node holds the full membership list** — pubkeys, claimed names, overlay
addresses, and last-seen times — without any node being authoritative. Composed
with local WireGuard state (last handshake, bytes, current endpoint) and your own
path racing (direct vs relayed, which candidate won), that is a genuine control
plane with no control server.

Reachability is inherently **per-observer**: node A may hold a direct tunnel to B
while C reaches B only via relay. A global topology view would require nodes to
gossip their own reachability over the mesh — cheap once the mesh carries its own
control traffic, but not needed for v1.

See PROTOTYPE §3 for the surfaced form.

### Measured latency to plan against

1000-node simulation, 2 KB message, **with** RLN: 497 ms avg, 597 ms p95.
Cluster 2 has no RLN, so subtract 223–258 ms → **~240 ms avg, ~360 ms p95**
(arithmetic on their component breakdown, not a measured figure — verify).

Real tail: a dozing Android phone may not see a message for **minutes**. This is
why every punch must be periodic and idempotent, never one-shot.

---

## 5. NAT traversal

### The model: notification, not conversation

> DCUtR and ICE both need a *conversation*; Nebula/ZeroTier/Tailscale need a
> *notification*. A pub/sub bus is a notification medium.

Ranked by tolerance of a high-latency, no-round-trip bus:

| Model | Tolerates 1–5 s pubsub? | Why |
|---|---|---|
| ZeroTier `RENDEZVOUS` | **yes** | spec: *"No OK or ERROR is generated"* |
| Nebula punchy | **yes** | 5.5 s spray window by construction |
| Tailscale disco | yes | sprays first, `CallMeMaybe` is advisory |
| libp2p DCUtR | **no** | needs an ordered duplex stream, ±one-way-latency sync |
| ICE (pion) | poorly | checks carry `MESSAGE-INTEGRITY` keyed on the peer's password — no probe can fly until OFFER→ANSWER completes *both* ways; every ICE restart repeats it |

**DCUtR is doubly unavailable:** the C FFI exposes no hole-punch control, and even
if it did, DCUtR punches a mapping for *libp2p's* socket, not WireGuard's.

### Algorithm

Per peer B, triggered by outbound traffic to B or by receiving a `PunchRequest`:

1. **Gather candidates** — local interface addresses (v4 **and v6**), any
   UPnP/PMP/PCP mapping, reflexive address from the VPS echo, and every
   `AddrPort` a packet from B has ever been *observed* arriving from.
2. **Spray immediately.** WireGuard handshake initiations to *all* of B's
   candidates. Nebula's linear backoff: 100, 200, 300 … 1000 ms for ~5.5 s, then
   every 5 s for ~30 s, then every 30 s.
3. **In parallel**, publish `PunchRequest`. Never wait for it.
4. **On receiving a `PunchRequest`** naming you: spray your own initiations at all
   listed candidates. No delay needed.
5. **Always reply to the observed source, never to an announced address.** Under
   endpoint-dependent mapping, spraying at 4 candidates creates 4 *different*
   external ports. Getting this wrong silently kills the symmetric-NAT case that
   would otherwise have worked.
6. First candidate to complete a handshake wins. Optionally keep racing with
   Tailscale's `betterAddr()` scoring (+50 loopback, +30 link-local, +20 private,
   +10 IPv6, 1% hysteresis).
7. **Concurrently** — not as a fallback — establish the relay path, so traffic
   flows within one RTT while punching proceeds. (Nebula's `StartRelays()`.)

The spray window (5.5 s dense, decaying to ~30 s periodic) sits inside a NAT
mapping lifetime of 35–65 s. Signalling delay anywhere from 0 to ~30 s still
lands in a live window. Minutes of delay just means the punch happens on the next
cycle.

### Reflexive address discovery: in-band echo

Every control packet echoes back the `AddrPort` the sender was observed at.
Tailscale does exactly this — its `Pong` carries `Src`, and the source comment
calls it *"effectively a STUN response"*. ZeroTier does it in `HELLO`/`OK`.

Better than public STUN here because: every peer is a STUN server for every other
(N−1 vantage points), it's authenticated (inside the AEAD), and it observes the
mapping *actually in use* for that peer — which under endpoint-dependent mapping
is the only correct answer.

Needs one always-reachable anchor for first contact between two symmetric-NAT
peers. That's the VPS.

### Port mapping

Attempt UPnP-IGD, NAT-PMP and PCP opportunistically at startup and on every
network change, in parallel, ~2 s timeout. **Map the WireGuard port specifically**
— a mapping for the Waku socket is worthless. Treat all results as advisory;
routers lie and mappings expire unannounced. Never gate anything on success.

Availability is ~40% at best (Netalyzr 2016, technical-user-biased) and trending
down since CallStranger (CVE-2020-12695). Never helps on cellular.

### What to expect

| Path | Expected |
|---|---|
| home ↔ office, European fixed-line | ~90%+ direct, likely IPv6-direct |
| any ↔ Android on home Wi-Fi | trivially direct |
| any ↔ Android on cellular | **60–70% direct**, rest relayed |

Cellular NAT is bimodal: ~40% symmetric, ~20% full cone. **Budget the relay as a
normal operating condition, not an exception.**

IPv6 is the real escape hatch — ~47–51% global adoption, and with global IPv6 on
both ends there is no NAT at all, only filtering, which one outbound packet
solves. Gather and race IPv6 candidates first.

---

## 6. Android

### Intermittency is forced, not chosen

Both nwaku and go-waku contain:

```nim
let sleepDetectionInterval = 3 * randomPeersKeepalive   # ~30 s default
if currentTime - lastTimeExecuted > sleepDetectionInterval:
  warn "Keep alive hasn't been executed recently. Killing all connections"
  await node.peerManager.disconnectAllPeers()
```

Doze suspends the SoC, timers don't fire, so **every Doze cycle tears down every
Waku connection.** The assumption is correct for servers and self-fulfilling on a
phone. Persistent connection is not an available behaviour.

### Core mode is incoherent on a phone

Not merely expensive. Gossipsub is a *maintained* overlay: GRAFT to `dLow=4`
needs 4–6 s of heartbeats; the mcache holds ~6 s of history (`seenTTL` 2 min,
`historyLength` 6); `pruneBackoff` and mesh-failure scoring exist to punish
graft-then-vanish. A node appearing for 3 s every 15 minutes never forms a mesh,
is a liability to its peers, and gains nothing — it must get history from Store,
which is a client protocol.

**Intermittency forces the Edge protocol set regardless of the flag.**

Corroborating: Status ships a user-toggleable LightClient/Edge switch on Android,
and passes radio type (Wi-Fi vs cellular) into their filter manager.

### Duty cycle

Wake Waku on: VPN start · `ConnectivityManager.NetworkCallback` transitions ·
"no WireGuard handshake with any peer for >3 min" · app foreground · a ~30–60 min
floor alarm.

On wake: dial the static peer (1–3 s), store-query the control topics with an
explicit `startTime`, publish current endpoint, then **disconnect**.

Prefer `NetworkCallback` over timers — those transitions are exactly the moments
the control plane has new information, they aren't subject to alarm quotas, and
the radio is already awake so the RRC promotion is free.

### Battery

| | 4G | Confidence |
|---|---|---|
| WireGuard data plane alone | **+0.5 pp/h** (~12 pp/day cellular) | measured |
| Core, stock intervals | +2 to +4 pp/h | extrapolated |
| Edge, stock intervals | +1.5 to +3 pp/h | extrapolated |
| **Edge, intermittent, static peers** | **+0.01 to +0.2 pp/h** | extrapolated |

The intermittent control plane costs 5–20% of the tunnel it serves. It stops
being a battery consideration.

**The default 10 s keepalive is the pessimal LTE interval** — USENIX Security '23
measured that power *drops* as interval grows except at Δt ≈ 10 s, where the
device reconnects exactly as the RRC tail expires. nwaku defaults to 10 s,
go-waku's mobile lib to 20 s, status-go to 5 s. Set 0 (intermittent) or ≥240 s.

### Required configuration

```
mode                 = Edge
discv5Discovery      = false   # nwaku's Edge does NOT disable this — a unit test
                               # asserts discovery stays on. status-go's Edge does.
dnsDiscovery         = off     # you have a static peer
staticnodes          = <VPS multiaddr>
keepAlive            = 0 or >= 240 s
rendezvous           = false   # 10 s lookup loop
```

### Android landmines

- **`online_monitor.nim` resolves `one.one.one.one` every 5 seconds** whenever
  peer count is 0, indefinitely. On cellular this is the worst battery bug you
  could ship. **Stop the node on failure; never let it retry-loop.**
- **Peer backoff blacklists your hub.** `InitialBackoffInSec=120`,
  `BackoffFactor=4`, 5 failures → 120·4⁴ ≈ **8.5 hours**. A Doze-induced failure
  can blacklist your only static peer for most of a day. Reset the peer store on
  VPN reconnect.
- **Foreground service type must be `systemExempted`.** `dataSync` is capped at
  6 h per 24 h on Android 14+ and cannot be started from `BOOT_COMPLETED`.
  (Note: WireGuard's own Android app declares no FGS type at all, relying on the
  VPN system slot. Simpler, and worth considering.)
- Always-on VPN (Android 7+) is the reliable way to survive Doze. With it you
  must disable your own disconnect UI and persist config across restarts.
- `protect(fd)` the outer socket or you get a routing loop.
  `setUnderlyingNetworks(null)` for seamless Wi-Fi↔LTE.

### Packaging

**Plain JNI into a `c-shared` `.so`, not gomobile.** This is what WireGuard's
Android app does:

```java
private static native int wgTurnOn(String ifName, int tunFd, String settings);
private static native int wgGetSocketV4(int handle);
```

Build wireguard-go and logos-delivery into **one** `.so` / one Go runtime, so
Waku can be handed a `protect()`ed socket directly.

---

## 7. Identity, keys, addressing

### Keys

Three, all independent:

| Key | Type | Role | Storage |
|---|---|---|---|
| `id_k` | Ed25519 | signs control messages, libp2p identity | TPM/Keystore, non-exportable if possible |
| `wg_k` | X25519 | WireGuard static key | disk (kernel/userspace needs the scalar) |
| `admin_k` | Ed25519 | signs device credentials and revocations | one device + offline backup |

**Do not reuse one key for identity and WireGuard.** The cryptography is probably
fine (Thormarker, eprint 2021/509, proves joint security for Ed25519 + an X25519
KEM without assuming domain separation — though nothing published covers
Noise_IKpsk2 specifically). The decisive argument is mundane: WireGuard needs the
raw scalar in memory, so reuse makes it impossible to keep the identity key
non-exportable in hardware. Cost of separation is 32 bytes in a message you're
already signing.

### Authorization is a separate job from rendezvous

A single shared network key can do three things at once, and conflating them is
the main weakness of the v1 bootstrap model:

| Job | v1 (bearer) | Target |
|---|---|---|
| Rendezvous — find each other on a public bus | `NK` | group secret `K_rdv`, rotatable |
| Confidentiality — hide payloads from observers | `NK` | derived from `K_rdv` |
| **Authorization — who is allowed** | `NK` | **admin-signed per-device credential** |

Only the third needs to change. Rendezvous genuinely requires a shared secret —
every member must independently compute the same topic with no coordination — but
that secret need not also grant membership.

The bearer model's five concrete failures: no per-device revocation; any device's
compromise is total; no expiry; the copy/paste artifact stays valid forever
(shell history, clipboard managers); and no per-device capabilities.

### Credentials

Nebula's model, minus the X.509. One Ed25519 admin key signs ~100 bytes of CBOR
per device:

```
credential = Sign(admin_k, {
    device_pk,                  // Ed25519, generated on-device, never leaves it
    wg_pk,                      // X25519
    name,
    overlay_ip,                 // derived, but pinned here so it is authenticated
    not_before, not_after,      // 7-30 days, auto-renewed
    caps                        // may-relay, may-route-subnet, ...
})
```

Devices present their credential in the handshake; peers verify against
`admin_pk` — a *public* value you configure, not a secret. This buys per-device
revocation without rotating anything, expiry as a backstop against a suppressed
revocation, and an offline admin key that only signs at enrolment and renewal.

**Learn from Nebula by inversion:** its blocklist is explicitly *not* distributed
via lighthouses — you push it yourself with Ansible. The gossip bus fixes that
for free, so make revocation tear down *live* tunnels rather than merely refusing
future handshakes.

### Derivation

```
K_topic = HKDF(NK, "mesh/v1/topic")
K_enc   = HKDF(NK, "mesh/v1/payload")
K_psk   = HKDF(NK, "mesh/v1/wgpsk")

wg_psk(A,B) = HKDF(K_psk, sort(pk_A, pk_B))      # per-pair WireGuard PresharedKey
```

The per-pair PSK gives two things: rotating `NK` invalidates every tunnel at the
WireGuard layer independent of your credential logic being correct, and you get
WireGuard's post-quantum hedge for free.

### Addressing — derived, no IPAM

```
ula_prefix = fd || SHA256("mesh/v1/ula" || NK)[0:5]        →  a /48
addr(dev)  = ula_prefix : 0000 : SHA256("mesh/v1/addr" || id_pk)[0:10]  →  /128
```

**This deletes IPAM from the control plane entirely** — no allocation messages, no
conflict resolution, no leases, no split-brain. `AllowedIPs` becomes
self-enforcing, since every node computes the mapping locally.

No vanity mining needed. cjdns brute-forces for `0xfc` and CGA does hash extension
only because someone else fixes their prefix; you derive your own. 80 bits is
ample because **the address is not an authenticator** — authentication comes from
WireGuard's handshake and the admin-signed credential.

Use `fd00::/8` (RFC 4193). Not `fc00::/8` (cjdns squats it, collides with real
ULA) and not `0200::/7` (Yggdrasil squats it).

### Enrolment

**The artifact you copy/paste must be one-time-use and short-lived.** This is the
single biggest practical improvement over a bearer key, and it holds even if you
keep a group secret. Tailscale auth keys and innernet invitations both work this
way.

`logos-vpn invite` on the admin machine emits a token valid ~15 minutes, single
redemption. The new device generates its own keys, redeems the token, and
receives `K_rdv` plus its signed credential over the resulting authenticated
channel. The token is dead afterwards, so a leaked clipboard is worth nothing.

**Transport: Waku Noise Pairing, RFC 43** — already shipping in js-waku and
go-waku. The QR carries a commitment but no secret (a photographed QR is inert),
XX-derived handshake, 8-digit confirmation code from the handshake hash as the
MITM defence. Serverless, and it delivers `K_rdv` and the credential in one shot.
On Linux-only, a typed token over the same handshake is equivalent; the QR earns
its keep once phones exist.

The new device generates its own keys and never receives another device's private
key (innernet's discipline, not Signal's).

### Future: pairwise rendezvous

The group secret can be eliminated entirely, Briar-style, by deriving a topic per
*pair* from their shared DH secret:

```
K_pair(A,B) = HKDF(DH(a_priv, b_pub), "mesh/v1/pair" || sort(pk_A, pk_B))
topic(A,B)  = HMAC(K_pair, epoch)
```

No group key exists to leak; revoking a device means others stop watching its
pairwise topics; compromising one pair reveals nothing about the rest of the mesh.

Cost is N² topics — at 10 devices, 45 pairs, so each device publishes 9 times per
cycle instead of once. At a 30–60 s cadence that is ~1.5–3 msg/s mesh-wide, which
is nothing on Waku, and **all pairwise topics stay on one shard** because
autosharding hashes only `application‖version`. The real cost is complexity in
`discovery/`, plus the requirement that a new device holds every peer's pubkey
before it can compute topics — which credential distribution already provides.

Ship credentials first. Reach for this if metadata privacy on a public bus
becomes a priority.

### Revocation

Four layers, all cheap, failing differently:

1. **Gossiped revocation** (seconds) — admin-signed, ~100 B, republished on epoch
   rotation and on join. Tear down the tunnel immediately, don't just refuse
   future handshakes.
2. **Short-expiry credentials** (7–30 days) — bounds damage from an attacker who
   *suppresses* the revocation. This is Nebula's model minus Nebula's flaw
   (its blocklist is explicitly not distributed via lighthouses).
3. **Per-pair PSK from `NK`** — rotating `NK` kills every tunnel at the WireGuard
   layer.
4. **`NK` rotation** (nuclear) — also rotates the topic derivation, so the thief
   loses the rendezvous itself.

Honest limitation, worth stating: no serverless scheme can revoke against an
adversary who already extracted the WireGuard private key and can reach a peer
that hasn't received the revocation. Full-disk encryption and non-exportable
identity keys matter more than any protocol change here.

### What not to build

- **Publisher anonymity on gossipsub.** Waku's own analysis concedes it's absent
  under multi-node adversaries, and USENIX Sec '25 demonstrated the general
  mechanism against Ethereum validators. More decisively: your anonymity set on a
  private topic is your own ten devices, so a perfect implementation would hide
  device A from device B.
- **X.509, CRLs, DIDs.** At N=10, a ~100-byte signed CBOR blob is your entire PKI.
- **Per-recipient ECIES.** N-fold amplification, no benefit among trusted peers.
- **Authority chains / multi-signer admin.** Tailnet Lock maintains an append-only
  chain of signed Authority Update Messages because Tailscale's coordination
  server is a party with independent interests that must not be able to inject
  devices. Your bus is dumb — it can drop, delay, replay and observe, but it
  cannot forge, because everything is signed. One admin key with an offline
  backup is correct here.
- **Web of trust / mutual attestation.** Removes the single point of compromise,
  but revocation becomes genuinely hard: you must convince every device
  independently. Wrong trade for a single-owner mesh.
- **Threshold / M-of-N admin.** You are one person.
- **CRDT/claim-based IP allocation.** Derived addressing already solved it.

---

## 8. Relay

A **DERP-style dumb UDP reflector** on the VPS, ~200 lines.

```
peer → relay:  [MAGIC][32B dest wg pubkey][wg packet]
relay:         map[pubkey]net.UDPAddr, refreshed from observed source
               (authenticated by a short HMAC over the header, to stop
                spoofed rebinding)
relay → peer:  forward verbatim
```

Routing key is the peer's public key. No allocation, no permission, no
credentials. **WireGuard has already encrypted and authenticated the payload, so
the relay needs zero cryptographic involvement** — it just must not be an open
reflector.

Prefer UDP over DERP's TCP/443 (which exists to survive hostile networks, at the
cost of TCP-over-TCP head-of-line blocking). Add a TCP/443 mode later if needed.

Rejected alternatives:

| | Why not |
|---|---|
| Plain WireGuard hub | terminates both tunnels — **relay sees plaintext** |
| TURN | allocate + CreatePermission + credentials; buys nothing when both ends are yours |
| Nebula-style relay | Noise session with the relay; more machinery than needed |
| libp2p Circuit Relay v2 | not reachable through the FFI, and defaults are **2 min duration, 128 KB per direction** — not a VPN |

**Cost:** negligible CPU (9 kpps at 100 Mbit/s). Relaying doubles bytes, so a
1 TB/month VPS sustains ~3.1 Mbit/s continuously — ample for a handful of devices
where only a minority of pairs fall back.

**Discovery:** the VPS publishes a signed `RelayAnnounce` to a Store-backed topic
every 30–60 s. Devices fetch it via `waku_store_query`. The VPS IP can change
freely. The only pinned constant is one 32-byte mesh public key.

---

## 9. Open risks

Ordered by how much they'd change the design.

### R1 — Android Edge mode may be unable to publish

`mix: true` on cluster 2, and mix binds only to `WakuLightPushCodec`. Core mode
publishes via relay (unaffected). **Edge publishes via lightpush → through mix**,
which adds ~150–500 ms and **fails entirely if fewer than 3 mix nodes are in the
pool** ("publishing with mix won't work until at least 3 mix nodes in node pool").

If logos.dev's mix pool is thin, your phone cannot publish. **Test first (§P-S1).**

### R2 — logos.dev store retention is unknown

The intermittent design leans on Store as the async mailbox. If retention there is
short or unreliable, the mailbox must move to the VPS (which runs a Core node
anyway, so this is a fallback, not a redesign).

### R3a — API skew between liblogosdelivery builds ✅ confirmed, mitigated

Verified during S1: the packaged build Basecamp installs and logos-delivery
master differ in the event API (`set_event_callback` vs
`add_event_listener`) and the config JSON shape (flat vs nested). Payload
encoding is also asymmetric — base64 on send, byte array on receive.

Mitigation: `internal/waku` targets the packaged build, and the header is pinned
to the `.so`. Re-check on any upgrade. See PROTOTYPE §4 S1a/S1b.

### R3 — Android JNI is thin

The wrapper at `xAlisher/logos-libdelivery-android` exposes only
`setup / new / start / relaySubscribe / relayPublish / stop / setEventCallback`.
**No store query, no filter, no lightpush.** The `.so` has all of them
(`waku_store_query`, `waku_filter_subscribe`, `waku_lightpush_publish`), so this
is local JNI work — but it is on the critical path, since relay discovery and
wake-and-sync both need store queries.

### R4 — logos.dev is a dev fleet

No SLA. May be reset or reconfigured. For a daily driver this is a real
dependency. Mitigation: the design pins nothing but a mesh public key, so moving
to another cluster (or your own) is a config change. Keep that path warm.

### R5 — autosharding topic rotation

Two independent research passes confirmed `{name}` is not hashed, but nwaku#2538
("autosharding resolves content topics to wrong shard") means this should be
tested against a live node rather than assumed.

---

## 10. Parameters

| Parameter | Value | Rationale |
|---|---|---|
| `PersistentKeepalive` | **15–25 s** | 74% of CGNs expire idle UDP ≤60 s; cellular median 65 s, non-cellular CGN median **35 s** |
| Tunnel MTU | 1420 | survives IPv6 underlay |
| Waku keepalive | 0 or ≥240 s | 10 s default is the pessimal LTE power point |
| Punch spray | 100,200,…,1000 ms for 5.5 s, then 5 s ×30 s, then 30 s | Nebula's linear backoff |
| Endpoint announce | 30–60 s, fixed size | also hides online/offline pattern |
| Topic epoch | 3600 s, accept ±1 | clock skew, not protocol, is the binding constraint |
| Credential expiry | 7–30 days | bounds suppressed-revocation damage |
| Store query window | explicit `startTime` | `autoStoreResume` silently truncates at 6 h |
| Android wake floor | 30–60 min | Doze caps `setExactAndAllowWhileIdle` at 1 per 9 min |

---

## 11. Sources

Research conducted 2026-08-06. Primary sources read directly where possible.

**Logos / Waku** — `logos-messaging/logos-delivery` @ `a5d7818`,
`logos-delivery-go` @ `3960897`; `library/liblogosdelivery.h` and
`liblogosdelivery_kernel.h`; `factory/networks_config.nim`; `api/conf/modes.nim`;
`waku_relay/protocol.nim`; `waku_store/resume.nim`; `waku_mix/protocol.nim`;
specs at [lip.logos.co](https://lip.logos.co) and
[logos-co/logos-lips](https://github.com/logos-co/logos-lips).
Android JNI: [xAlisher/logos-libdelivery-android](https://github.com/xAlisher/logos-libdelivery-android).

**Mesh VPN prior art** — [slackhq/nebula](https://github.com/slackhq/nebula)
(`lighthouse.go`, `punchy.go`, `handshake_manager.go`);
[tailscale/tailscale](https://github.com/tailscale/tailscale) (`magicsock.go`,
`disco.go`, `endpoint.go`, `derp.go`);
[zerotier/ZeroTierOne](https://github.com/zerotier/ZeroTierOne) (`Packet.hpp`);
[netbirdio/netbird](https://github.com/netbirdio/netbird) (`ice_bind.go`,
`signalexchange.proto`); [tonarino/innernet](https://github.com/tonarino/innernet);
[costela/wesher](https://github.com/costela/wesher); tinc `src/graph.c`;
[yggdrasil-network](https://github.com/yggdrasil-network/yggdrasil-go)
`src/address/address.go`; cjdns `doc/security_specification.md`.

**Measurements** —
Hugenroth & Beresford, *Powering Privacy*, USENIX Security '23 (battery);
Trautwein et al., [arXiv:2510.27500](https://arxiv.org/abs/2510.27500) (DCUtR, 4.4M attempts);
Seemann/Inden/Vyzovitis, DINPS 2022 (hole punching);
Richter et al., IMC 2016 (CGNAT prevalence, NAT timeouts);
Halkes & Pouwelse, IFIP Networking 2011 (NAT taxonomy in the wild);
Revuelta et al., [DLT 2024](https://ceur-ws.org/Vol-3791/paper13.pdf) (Waku relay latency);
Heimbach et al., USENIX Security '25, [arXiv:2409.04366](https://arxiv.org/abs/2409.04366) (gossipsub deanonymisation);
Thormarker, [eprint 2021/509](https://eprint.iacr.org/2021/509) (Ed25519/X25519 key reuse);
[defined.net mesh VPN benchmarks](https://www.defined.net/blog/nebula-is-not-the-fastest-mesh-vpn/);
[EdgeVPN #157](https://github.com/mudler/edgevpn/discussions/157) (libp2p tunnel throughput);
[Tailscale throughput posts](https://tailscale.com/blog/more-throughput).

**Specs** — RFC 4787 (NAT behaviour), RFC 4193 (ULA), RFC 6887 (PCP),
EIP-1459 (DNS discovery), Waku RFCs 11/13/14/17/31/34/43/64,
[libp2p DCUtR](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md).
