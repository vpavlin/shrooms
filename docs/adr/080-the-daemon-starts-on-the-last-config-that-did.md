# 080. The daemon starts on the last config that did, when an edit is refused

**Status:** accepted 2026-11-04 by the owner ("keeping a working copy of
config makes a lot of sense"); built.

## Context

The daemon exits when its config is refused: at load (it does not parse, or
`Validate` refuses it), or when a mesh fails to start from it. Under systemd
that is a restart loop with every mesh down, including the one the machine is
reached over. Whoever edited the file then needs another way in to fix it.

On 2026-11-04 the owner edited k11's config to move Home Assistant from a
service to a LAN host (ADR-068). The daemon did not come back, k11 left the
home mesh, and it was reached only over the relay, from the office: it waited
until he was home. "We need to figure out a way to prevent bad config killing
shrooms."

Services were made non-fatal for the same reason earlier (`Validate` says
nothing optional may stand between a machine and its own mesh), and LAN hosts
the same day (ADR-068's note). That covers the optional parts. This covers
the rest: any edit at all.

## Decision

- **After a clean start**, once every mesh is up, the daemon keeps the config
  it started with as `<config>.last-good`, beside it, with the config's
  permissions (it holds the mesh keys), written atomically, only when it
  changed.
- **A config refused at load**: the daemon starts on the last good copy and
  logs at error level why.
- **A config that loads but fails to start a mesh**: the daemon records it
  (`<config>.refused`: its sha256, the reason) and exits as before; systemd's
  restart seconds later starts on the last good copy. The record only applies
  to that exact file: any edit since and it is tried again.
- **The edited file is never touched.** It is the person's work; the fallback
  lasts until they fix it and restart.
- **`shrooms status` says so first**, above everything that looks healthy:
  which config runs, which was refused and why. The status JSON carries
  `config_refused` and `config_running`.
- With no last good copy (a first start), a refused config fails as before.

## Consequences

- An edit can no longer take a machine off its meshes, as long as it once
  started cleanly with this daemon.
- The daemon may run on a config older than the file in place, until someone
  reads the status or the log. That is the point, and it is why status leads
  with it.
- Commands that change the config through the daemon read the file in place,
  so while it is refused they fail rather than overwrite the edit.
- A copy of the config's secrets now sits beside it, with the same
  permissions.
- What failed for reasons outside the config (no TUN, a port in use) falls
  back to a copy that fails the same way: no worse than before.
