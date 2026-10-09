#!/usr/bin/env python3
"""Writes grafana/dashboards/shrooms-traffic.json (docs/metrics.md).

Generated rather than hand-edited JSON, so the queries stay readable here and
consistent with each other: one definition of "a physical interface".
"""
import json, pathlib

# Interfaces that are not the machine's own wire: the mesh's tunnels, container
# and VM bridges. What is left is what a router sees.
VIRTUAL = '(logos|shrooms|docker|veth|br-|virbr|podman|cni|vnet|tun|tap|wg|flannel|lxc|tailscale).*'
PHYS = f'interface!~"{VIRTUAL}"'

panels, y = [], 0

def row(title):
    global y
    panels.append({"type": "row", "title": title, "collapsed": False, "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []})
    y += 1

def panel(kind, title, targets, w, h, x=0, unit="Bps", desc="", extra=None):
    p = {"type": kind, "title": title, "description": desc, "datasource": {"type": "prometheus", "uid": "prometheus"},
         "gridPos": {"h": h, "w": w, "x": x, "y": y},
         "fieldConfig": {"defaults": {"unit": unit}, "overrides": []},
         "targets": [{"refId": chr(65 + i), "expr": e, "legendFormat": l, "datasource": {"type": "prometheus", "uid": "prometheus"}}
                     for i, (e, l) in enumerate(targets)]}
    if kind == "bargauge":
        p["options"] = {"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                        "reduceOptions": {"calcs": ["lastNotNull"], "values": False}}
    if kind == "stat":
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "values": False}, "colorMode": "value", "graphMode": "none"}
    if kind == "timeseries":
        p["fieldConfig"]["defaults"]["custom"] = {"fillOpacity": 10, "lineWidth": 1}
    if extra:
        p.update(extra)
    panels.append(p)

def advance(h):
    global y
    y += h

row("Machines — what the router sees (physical interfaces)")
panel("timeseries", "Traffic per machine",
      [(f'sum by (node) (rate(shrooms_host_receive_bytes_total{{{PHYS}}}[5m]))', "{{node}} in"),
       (f'-sum by (node) (rate(shrooms_host_transmit_bytes_total{{{PHYS}}}[5m]))', "{{node}} out")],
      16, 9, desc="Every physical interface of each machine, in (up) and out (down). The sum over machines on one LAN is roughly what its router counts.")
panel("bargauge", "Last 24 h per machine (in + out)",
      [(f'sum by (node) (increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "{{node}}")],
      8, 9, x=16, unit="bytes")
advance(9)
panel("stat", "All machines, last 24 h",
      [(f'sum(increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "total")],
      6, 4, unit="bytes")
panel("stat", "…of which delivery (rendezvous plane)",
      [('sum(increase(shrooms_delivery_bytes_total[24h]))', "delivery")], 6, 4, x=6, unit="bytes")
panel("stat", "…of which shrooms tunnels",
      [('sum(increase(shrooms_peer_receive_bytes_total[24h]) + increase(shrooms_peer_transmit_bytes_total[24h]))', "tunnels")],
      6, 4, x=12, unit="bytes", desc="Counted at both ends of each tunnel, and riding the physical interfaces of each.")
panel("stat", "Delivery share of physical traffic",
      [(f'sum(increase(shrooms_delivery_bytes_total[24h])) / sum(increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "share")],
      6, 4, x=18, unit="percentunit")
advance(4)

row("Delivery — the rendezvous plane (a Core node relays for the whole cluster)")
panel("timeseries", "Delivery traffic per machine",
      [('sum by (node) (rate(shrooms_delivery_bytes_total{direction="in"}[5m]))', "{{node}} in"),
       ('-sum by (node) (rate(shrooms_delivery_bytes_total{direction="out"}[5m]))', "{{node}} out")], 16, 9)
panel("bargauge", "Delivery, last 24 h per machine",
      [('sum by (node) (increase(shrooms_delivery_bytes_total[24h]))', "{{node}}")], 8, 9, x=16, unit="bytes")
advance(9)
panel("timeseries", "Delivery peers",
      [('shrooms_delivery_peers', "{{node}}")], 12, 6, unit="short")
panel("timeseries", "Delivery messages per second",
      [('sum by (node) (rate(shrooms_delivery_messages_total[5m]))', "{{node}}")], 12, 6, unit="short")
advance(6)

row("Tunnels — shrooms between peers")
panel("timeseries", "Tunnel traffic per peer (as each machine counts it)",
      [('sum by (node, mesh, peer) (rate(shrooms_peer_receive_bytes_total[5m]) + rate(shrooms_peer_transmit_bytes_total[5m]))', "{{node}} ↔ {{peer}} ({{mesh}})")],
      16, 9)
panel("bargauge", "Tunnels, last 24 h per mesh",
      [('sum by (mesh) (increase(shrooms_peer_receive_bytes_total[24h]) + increase(shrooms_peer_transmit_bytes_total[24h]))', "{{mesh}}")],
      8, 9, x=16, unit="bytes", desc="Each tunnel counted at both ends.")
advance(9)
panel("table", "Peers now",
      [('shrooms_peer_up', "up"), ('shrooms_peer_relayed', "relayed")], 24, 8, unit="short",
      extra={"transformations": [{"id": "seriesToColumns", "options": {"byField": "peer"}}],
             "targets": [{"refId": "A", "expr": 'shrooms_peer_up', "format": "table", "instant": True},
                         {"refId": "B", "expr": 'shrooms_peer_relayed', "format": "table", "instant": True}]})
for p in panels:
    if p.get("type") == "table":
        for t in p["targets"]:
            t["datasource"] = {"type": "prometheus", "uid": "prometheus"}
        p["transformations"] = [{"id": "merge", "options": {}},
                                {"id": "organize", "options": {"excludeByName": {"Time": True, "__name__": True, "job": True, "instance": True},
                                                                "renameByName": {"Value #A": "up", "Value #B": "relayed"}}}]
advance(8)

dash = {"uid": "shrooms-traffic", "title": "shrooms — where the bytes go", "tags": ["shrooms"], "timezone": "browser",
        "schemaVersion": 39, "version": 1, "refresh": "1m", "time": {"from": "now-24h", "to": "now"}, "panels": panels}
out = pathlib.Path(__file__).parent / "grafana" / "dashboards" / "shrooms-traffic.json"
out.write_text(json.dumps(dash, indent=1, ensure_ascii=False) + "\n")
print("wrote", out)
