# 046. Metrics on the mesh addresses

**Status:** accepted 2026-10-09, built the same day; see [docs/metrics.md](../metrics.md)

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
