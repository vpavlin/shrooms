#!/usr/bin/env bash
# Prometheus and Grafana for a shrooms mesh (docs/metrics.md), on one node of
# it — one on every mesh you want to see is best: it scrapes the peers it can
# reach.
#
#   sudo bash deploy/metrics/install.sh            # on the monitoring node
#   sudo bash deploy/metrics/install.sh --uninstall
#
# - Prometheus on 127.0.0.1:9090, 90 days kept, finding the nodes through this
#   node's own /targets (it and every peer, once each).
# - Grafana on 127.0.0.1:3000, published to the mesh as the shrooms service
#   "grafana": http://grafana.<this node>.<mesh>.mesh from any member,
#   unreachable from anywhere else. Anyone on the mesh may look; editing
#   needs the admin password this prints once (kept in
#   /etc/shrooms-metrics/grafana-admin-password).
# - Both as podman (or docker) containers on the host's network, run by
#   systemd: shrooms-prometheus.service, shrooms-grafana.service.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ETC=/etc/shrooms-metrics
DATA=/var/lib/shrooms-metrics
SOCK=${SOCK:-/run/shrooms/shrooms.sock}
PROM_IMAGE=${PROM_IMAGE:-docker.io/prom/prometheus:latest}
GRAFANA_IMAGE=${GRAFANA_IMAGE:-docker.io/grafana/grafana-oss:latest}

[ "$(id -u)" = 0 ] || { echo "run as root (sudo)"; exit 1; }
ENGINE=$(command -v podman || command -v docker || true)
[ -n "$ENGINE" ] || { echo "needs podman or docker"; exit 1; }

shrooms_cli() {
    # The daemon runs natively or in its container; ask whichever there is.
    if command -v shrooms >/dev/null; then shrooms "$@"
    else "$ENGINE" exec shrooms shrooms "$@"; fi
}

if [ "${1:-}" = "--uninstall" ]; then
    systemctl disable --now shrooms-grafana shrooms-prometheus 2>/dev/null || true
    rm -f /etc/systemd/system/shrooms-grafana.service /etc/systemd/system/shrooms-prometheus.service
    systemctl daemon-reload
    shrooms_cli services remove grafana 2>/dev/null || true
    echo "removed; data kept in $DATA, settings in $ETC"
    exit 0
fi

# Where this node's metrics are: its first mesh address.
OVERLAY=$(curl -s --unix-socket "$SOCK" http://shrooms/status | python3 -c '
import json,sys
d=json.load(sys.stdin)
print((d.get("meshes") or [{}])[0].get("overlay") or d.get("overlay",""))')
[ -n "$OVERLAY" ] || { echo "no mesh address from the daemon at $SOCK"; exit 1; }
PORT=$(python3 -c 'import re,sys
s=open("/etc/shrooms/config.toml").read() if __import__("os").path.exists("/etc/shrooms/config.toml") else ""
m=re.search(r"^metrics_port\s*=\s*(\d+)", s, re.M); print(m.group(1) if m else 9180)')
TARGETS="http://[$OVERLAY]:$PORT/targets"
curl -sf -m 5 "$TARGETS" >/dev/null || { echo "this node does not serve $TARGETS (a shrooms with metrics, metrics_port not 0?)"; exit 1; }

mkdir -p "$ETC" "$DATA/prometheus" "$DATA/grafana"
sed "s|SHROOMS_TARGETS|$TARGETS|" "$HERE/prometheus.yml.in" > "$ETC/prometheus.yml"
rm -rf "$ETC/grafana" && cp -r "$HERE/grafana" "$ETC/grafana"
cp "$HERE/grafana.ini" "$ETC/grafana.ini"
if [ ! -s "$ETC/grafana-admin-password" ]; then
    head -c 18 /dev/urandom | base64 | tr -d '/+=' > "$ETC/grafana-admin-password"
    chmod 600 "$ETC/grafana-admin-password"
    NEWPW=1
fi
# The containers' own users write their data.
chown -R 65534:65534 "$DATA/prometheus"
chown -R 472:0 "$DATA/grafana"

cat > /etc/systemd/system/shrooms-prometheus.service <<EOF
[Unit]
Description=Prometheus for the shrooms mesh (docs/metrics.md)
After=network-online.target shrooms.service
Wants=network-online.target
[Service]
ExecStartPre=-$ENGINE rm -f shrooms-prometheus
ExecStart=$ENGINE run --rm --name shrooms-prometheus --network host \\
    -v $ETC/prometheus.yml:/etc/prometheus/prometheus.yml:ro,z \\
    -v $DATA/prometheus:/prometheus:z \\
    $PROM_IMAGE --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus \\
    --storage.tsdb.retention.time=90d --web.listen-address=127.0.0.1:9090
ExecStop=$ENGINE stop -t 10 shrooms-prometheus
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
cat > /etc/systemd/system/shrooms-grafana.service <<EOF
[Unit]
Description=Grafana for the shrooms mesh (docs/metrics.md)
After=network-online.target shrooms-prometheus.service
Wants=network-online.target
[Service]
ExecStartPre=-$ENGINE rm -f shrooms-grafana
ExecStart=$ENGINE run --rm --name shrooms-grafana --network host \\
    -v $ETC/grafana.ini:/etc/grafana/grafana.ini:ro,z \\
    -v $ETC/grafana/provisioning:/etc/grafana/provisioning:ro,z \\
    -v $ETC/grafana/dashboards:/etc/grafana/dashboards:ro,z \\
    -v $DATA/grafana:/var/lib/grafana:z \\
    -e GF_SECURITY_ADMIN_PASSWORD__FILE=/etc/grafana/admin-password \\
    -v $ETC/grafana-admin-password:/etc/grafana/admin-password:ro,z \\
    $GRAFANA_IMAGE
ExecStop=$ENGINE stop -t 10 shrooms-grafana
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
"$ENGINE" pull -q "$PROM_IMAGE" >/dev/null
"$ENGINE" pull -q "$GRAFANA_IMAGE" >/dev/null
systemctl daemon-reload
systemctl enable --now shrooms-prometheus shrooms-grafana
systemctl restart shrooms-prometheus shrooms-grafana

shrooms_cli services add grafana --to 127.0.0.1:3000 >/dev/null 2>&1 || shrooms_cli services list | grep -q grafana || \
    echo "could not publish grafana as a shrooms service: shrooms services add grafana --to 127.0.0.1:3000"

echo "Prometheus: 127.0.0.1:9090, scraping what $TARGETS lists"
echo "Grafana:    http://grafana.<this node>.<mesh>.mesh  (shrooms services list shows the name)"
[ -n "${NEWPW:-}" ] && echo "Grafana admin password (also in $ETC/grafana-admin-password): $(cat "$ETC/grafana-admin-password")"
exit 0
