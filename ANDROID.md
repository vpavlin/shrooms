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

**✅ A1 done.** `make aar` produces an 18 MB `android/logosvpn.aar`:

```
classes.jar                        mobile.Mobile, Protector, Logger
jni/arm64-v8a/libgojni.so           5.9 MB   the Go core
jni/arm64-v8a/liblogosdelivery.so  27.6 MB   the node
jni/arm64-v8a/librln.so             6.1 MB
jni/arm64-v8a/libc++_shared.so      1.7 MB
```

Two things this cost, both worth knowing:

**The binding lives in its own module** (`mobile/go.mod`). `gomobile bind`
requires `golang.org/x/mobile` in the module holding the bound package, and
adding it to the root bumped the go directive to 1.25 and upgraded `x/sys`,
`x/sync` and `x/tools` — moving wireguard-go's dependency graph as a side effect
of wanting an `.aar`. The core's dependencies are the ones its tests ran
against. Nested modules are excluded from the parent's `./...` patterns, so the
root build and test are untouched.

**gomobile packages only what it built**, so the three prebuilt libraries are
injected afterwards. Without them the `.aar` loads and dies at the first call
into liblogosdelivery — which presents as a Go bug. `build-aar.sh` fails if any
of the four is missing, because the alternative is discovering it on a phone.

The build runs in a container: gomobile needs a JDK and Go ≥ 1.25, neither of
which the core requires, and the host SDK/NDK is mounted rather than
re-downloaded.

### A2 + A3 — the app

**🟨 Built, not yet run on hardware.** `make apk` produces a 51 MB
`android/logos-vpn.apk`: arm64 only, all four native libraries, no stray ABIs.

Merged because the split was for isolating failures during debugging, and with
no phone here the useful artefact is one that does the whole thing. The
telemetry makes a failure attributable anyway: discovery, path and tunnel are
reported separately, so "which stage" is answerable from the app itself.

What is in it:

- `MeshVpnService`, a foreground service holding the tunnel. `Builder` adds the
  derived /128, routes **only** the mesh /48, MTU 1280, and excludes our own
  package so the app's traffic is not captured by its own tunnel.
- `Protector` forwarding `VpnService.protect`; Go refuses to start if it returns
  false or no socket is exposed.
- The TUN descriptor is dup'd in Go, so closing it here is safe.
- A Compose UI over the same `status --json` schema the CLI reads.

**Done when:** the phone appears in `logos-vpn status` on the laptop, `ping6`
works between phone and VPS, and ssh from the phone works.

### A3 — the tunnel (folded into A2 above)

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
- **`CreateUnmonitoredTUNFromFD`, never `CreateTUNFromFile`.** The latter also
  opens a netlink socket to watch for route and MTU changes, which an ordinary
  app has no privilege for. It fails with EPERM on a descriptor that is
  perfectly usable, reported as "tun from fd: permission denied" — an error that
  points at the descriptor rather than at the monitoring nobody asked for.
- **The overlay is IPv6-only.** `VpnService` must be configured for that, and
  route only the mesh prefix.
- **Battery.** wireguard-go's persistent keepalive is 25s and disco probes every
  5s (`PathRefresh`). Both are tuned for a machine on mains power and should be
  measured before being assumed acceptable on a phone.
- **The network key is a bearer credential** (ADR-008). Putting one on a phone
  that can be lost sharpens the case for M5 — per-device credentials and
  revocation — and for Android's keystore.
