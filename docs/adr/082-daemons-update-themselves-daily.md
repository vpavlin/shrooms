# 082. Daemons update themselves daily, and roll back

**Status:** accepted 2026-11-11 by the owner ("Updating all the machines makes
sense, we should automate that"); built.

## Context

The daemon runs as a CI-built container image (`ghcr.io/vpavlin/shrooms:latest`)
under a systemd unit written by `scripts/install.sh`. Hosts running it under
podman were meant to be updated by podman's auto-update, which needs a label this
unit never set; hosts running it under Docker, which has no auto-update, ran
whatever image was last pulled by hand. pi5 and jimmy-crib were weeks behind
more than once: jimmy-crib missed the metrics endpoint for days in October, and
on 2026-11-11 pi5 still ran an image without the fix for its nightly shutdown
crash. Rolling the fleet was a manual round of ssh, pull and restart.

## Decision

- `install.sh` adds `shrooms-update.timer`: daily, at a random time fixed per
  machine (`RandomizedDelaySec=20h`, `FixedRandomDelay`), catching up a missed
  run after a reboot (`Persistent`). `AUTO_UPDATE=no` at install leaves it out.
- `/usr/local/lib/shrooms/update`, the same under Docker and podman:
  - pull the image; if the running container already runs it, stop;
  - if it is an image this machine rolled back before, stop until a newer one;
  - otherwise tag the running image `rollback-<date>`, restart the service, and
    wait up to two minutes for `shrooms status` to answer;
  - if it does not, tag the rollback image back as the image, restart, and
    remember the refused image.
  - Rollback tags beyond the newest three are removed.
- Spread over the day, a bad image reaches one machine first and that machine
  rolls itself back; the rest are not taken down together.

## Consequences

- Every installed host follows `master` within a day of CI publishing it.
- A broken image costs one failed restart per machine, then is skipped.
- "Answers on its socket" is the health check: a daemon that starts and talks
  but is wrong in a subtler way is not caught. Keeping `latest` tested before
  merge is still what keeps the fleet working.
- Hosts that run a native binary (the laptop) are not covered; they update with
  `make install`.
