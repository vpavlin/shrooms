# 065. The mesh's delivery node is infrastructure: applications run their own

**Status:** accepted 2026-10-19 by the owner. Writes down what had only been
said in conversation.

## Context

Every Shrooms daemon runs a Logos Delivery node: its rendezvous, the way a
mesh's members find and reach each other. Applications on the same machines
want Logos Delivery too:
- Basecamp runs its own node, and on the laptop it is the larger share of the
  delivery traffic (measured 2026-08-27);
- Scala, for agents' schedules (ADR-064), would sync through one;
- Foray, and whatever comes next, may.

Each one is another node, and more traffic. So it keeps being asked whether
they could ride on the daemon's instead. That had been answered no more than
once, and never written down.

## Decision

**The Shrooms daemon's delivery node is the mesh's infrastructure, and
nothing else uses it. An application that needs Logos Delivery runs its own
node, or reaches one that already runs elsewhere.**

- **The daemon is kept to one job.** Its node's settings follow what the mesh
  needs: Core or Edge, its ports (ADR-035), its restarts after a network
  change. An application's needs would pull them other ways.
- **Neither can take the other down.** A fault or a load in an application's
  traffic must not cost the mesh its rendezvous, and a mesh restart must not
  drop an application's sync.
- **Their traffic is kept apart.** It stays attributable to whoever caused
  it, which is how the laptop's delivery traffic was found to be mostly
  Basecamp's.
- **No interface is opened on the daemon for this.** It does not offer
  publish or subscribe to local programs.

**To keep the count of nodes down:**
- run an application's node as Edge where it can: a light client that relays
  nothing;
- or let one machine's node serve the application for the others, over the
  mesh. ADR-064's schedules may do this, with a Scala hub on a machine that
  already runs one.

## Consequences

- A machine may run more than one delivery node: one for the mesh, and others
  for applications. That is the price, paid knowingly.
- Questions of "could this use the daemon's node?" are answered here.
