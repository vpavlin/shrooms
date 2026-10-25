# 046. Metrics on the mesh addresses

**Status:** accepted 2026-10-09, built the same day; see [docs/metrics.md](../metrics.md).
Amended 2026-10-25: each mesh's listener shows only that mesh.

## Context

The office router reported about a terabyte a day. Nothing could say how much
of it was shrooms or which part: tunnels between peers, the delivery node
(which a Core node runs for the whole cluster), or something else on the
same machines.

## Decision

**Every daemon serves Prometheus metrics on its own mesh addresses**, at
`metrics_port` (default 9180, 0 for off):

- per-peer tunnel bytes, by mesh;
- the delivery node's own counters, from the library's metrics server on a
  free loopback port, renamed to `shrooms_delivery_*`;
- the machine's interface counters, because the router's figure is about
  the machine, not about shrooms.

The listener binds only overlay addresses, so a member can scrape and the
internet cannot. It re-listens every 15 seconds, because meshes come and go.

**The scraper finds nodes through the mesh.** `/targets` serves Prometheus'
`http_sd` format, listing the node and its peers. One URL is enough to
configure, and nodes that join later are picked up.

**Prometheus and Grafana run on one node**, from `deploy/metrics/install.sh`.
Grafana stays on loopback and is published as a shrooms service on every
mesh the node is on. Anonymous viewing is on: the mesh is the boundary
(ADR-041).

Settled with it: the services endpoint now writes to the mesh it is told,
and a node on several meshes without a top-level one refuses a write that
names no mesh. Before, such a write went to the top-level `services` list,
which belongs to no mesh there. It was accepted, read back correctly, and
never published. That is how Grafana on the VPS first stayed invisible.

## Consequences

- Hand-written text format: no client library. Labels are escaped, and
  values are written in plain decimal rather than exponent form, so a large
  counter reads the same to a person with curl as to Prometheus.
- Mesh labels are local, so the same mesh can carry two labels across nodes.
- Nodes need the metrics port open on their mesh interface (firewalld did
  block it on three nodes).
- Phones serve no metrics yet. The dashboard sees their traffic only as the
  peer counters of whoever they talk to.

## Amendment, 2026-10-25: each mesh sees only itself

As built, every listener served one page for all of a node's meshes, and
`/targets` listed every mesh's machines. A member of one mesh could read the
names, overlay addresses and traffic of every other mesh the node was on.
That undid what per-mesh identities are for ([ADR-015](015-multiple-meshes-one-daemon.md)):
joining a shared mesh is meant to reveal nothing about a private one. Found
on 2026-10-25, while checking shrooms against a report on Tailscale's
serve and SSH. That report's finding 11 is the same class: one mount's
context reached another's.

**A mesh's listener now serves that mesh.**
- **Its peers**, and what this node sent on it.
- **The machine's own figures:** host, process and the delivery node. The
  interface list leaves out the other meshes' tunnel interfaces.
- **`/targets`** lists that mesh's machines.
- **The machine itself is the exception.** Asking from loopback or its own
  overlay address, it gets every machine on every mesh. That's the scraper
  `deploy/metrics` runs beside it. A peer can't send from those addresses,
  because WireGuard accepts from a peer only its own.

Since a machine on two meshes is now two targets, `/targets` marks one per
machine `first="1"`. Prometheus keeps the machine's own series and its agent
from that target only, so dashboards that sum by node are unchanged.

What a member can still learn is about the machine, not the other meshes:
its load, memory and physical interfaces' traffic. Correlating those
figures across two meshes could suggest that two devices are one machine.
That is weaker than reading the other mesh's roster, which is what was
fixed.

