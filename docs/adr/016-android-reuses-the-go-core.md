# 016. Android reuses the Go core

**Status:** accepted; prototype in progress

## Context

Android being a full mesh participant — not a client of something else — was an
original constraint. iOS is explicitly out of scope, which removes the 50 MiB
network-extension limit that shapes every other design in this space.

The mesh logic is not small: rotating rendezvous topics, sealed announces with
replay protection, derived addressing, reflexive discovery, hole punching, relay
selection, path scoring, endpoint churn avoidance. Most of today's bugs were in
that logic, and each fix is a property nobody would rediscover by reading the
code.

## Decision

**The Android app is a UI and a `VpnService` around the same Go core.** Nothing
in `internal/` is reimplemented in Kotlin.

The core is compiled to an `.aar` with `gomobile bind`. Kotlin owns exactly
three things Go cannot do: acquiring the TUN file descriptor from
`VpnService.Builder`, protecting sockets, and the interface.

### No JNI bridge

qaku-logos hand-writes a JNI shim over liblogosdelivery, and its README is
mostly the hazards of doing so: caching a global ref in `JNI_OnLoad`, attaching
the JVM per callback thread with a `pthread_key` detach, never attaching inside
`assert()` because NDEBUG strips it and you get a release-only SIGSEGV.

None of that applies here. That shim exists because React Native must go
JS → Kotlin → C. We go **Go → C directly**, which `internal/waku` already does
and which cgo already handles — including the callback arriving on a foreign
thread, the exact problem the JNI notes are about.

Verified: the Android `liblogosdelivery.h` is a strict superset of the one we
build against — it adds the reliable-channel API (`logosdelivery_channel_*`) and
`logosdelivery_kernel`, and every symbol `internal/waku` uses is present.

## What Android forces

**Sockets must be protected, or the tunnel eats itself.** A UDP socket created
inside a `VpnService` routes through the tunnel by default, so our own
rendezvous and disco traffic would loop into the interface carrying it.
`VpnService.protect(fd)` exempts it. wireguard-go exposes
`(*conn.StdNetBind).PeekLookAtSocketFd4/6` for precisely this; our `Bind` wraps
the inner bind, so it must forward those.

This is the single highest-risk detail. It fails as "nothing works at all" with
no useful error, and it is invisible on every other platform.

**arm64 only.** There is no public x86_64 `liblogosdelivery.so`, so an emulator
has no node. Testing needs a real arm64 phone, and CI can build but not run.

**Library load order.** `libc++_shared` → `librln` → `liblogosdelivery`.
Packaged in `jniLibs/arm64-v8a`, where the dynamic linker resolves the
`DT_NEEDED` chain; the explicit `System.loadLibrary` ordering qaku needs is a
consequence of its shim being loaded separately.

**Doze.** The rendezvous connection will be killed when the screen is off. This
is survivable by design — Waku is rendezvous, not a control plane, so existing
tunnels continue (DESIGN §2) — but discovery stops, and reconnect must not hit
nwaku's 8.5-hour peer backoff. A foreground service with a persistent
notification is required regardless, to keep the VPN alive.

**The overlay is IPv6-only**, and `VpnService.Builder` must be told so:
`addAddress` with the derived /128, `addRoute` for the mesh /48 only. Routing
everything would make this a full-tunnel VPN, which it is not.

## Consequences

- A bug fixed in the mesh is fixed on both platforms at once. Given how many of
  today's were timing-dependent and invisible in containers, having one
  implementation rather than two is the main safeguard.
- The `.aar` carries a 29 MB native library plus ~8 MB of dependencies. Fine for
  Android; it is the number that would have made iOS impossible.
- `gomobile bind` constrains the exported API to a narrow type set. The boundary
  is therefore deliberately small — start, stop, a JSON status snapshot, and a
  callback for protecting sockets — rather than exposing Go types to Kotlin.
- DNS becomes available essentially free once the resolver exists (ADR-013):
  `addDnsServer` plus `addSearchDomain` is a first-class API here, unlike every
  other platform.
