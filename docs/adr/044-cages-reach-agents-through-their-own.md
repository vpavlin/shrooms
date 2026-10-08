# 044. Cages reach agents only through their own, which says so

**Status:** accepted 2026-10-08, built the same day — amends ADR-037 (a cage
"can reach the mesh"); the alternatives are in
[docs/agents-in-cages.md](../agents-in-cages.md), "Telling other agents a
request comes from a cage"

## Context

A caged session reaches the network through the machine's own sockets, so to
every agent — this machine's included — its requests were the machine's.
Two consequences. The owner wanted receivers to be able to refuse work from
caged agents (caged sessions work with caged ones, the laptop's do not), and
nothing could tell them a request came from a cage: the session's name is a
claim, and a "caged" flag would be one the cage could leave out. And a caged
agent could ask this machine's own agent to start a session outside any cage
— the one hole a cage still had.

Four ways were weighed: the cage's own agent vouching for it; an address of
its own on the mesh per cage (needs ADR-019's per-service addresses);
running shrooms inside each cage as a mesh member of its own; and no agent
access from cages at all.

## Decision

**Mesh peers stay machines.** Not a member, not an address per cage: that
would multiply the mesh's members and traffic for something one machine can
settle locally.

- **The agent port is closed inside every cage**: an nftables rule in the
  cage's network namespace, put there from outside on every start; root in a
  cage has no `NET_ADMIN` to remove it. The rest of the network is as before.
- **Each cage gets a socket to its own agent instead** (mounted at
  `/run/shrooms-agent/proxy.sock`), which forwards what the shrooms tools
  need: the machines and their sessions, an agent's card, `SendMessage`,
  `GetTask`, `AckTask`, and `task_update` for the cage's own session's tasks.
  Nothing that makes, deletes or reconfigures a session.
- **The agent names the asker, and says it is caged**: it writes
  `shrooms/from` itself, from the session the socket belongs to, and adds the
  `X-Shrooms-Caged` header. A receiver can believe it: no cage can reach an
  agent except through its own.
- **The receiver decides**, per session (`accept_caged`): by default a caged
  session takes tasks from caged agents and a session outside a cage does
  not. A refused task is answered with why.

## Consequences

- Caged and uncaged agents keep working together where the owner allows it,
  with the line drawn by the receiver, which is where the risk lands.
- A cage made before this has no socket mounted: it is made again on its
  next start, and what was installed in it goes.
- An agent on another machine that predates this ignores the header and
  takes the task; the protection is complete only once every agent has it.
- The machine's own processes outside cages are trusted as before: they are
  the owner's.
