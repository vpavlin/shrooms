# 043. Peers are probed on evidence, not only on announces

**Status:** accepted, built 2026-10-08 — generalises 9f522db (a relay that
answers stays the relay); rests on DESIGN §2 (the data plane does not need
the bus)

## Context

A phone that moved onto the office Wi-Fi reached the VPS and none of the
machines on the office LAN, for many minutes, until the app was
force-stopped — repeatedly, on moving between networks. Its delivery node,
which the app keeps across its in-process rebuilds (it does not survive being
restarted inside a process), had stopped bringing announces. And every way
back to a LAN peer went through an announce:

- `probeAll` skipped every peer reading offline (no announce for 3 minutes),
  so a remembered peer, or one whose announces had stopped, got no probe and
  no direct path;
- `syncPeers` then routed it through the relay — where the office machines
  are not registered — and after the remembered roster's 90 s it was dropped
  from WireGuard altogether;
- a network change only forgot reflexive addresses and announced;
- the other side did the same: a desktop not hearing the phone's announces
  neither probed it back nor kept it installed, so its handshake was refused.

The VPS worked because a public endpoint is dialled without a probe, and
since 9f522db a relay that answers is probed without announces. A force-stop
built a fresh delivery node; announces flowed; everything came back.

## Decision

A peer is probed when there is evidence it is there, or a reason to look —
not only when its announce is recent:

- it announced recently (as before);
- it answers our probes (a fresh path) — for any peer, not only a relay;
- it was remembered from the last run and this process has just started
  (the remembered roster's window);
- this device changed network in the last 90 s (`NetworkChanged`, which also
  rewrites endpoints at once);
- it probed us in the last 90 s — it is looking for us.

And a peer that answers our probes is kept in the data plane (`carry`) with
or without its announces.

## Consequences

- Roaming no longer depends on the delivery node surviving the move. A node
  stalled across rebuilds still costs discovery of *new* peers and changed
  endpoints; known ones come back over the LAN within a probe round.
- More probes: after a move, every peer in the roster for 90 s (a few
  packets each); peers that answer, every PathRefresh, as peers that announce
  already were. Nothing more for peers that answer nothing.
- Both sides need it: desktops built before it still refuse a phone whose
  announces do not reach them, until those arrive.
- The stalled delivery node is still only cured by ending the process (the
  watchdog does it on "Disconnected"); making it rebuildable, or the watchdog
  act on a partial one, is separate.

## What would change our mind

Probing peers that read offline turning out to keep dead entries alive (a
peer that answers is by definition not dead), or the probe volume mattering
on a large roster — then the move window would narrow to peers seen on the
same network before.
