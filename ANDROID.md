# Android

A phone as a full mesh participant, not a client of something else. See
[ADR-016](docs/adr/016-android-reuses-the-go-core.md) for why the Go core is
reused rather than reimplemented.

## Shape

```
┌─────────────────────────────────────────────┐
│  Compose UI          dark, minimal          │
├─────────────────────────────────────────────┤
│  MeshVpnService : VpnService                │
│    Builder → tun fd, address, route, DNS    │
│    protect(fd) for the shared UDP socket    │
│    foreground notification                  │
├─────────────────────────────────────────────┤
│  logosvpn.aar        gomobile bind          │
│    internal/mesh  control  disco  relay  wg │
│    internal/waku  ──cgo──┐                  │
├──────────────────────────┼──────────────────┤
│  jniLibs/arm64-v8a       ▼                  │
│    liblogosdelivery.so (29 MB)              │
│    librln.so  libc++_shared.so              │
└─────────────────────────────────────────────┘
```

Kotlin owns only what Go cannot reach: the TUN descriptor, socket protection,
the notification, and the interface.

## The Kotlin ↔ Go boundary

Deliberately narrow — `gomobile bind` supports a restricted type set, and a wide
boundary would drag Go types into Kotlin.

```go
package mobile

// Start brings the mesh up on an existing TUN descriptor.
//   tunFd     from VpnService.Builder.establish()
//   configDir app-private storage
//   protector Kotlin calls VpnService.protect on the fds it is handed
func Start(tunFd int, configDir string, protector Protector) error
func Stop() error

// StatusJSON is the same payload the CLI's `status --json` returns, so both
// front-ends read one schema.
func StatusJSON() string

// Join writes a config for a network key. Init creates a mesh.
func Join(key, name, configDir string) error
func Init(name, configDir string) (networkKey string, err error)

type Protector interface{ Protect(fd int) bool }
```

## Stages

Each stage is independently checkable, and the risky ones are first.

### A1 — the core cross-compiles (highest risk, do first)

`GOOS=android GOARCH=arm64` with cgo against the Android
`liblogosdelivery.so`. Nothing else matters if this does not link.

**Done when:** `gomobile bind` produces an `.aar` containing `arm64-v8a` and the
native libraries, and a trivial app calls `StatusJSON()` without crashing.

Risk: cgo + gomobile + a prebuilt 29 MB `.so`. If `gomobile bind` fights us, the
fallback is `-buildmode=c-shared` and a small hand-written JNI entry point —
more work, more control.

### A2 — a node that talks

No VPN yet. A foreground service starts the mesh, announces, and shows peers.
This isolates the rendezvous plane from every VPN complication.

**Done when:** the phone appears in `logos-vpn status` on the laptop, and the
phone lists the laptop and VPS.

### A3 — the tunnel

`VpnService.Builder`: the derived /128 address, a route for the mesh /48 only
(not a full tunnel), MTU 1280. `tun.CreateTUNFromFile` on the descriptor.
**Protect the UDP socket** — see ADR-016; this is the one that fails silently.

**Done when:** `ping6` between phone and VPS over the overlay, and ssh from the
phone works.

### A4 — traversal from a phone

Mobile networks are where this gets interesting: CGNAT is common and roughly 40%
of it is endpoint-dependent, which is exactly where punching fails and the relay
must take over. Compare wifi against LTE, and moving between them.

**Done when:** the relay path is confirmed on LTE, and a wifi↔LTE handover
reconnects without user action.

### A5 — living on a phone

Doze, screen-off, battery. The rendezvous connection will die; tunnels must
survive it (DESIGN §2) and reconnect without hitting nwaku's 8.5-hour backoff.

**Done when:** it survives a night in a pocket and is usable in the morning.

## Interface

Minimal, dark, cypherpunk — but the restraint matters more than the aesthetic. A
VPN's interface should answer three questions and then stop talking: is it on,
who can I reach, and if something is broken, what.

- **One switch.** Connected / not, and how long.
- **Peers**, name and a reachability dot. Tapping one shows how it is reached —
  direct or relayed, latency, and the three connect timings we now measure.
- **Trouble** surfaces the same distinctions the CLI learned the hard way:
  rendezvous down is not the same as a peer being offline, and a stale handshake
  is not the same as a live one.
- Joining is a network key, by QR from another device or pasted.

Monospace for anything technical; addresses and keys are read character by
character. Sober about state: green only when traffic is actually flowing,
because a VPN that claims to be up when it is not is worse than one that admits
it is down — a lesson from `status` reporting "up" for tunnels whose far end had
vanished.

No accounts, no telemetry, no cloud. There is no server to have one.

## Known before starting

- **arm64 only.** No public x86_64 `liblogosdelivery.so`, so the emulator has no
  node. A real phone is required; CI can build but not run.
- **The overlay is IPv6-only.** `VpnService` must be configured for that, and
  route only the mesh prefix.
- **Battery.** wireguard-go's persistent keepalive is 25s and disco probes every
  5s (`PathRefresh`). Both are tuned for a machine on mains power and should be
  measured before being assumed acceptable on a phone.
- **The network key is a bearer credential** (ADR-008). Putting one on a phone
  that can be lost sharpens the case for M5 — per-device credentials and
  revocation — and for Android's keystore.
