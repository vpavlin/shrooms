# 049. Sessions per mesh

**Status:** accepted 2026-10-10, built the same day — [docs/agents.md](../agents.md), "Sessions per mesh"

## Context

A machine's agent serves on every mesh the machine is in, and sessions
belonged to the machine. Putting a family member's device on the laptop's
home mesh would have shown them every session on the laptop, office work
included, with everything the apps can do to a session. It would also have
let agents on the home mesh ask the office sessions for work. The mesh was
the only boundary, and the same boundary for everything on the machine.

## Decision

**A session can be limited to some of its machine's meshes.**

- The `meshes` setting names them by this machine's labels (`GET /v1/meshes`).
  Empty is every mesh, which is what existing sessions keep.
- From any other mesh the session does not exist: every route answers as it
  would for a session that isn't there. That covers listing, opening,
  settings, rename, cage, delete, A2A (the card and every method), files, and
  tasks.
- The caller's mesh comes from the name the daemon gives the peer it called
  from (`phone.home`).
- This machine (loopback, or one of its own mesh addresses) always sees
  everything.
- An address the agent can't name used to count as this machine. Now it is
  no one, and sees no limited session.

"Moving a session to another mesh", as asked, is changing the list.

## Consequences

- A machine on a home mesh and an office one can keep sessions for each
  without either seeing the other's.
- Labels are local to each device (docs/mesh-labels-are-local.md). The
  setting uses this machine's labels, which is also how this machine names
  its callers, so the two always agree.
- Moving a session to another *machine* is a different thing and not done:
  it would mean carrying a conversation and its working directory across.
