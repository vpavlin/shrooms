# 073. A phone in a poor signal area: futile rebuilds become a restart, truces grow

**Status:** accepted 2026-10-31 by the owner ("if we can improve things without
making it worse in general well connected world"); being built. Amends ADR-039.

## Context

A phone's diagnostics from 2026-10-31, on mobile data in a poor signal area:

- **Rebuilds that could not help.** The watchdog rebuilt the session every 11–19
  minutes for an hour, each time for "nothing has arrived yet", and one rebuild
  left the node subscribed to no topics at all. A rebuild keeps the delivery
  node (it is process-global and does not survive being restarted in place), and
  the node was what was wrong. Only "Disconnected" ended the process, and the
  node was not disconnected; nor was it deaf (`Health.Silent`, ADR-039), since
  nothing at all arrived, ours or anyone's.
- **Restarts listed as kills.** The watchdog's restart ends the process with a
  kill; `Mobile.noteRestart` wrote a log line but left the session record open,
  so the next start listed it as "KILLED" among Android's own kills, twice that
  morning.
- **An endpoint fight that never ended.** The phone and k11 went round
  `yieldRoam` every three minutes all morning: two rewrites, a two-minute truce,
  and the count started from zero again when it ended.
- **A warning that named the wrong cause.** "Cannot list this device's own
  addresses: netlinkrib: permission denied" — expected on Android, where the app
  supplies the addresses (ADR-039); what had happened was that none of them
  could be announced, the carrier's NAT hiding the phone.

## Decision

- **Two rebuilds without the plane working in between end the process**, under
  the same persisted 30-minute floor as the other restarts. The count is the
  service's (each rebuild starts a new watchdog loop) and any moment of a
  working plane resets it, so a connection that works never reaches it.
- **A restart on purpose closes the session record** ("restarted on purpose
  after X: why"), as a rebuild does.
- **A truce grows while the fight goes on:** a peer that drifts again within a
  minute of a truce ending gets twice the truce at once, without the rewrites,
  up to 16 minutes. One that stays settled for a truce's length starts again
  from two minutes, so a direct path is still corrected back promptly once the
  peer settles.
- **On a phone, the no-endpoints warning says the network's addresses could not
  be given**, not the netlink error.

- **The watchdog's holds are bounded and visible** (2026-11-03, build 90). A
  phone whose delivery died after an airplane-mode toggle stayed dead for hours
  with its tunnels up and no restart: the settle hold restarted on every
  network callback, link-property changes (IPv6 address rotation, DNS) among
  them, and mobile data in a poor area comes and goes. Now only a network
  coming or going restarts it, the hold lasts at most 15 minutes until the
  plane works or the watchdog acts, "usable network" counts the networks the
  callback has seen rather than only the active one, and a hold longer than 10
  minutes is written to the diagnostics log (`Mobile.NoteWatchdog`).

## Consequences

- On a well-connected phone nothing changes: the escalation needs two futile
  rebuilds, and the truce only grows for a peer that keeps roaming.
- A phone that keeps losing its node restarts at most every 30 minutes instead
  of rebuilding every 15; a restart costs the tunnels a few seconds more than a
  rebuild does.
- While a long truce holds, a tunnel may stay on the path WireGuard learned (the
  relay, say) for up to 16 minutes after a better one appears.

## What would change our mind

A delivery library that can be restarted inside a live process, or one that
reports why it receives nothing.
