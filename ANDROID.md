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

Deliberately narrow in its types — `gomobile bind` supports a restricted type
set, and a wide boundary would drag Go types into Kotlin — so it passes strings,
mostly JSON. A sketch of the core of it; `mobile/` has the rest (settings,
invites issued from the phone, Keycard, diagnostics):

```go
package mobile

// Start brings the mesh up on an existing TUN descriptor.
//   tunFd      from VpnService.Builder.establish()
//   configDir  app-private storage
//   dnsServers the network's own resolvers, for the in-app resolver to forward to
//   p          Kotlin calls VpnService.protect on the fds it is handed
//   l          the daemon's log, to the app
func Start(tunFd int, configDir string, dnsServers string, p Protector, l Logger) error
func Stop() error

// StatusJSON is the same payload the CLI's `status --json` returns, so both
// front-ends read one schema.
func StatusJSON() string

// Init creates a mesh. JoinWithInvite redeems an invite token (ADR-017);
// InviteToken says whether scanned text is one. Join, by network key, is a
// stub that returns an error: that way in was removed.
func Init(name, meshName, configDir string) (networkKey string, err error)
func JoinWithInvite(token, name, configDir string, timeoutSeconds int) error
func JoinAnotherWithInvite(token, label, name, configDir string, timeoutSeconds int) error
func InviteToken(scanned string) string

type Protector interface{ Protect(fd int) bool }
type Logger interface{ Log(level, message string) }
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

The build runs in a container (`golang:1.26.11`, the core's Go): gomobile needs
a JDK, which the core does not, and the host SDK/NDK is mounted rather than
re-downloaded.

The Android `liblogosdelivery.so` is not ours to build: `make android-deps`
(`scripts/fetch-android-lib.sh`) takes it from another project that vendors it,
at a pinned commit, and checks each file against `deps/CHECKSUMS`.

### A2 + A3 — the app

**✅ Shipping, in daily use on a real phone.** `make apk` builds
`android/shrooms.apk`: arm64 only, all four native libraries, no stray ABIs.
Three variants, from `scripts/build-apk.sh`:

- `make apk`: a debug build, `xyz.vpavlin.shrooms`.
- `RELEASE=1`: an unsigned release APK, signed elsewhere.
- `AGENTS=1`: Shrooms Agents, `xyz.vpavlin.shrooms.agents`, a separate app from
  the same code with its own key, which never touches the VPN.

`make fdroid` builds unsigned and signs and publishes it on the F-Droid repo's
host, where the key stays. No CI workflow builds the `.aar` or the APK; they
are built by hand, and a phone is the test.

A2 and A3 were merged because the split was for isolating failures during
debugging, and with no phone here at the time the useful artefact is one that does the whole thing. The
telemetry makes a failure attributable anyway: discovery, path and tunnel are
reported separately, so "which stage" is answerable from the app itself.

What is in it:

- `MeshVpnService`, a foreground service holding the tunnel. `Builder` adds,
  for every enabled mesh, its derived /128 and a route for **only** its /48,
  plus its synthetic IPv4 address and block (ADR-021); MTU 1280; and excludes
  our own package so the app's traffic is not captured by its own tunnel.
- The delivery node runs as **Edge** by default (`mobile/support.go`): as Core
  a phone spent mobile data relaying for others and, behind a carrier NAT,
  failed to hold a gossip mesh at all. Desktops stay Core.
- `Protector` forwarding `VpnService.protect`; Go refuses to start if it returns
  false or no socket is exposed.
- The TUN descriptor is dup'd in Go, so closing it here is safe.
- A Compose UI over the same `status --json` schema the CLI reads.

**Done when:** the phone appears in `shrooms status` on the laptop, `ping6`
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
- Joining is an invite token, by QR from another device or pasted
  (`shrooms invite`, or the invite button in another device's app).

Monospace for anything technical; addresses and keys are read character by
character. Sober about state: green only when traffic is actually flowing,
because a VPN that claims to be up when it is not is worse than one that admits
it is down — a lesson from `status` reporting "up" for tunnels whose far end had
vanished.

No accounts, no telemetry, no cloud. There is no server to have one.

## The one that looked like DNS three times

`VpnService` **blocks** an address family it holds an address for unless the
traffic matches a route, and blocks families it has no address for outright.
This tunnel is IPv6-only and routes a single /48, so every IPv4 packet on the
device was being dropped.

Name servers are almost always IPv4, so it presented as "DNS does not work" —
three times, through three unrelated fixes, while the actual fault was that
nothing except the mesh could send anything at all. It survived removing the
resolver entirely, which is what finally ruled DNS out.

`allowFamily(AF_INET)` and `allowFamily(AF_INET6)` let everything that is not
the mesh bypass the tunnel. The /48 route still captures the mesh, because
routes take precedence over the bypass.

The lesson is not about Android. Three fixes in a row addressed where the error
appeared rather than where the decision was made, and each looked like progress
because the symptom was unchanged for a different reason each time.

## DNS on Android: why it is harder than ADR-013 said

Three attempts, each of which took the phone's internet away. Recorded so the
fourth does not.

**Android has no split-DNS.** `addDnsServer` makes the tunnel's resolver receive
*every* query the device makes; `addSearchDomain` does not narrow it. A resolver
answering only mesh names therefore removes name resolution entirely.

**So it must forward** — which means the forwarding socket has to be protected,
or it routes into the tunnel it is resolving for.

**And even then the resolver is unreachable.** Android routes queries for a
VPN's DNS server *through* the VPN. The query is written into our tun, where
wireguard-go looks for a peer owning the destination address — our own — finds
none, and drops it. The resolver is listening on an address nothing can deliver
to.

**And the resolver must not answer on this device's own address.** A packet
addressed to an address the interface holds is delivered locally by the kernel
and never traverses the tun — so neither a socket nor the read-path intercept
ever sees it. The resolver answers on `<mesh prefix>::53` instead: inside the
routed prefix, assigned to nothing, so packets for it go into the tun where the
intercept is waiting. Tailscale uses 100.100.100.100 for exactly this reason.

None of that is fixable from Kotlin. The query is intercepted in the **tun read
path**, before WireGuard sees it: the tun device is wrapped, and packets for the
resolver's address on UDP/53 are answered locally, the reply written back into
the tun; everything that is not a mesh name is forwarded, on a protected
socket, to the network's own resolvers (`dnsServers` in `Start`). This is how
Tailscale does it. The app registers the resolver only when it has those
upstream servers to forward to, since claiming DNS without them can only break
things.

The Linux daemon is unaffected: there the address is on a real interface and the
kernel delivers locally, which is why the same code works.

## Known before starting

- **arm64 only.** No public x86_64 `liblogosdelivery.so`, so the emulator has no
  node. A real phone is required; CI can build but not run.
- **`CreateUnmonitoredTUNFromFD`, never `CreateTUNFromFile`.** The latter also
  opens a netlink socket to watch for route and MTU changes, which an ordinary
  app has no privilege for. It fails with EPERM on a descriptor that is
  perfectly usable, reported as "tun from fd: permission denied" — an error that
  points at the descriptor rather than at the monitoring nobody asked for.
- **The overlay was IPv6-only.** `VpnService` had to be configured for that,
  routing only the mesh prefix. Each mesh now also has a synthetic IPv4 block
  (ADR-021), routed the same way, because browsers ask for A records.
- **Battery.** wireguard-go's persistent keepalive is 25s and disco probes every
  5s (`PathRefresh`). Both are tuned for a machine on mains power and should be
  measured before being assumed acceptable on a phone.
- **The network key was a bearer credential** (ADR-008), and a phone can be
  lost. Per-device credentials and revocation (M5, ADR-018) are built: a lost
  phone is revoked with `shrooms admin revoke` and costs that phone alone. The
  network key still sits in the app's private storage, not Android's keystore,
  and still decrypts the control plane (SECURITY.md).
