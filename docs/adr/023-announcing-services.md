# 023. Announcing services

**Status:** proposed

## Context

`services` publishes a local port on the mesh under a name — `immich:2283`
becomes `immich.nas.mesh` (ADR-019). Nothing announces it. Every node knows only
what it publishes itself, so the roster can show your own services and never
anyone else's, and finding out what a peer offers means remembering, or asking
the person.

The obvious improvement is to put them on the announce. The obvious worry is
that this hands every member a list of what you run.

## What it actually discloses

Less than it first appears, and not nothing.

**An announce is readable by every member and by nobody else.** It is sealed
under the per-epoch key derived from the network key; a passive observer on the
shard sees a fixed-size ciphertext (SECURITY.md).

**Members can already enumerate services.** They listen on the device's overlay
address at known ports, and that address is in the announce. Scanning a single
/128 for a few hundred common ports is seconds of work. So announcing buys
*discoverability*, not access — it does not make anything reachable that was not
reachable before.

**What it does add is intent, in names.** "immich", "home-assistant",
"jellyfin" tells a reader what you run and what to try, which a port scan does
not. On a mesh of your own machines that is worth nothing. On a mesh shared with
other people it is an inventory you did not mean to hand over, and the people on
it are exactly the ones for whom a list is useful.

That asymmetry is the whole decision, and it maps onto something that already
exists: meshes are separate, with separate configs.

## Decision

**Announce services, per mesh, off by default.**

```toml
services = ["immich:2283", "jellyfin:8096"]
announce_services = "true"          # this mesh's peers may see the names

mesh.shared.key = "D4R5TBD..."      # a mesh with other people on it —
                                    # nothing said about services here
```

**Carried in their own control message, not on the announce.** Announces are
padded to 512 or 1024 bytes and a credential already forced the larger size; a
service list would compete for that budget and be trimmed exactly when there is
most to say. A separate message, sealed the same way and sent every few minutes,
keeps the announce small and lets the list be trimmed independently.

**Names stay scoped to the device that claims them.** A service name is
self-asserted, like a device name (ADR-008): a member can publish a service
called `immich` on their own machine. Displaying it as `immich.their-laptop.mesh`
— which is already how the names work — makes that visible rather than
confusing. The roster should show the device, always.

**A service list is a claim, not a promise.** It says what a device intends to
publish, not what is running. `shrooms status` on the publishing node is the only
thing that knows whether the port is actually answering, so a peer's list should
be shown as names to try, not as a health display.

## Consequences

- The roster can show what the mesh offers, which is the point.
- A shared mesh discloses nothing by default, and turning it on is one line and
  one mesh at a time.
- One more message type on the control plane, at a few hundred bytes every few
  minutes. Negligible beside the ~20 MB/h the rendezvous node costs anyway.
- The name router already routes by `Host` and SNI, so a name from the roster is
  directly usable: no port to remember, which was the point of ADR-019.
- If the per-recipient announce wrapping of ADR-018 is ever built, this message
  should ride the same mechanism rather than growing its own.
