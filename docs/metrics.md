# Metrics

Every shrooms daemon serves Prometheus metrics on its own mesh addresses, at
`metrics_port` (9180 by default; `metrics_port = 0` turns them off). Members of
the mesh can scrape them. Nobody else can reach them. Decided in
[ADR-046](adr/046-metrics-on-the-mesh-addresses.md).

Each mesh address shows that mesh only: its peers, what was sent on it, and
the machine's own figures without the other meshes' interfaces. A member of
one mesh never reads another mesh's names, addresses or traffic. Device keys
are per mesh so that a machine's meshes can't be linked
([ADR-015](adr/015-multiple-meshes-one-daemon.md)).

They answer one question: *where does the traffic come from?* There are three
candidates, so there are three kinds of metric.

## What a node exports

| Metric | Labels | What it is |
|---|---|---|
| `shrooms_peer_receive_bytes_total`, `shrooms_peer_transmit_bytes_total` | `mesh`, `peer` | What one peer's WireGuard session carried. |
| `shrooms_peer_up`, `shrooms_peer_relayed`, `shrooms_peer_announcing` | `mesh`, `peer` | Whether the tunnel is up, whether it goes through a relay, whether the peer announces. |
| `shrooms_peer_rtt_seconds` | `mesh`, `peer` | Last measured round trip. |
| `shrooms_mesh_peers` | `mesh` | Peers known on each mesh. |
| `shrooms_delivery_bytes_total` | `direction` | What the delivery (rendezvous) node exchanged. A Core node does this for the whole cluster, not only for itself. |
| `shrooms_delivery_peers`, `shrooms_delivery_messages_total` | `type` | Delivery node peers and messages. |
| `shrooms_delivery_sent_messages_total`, `shrooms_delivery_sent_bytes_total`, `shrooms_delivery_send_failures_total` | `mesh`, `kind` | What this node itself published on the rendezvous plane, not what it relayed. Bytes are payload bytes as handed to the delivery node; the wire adds the envelope. |
| `shrooms_delivery_sent_message_bytes` (histogram) | `mesh`, `kind`, `le` | The payload size of each published message. |
| `shrooms_delivery_sent_peak_per_second` | | The most messages this node published in any one second over the last 10 minutes, all meshes together. |
| `shrooms_host_receive_bytes_total`, `shrooms_host_transmit_bytes_total` | `interface` | Every interface on the machine except `lo`, from `/proc/net/dev`. This is what a router sees, shrooms or not. |
| `shrooms_process_resident_bytes` | | The daemon's memory. |
| `shrooms_host_cpu_seconds_total` | `mode` | All the machine's CPUs' time, by mode, from `/proc/stat`. |
| `shrooms_host_cpus` | | Its CPUs. |
| `shrooms_host_load` | `window` (1m, 5m, 15m) | Its load average. |
| `shrooms_host_memory_bytes` | `kind` (total, available) | Its memory, from `/proc/meminfo`. |
| `shrooms_host_disk_bytes` | `kind` (size, free) | Its root filesystem. |
| `shrooms_info` | `name`, `mode`, `version` | Always 1. |

Mesh labels are each device's own ([mesh-labels-are-local.md](mesh-labels-are-local.md)),
so one mesh can appear under two labels when two nodes named it differently.

The delivery figures come from the delivery library's own metrics server. It
listens on a loopback port that the kernel hands out as free, and shrooms
passes its output through under new names. A fixed port broke a second node
on the same machine, and a delivery node cannot be recreated in a running
process, so retrying is not an option.

The `sent` figures are for sizing RLN's per-publisher rate limits. The
delivery team asked what an application like this actually sends
(2026-10-21), and the delivery node's own counters can't say: they count
everything it relays. They are counted at shrooms' own publishes instead.
The `kind`s are:
- `announce`: every 45 s per mesh, plus one in answer to a peer that
  restarted;
- `rekey`: the announce generation after a revocation;
- `relay_advice`, `services`;
- `revocation`, `grant`: admin actions relayed;
- `invite_answer`.

Probes travel over WireGuard, not delivery, and aren't counted.

`/targets` on the same port lists, in Prometheus' `http_sd` format, this node
and its peers on the mesh it was asked on. Asked by this machine itself (the
scraper of `deploy/metrics` beside it), it lists every machine on every mesh,
once per mesh, and labels one target per machine `first="1"`. Prometheus keeps
a machine's own series (`shrooms_host_*`, `shrooms_process_*`, the delivery
node's) and its agent from that target only, so nothing is counted twice
(`deploy/metrics/prometheus.yml.in`). A scraper is told about one node and
finds the rest. Peers without metrics (phones, older daemons) show as down.

## What an agent exports

Every shrooms-agent serves `/metrics` on its own port (7387), on the same
addresses as its API. Prometheus finds the agents through the same
`/targets` as the daemons, on that port (the `shrooms-agents` job).

| Metric | Labels | What it is |
|---|---|---|
| `shrooms_agent_tokens_total` | `machine`, `session`, `harness`, `model`, `provider`, `kind` (input, cache_read, cache_write, output) | The tokens a session's model took and wrote, from its usage. |
| `shrooms_agent_turns_total`, `shrooms_agent_cost_usd_total`, `shrooms_agent_busy_seconds_total` | as above, without `kind` | Turns, what they cost as the harness prices them (0 for a local model), and the time from asking to the answer. |
| `shrooms_agent_tokens_per_second` | `machine`, `session`, `harness`, `model`, `provider` | How fast the model wrote in the session's last turn. |
| `shrooms_agent_sessions` | `machine`, `state` | Sessions by state. |

The provider is the part of a model's name before the slash, as pi names
models (`venice/…`, `k11/…`), and `anthropic` for Claude Code. A session
limited to some meshes (ADR-049) is counted only for a scraper on one of
them.

## A scraper and a dashboard

On one node, ideally one that is on every mesh:

```sh
sudo bash deploy/metrics/install.sh
```

This sets up:

- Prometheus on `127.0.0.1:9090`, keeping 90 days and finding nodes through
  this node's own `/targets`;
- Grafana on `127.0.0.1:3000`, published as the shrooms service `grafana` on
  every mesh this node is on, so it is `http://grafana.<node>.<mesh>.mesh`.
  Anyone on the mesh can view it. Editing needs the admin password, which the
  script prints once and keeps in `/etc/shrooms-metrics/grafana-admin-password`;
- both as podman (or docker) containers on the host network, run by systemd
  as `shrooms-prometheus.service` and `shrooms-grafana.service`.

`--uninstall` removes them and keeps the data.

The dashboard ("shrooms — where the bytes go", generated by `deploy/metrics/make-dashboard.py`)
has three rows:

- **Machines:** physical interfaces only (tunnels, bridges and veths are left
  out), as rates and as 24-hour totals;
- **Delivery:** the delivery node's traffic, and its share of the machine's;
- **Tunnels:** per mesh and per peer, direct or relayed.

### Firewalls

The metrics port has to be open on the mesh interface. On firewalld, allow it
for the node's own overlay addresses:

```sh
sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv6 destination address=<overlay>/128 port port=9180 protocol=tcp accept'
sudo firewall-cmd --reload
```

## Running it

The office runs it on the VPS: <http://grafana.vps.office.mesh> (or
`grafana.vps.home.mesh`).

The first look, on 2026-10-09:

- **The terabyte a day was atlas's Logos blockchain node** (`logoscore.service`),
  not shrooms. Atlas's interface moved about 970 GB in its first 26 hours
  (366 GB in, 603 GB out). That is about 10 MB/s, steady.
- **shrooms tunnels** carry kilobytes per peer.
- **The VPS's delivery node** (Core) moves about 2 MB/s.
- **The Edge nodes' delivery** is about 10 kB/s.
- **The rest of the VPS's ~4 MB/s** comes from two other Logos `delivery_module`
  hosts running there (scala-hub2, swamp-hub).
