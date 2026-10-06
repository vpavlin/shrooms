# 039. On a phone, the app tells the core what it cannot see

**Status:** accepted, built 2026-10-06 — PRs #20 and #19 (strudelPi), merged

## Context

On Android the Go core (ADR-016) is not allowed to list the phone's own
addresses: netlink is denied to apps. A phone therefore announced no endpoints
at all; after a move from mobile data to Wi-Fi its peers kept trying the old
addresses, and on 2026-10-04 the phone could not reach the office mesh at all. Separately, a delivery node can come up
deaf — relaying, but never opening its own mesh's announces — and the core's
check for that (`Health.Silent`) could not fire for a node that had never heard
a peer. A phone sat that way for at least 24 minutes after a restart. The
desktop daemon restarts on such a fault; the phone did not.

## Decision

- **The Android app hands the core its addresses.** It reads the active
  network's link addresses from ConnectivityManager on every network change and
  calls `Mobile.networkChanged`, which keeps them for every announce and has
  each mesh drop what peers observed and announce at once. Only a real change
  of the addresses is passed on, since each call throws away the reflexive
  addresses a phone behind NAT needs.
- **A node that started deaf is deaf**: `Silent` measures from the node's start
  when no announce was ever opened (after `SilentAfter`, 12 minutes).
- **The phone acts on it like the desktop does.** Its status carries `deaf`, and
  the service's watchdog restarts the app on it, a new process being the cure,
  under the same persisted 30-minute floor as the restart on a dead library —
  so a phone that comes up deaf every time cannot loop. The restart no longer
  calls `Mobile.stop`, where a segfault was seen.

## Consequences

- The core on a phone depends on the app for something the desktop core finds
  itself; another host without netlink (iOS, a sandbox) needs the same feed.
- A deaf phone can still lose up to 12 minutes before it is noticed, and a
  second deafness within 30 minutes of the first restart is waited out.

## What would change our mind

Android allowing apps to list their addresses again, or a delivery library that
recovers from deafness without a new process.
