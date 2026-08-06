# 002. Userspace WireGuard, not the kernel module

**Status:** accepted

## Context

Kernel WireGuard is faster than wireguard-go and needs no embedding. On Linux it
is the obvious choice.

## Decision

Use userspace WireGuard (wireguard-go) everywhere.

## Why

**NAT traversal and the tunnel must share one UDP socket.** Tailscale states the
constraint plainly: otherwise the reflexive address you discover via STUN or an
in-band echo is not the port your data actually arrives on, and hole punches
land on the wrong mapping.

Kernel WireGuard owns its socket and will not share it. This is why Tailscale
runs wireguard-go on every platform despite the kernel module existing.

Two supporting reasons:

- **Android has no kernel WireGuard without root.** The official app's
  `WgQuickBackend` requires root; the Play Store path is `GoBackend`, i.e.
  wireguard-go. Since Android forces userspace anyway, kernel WG would be a
  Linux-only special case rather than an architecture.
- **The throughput cost is not what it used to be.** With GSO/GRO (merged
  Oct 2023), wireguard-go measured 13.0 Gb/s against the kernel's 11.8 on the
  same machine. The gap only appears without offload.

## Consequences

- Lower throughput on Linux than the kernel module would give, in exchange for
  NAT traversal being possible at all.
- One code path across Linux and Android.
- Kernel WireGuard remains available later as an opportunistic fast path for
  nodes with a stable public endpoint, where no traversal is needed.
