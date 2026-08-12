# 026. Announce what is bound to the mesh address

**Status:** accepted; built, off by default

## Context

[ADR-023](023-announcing-services.md) states the security position plainly:
publishing a service is the access decision, and `services` forwards a mesh
connection to a loopback port — which makes every member a local user of
anything that trusts `127.0.0.1`, and what is missing is authentication in the
application.

There is a second arrangement that ADR did not consider, and it is better.

**Bind the service to the overlay address.** `sshd` listening on
`fd3b:ffe9:f81:81a7:…:22` rather than on `::` is reachable by mesh members and
by nobody else — not the LAN, not a café network, not the internet — because
only members can route to that prefix at all. The bind *is* the access control.
No forwarder is involved, no name router, and the application needs no
awareness of the mesh.

It works today and needs no code. What it lacks is that nobody is told: the
port is reachable at `laptop.mesh:22` and the only way to know is to have been
told by a person.

## Decision

**Announce the ports listening on this device's mesh address, per mesh, off by
default.**

```toml
announce_services = "true"   # the names this device forwards (ADR-023)
announce_bound    = "true"   # the ports already listening on its mesh address
```

### These are a different claim from ADR-023's, and are carried separately

| | how it is reached | what makes it work |
|---|---|---|
| `services` | `immich.nas.mesh` | this daemon forwards to a local port |
| bound port | `nas.mesh:22` | the service is on the mesh address itself |

They travel in the same control message and in different fields, because
rendering one as the other prints an address that does not work: there is no
`ssh.laptop.mesh`, and there is no forwarder behind `laptop.mesh:2283`.

### Only exactly our own address counts

A socket bound to `::` is reachable from every network the device is on. Listing
it as a mesh service would be a claim about who can reach it, and a false one.
So the filter is exact address equality against this mesh's overlay address —
not "any IPv6 socket", not "not loopback".

That also makes the feature self-documenting in the useful direction: a service
appears here **because** somebody bound it to the mesh, which is the act that
made it mesh-only.

### Discovered, not declared, and that is the risk

`services` entries are written one line at a time by somebody who meant it.
These are found. So this announces whatever happens to be bound, including the
debug server you started for ten minutes and forgot — to everyone on that mesh.

The ports are **already reachable** by every member either way, so this is
disclosure rather than exposure, and the same argument ADR-023 makes applies:
a member can find them by connecting to a few hundred ports, and what the
announcement adds is intent in names. It is still the reason this is off by
default and per mesh, and the reason the log says what it announced.

Port 80 and 443 are excluded: they are the name router's, and announcing them
as "http" would send somebody to a router rather than to anything.

### Names are advisory

A short table of well-known ports, so `22` reads as `ssh`, and anything else is
announced by its number as `port-4711:4711`. The port is the fact and the name
is for reading; deriving a nicer name from the process would need to inspect
`/proc/<pid>/fd` for socket inodes, which is a great deal of machinery for a
label a person can read off the number anyway.

## Consequences

- Binding a service to the mesh becomes a discoverable act rather than a
  private arrangement, which makes the strongest access-control pattern this
  system offers also the most convenient one.
- One more thing announced by default? No — off by default, like ADR-023, and
  for a stronger reason, since these are found rather than chosen.
- The roster can show `nas.mesh:22` next to `immich.nas.mesh`, which is the
  whole point: what a device offers, however it offers it.
- Linux only, since it reads `/proc/net/tcp6` and `/proc/net/udp6`. There is no
  portable API for the question, and every tool that answers it does the same.
  A kernel without those tables reports nothing rather than failing.
