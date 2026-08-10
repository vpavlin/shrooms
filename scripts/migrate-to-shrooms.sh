#!/usr/bin/env bash
# Move a container deployment from the logos-vpn names to the shrooms ones.
#
#   ./scripts/migrate-to-shrooms.sh user@host
#
# Run on a node installed by install.sh or deploy.sh before the rename. It is
# idempotent and safe to re-run: a node already migrated is left alone.
#
# The device identity in /var/lib is MOVED, never recreated. A node that came
# back with a fresh identity would have a new overlay address, and every peer
# would have to learn it — which is the one outcome this whole exercise exists
# to avoid.
set -euo pipefail

HOST=${1:-}
[ -n "$HOST" ] || { echo "usage: $0 user@host"; exit 1; }

echo "==> migrating $HOST"

ssh "$HOST" 'bash -s' <<'REMOTE'
set -euo pipefail
S=""; [ "$(id -u)" -eq 0 ] || S=sudo

if [ ! -d /etc/logos-vpn ] && [ -d /etc/shrooms ]; then
    echo "    already migrated"
    exit 0
fi

# Stop the old unit first: moving a state directory under a running daemon is
# how you get a half-written state.json.
if systemctl list-unit-files logos-vpn.service >/dev/null 2>&1; then
    $S systemctl disable --now logos-vpn.service 2>/dev/null || true
fi
$S docker rm -f logos-vpn 2>/dev/null || true

for d in etc/logos-vpn:etc/shrooms var/lib/logos-vpn:var/lib/shrooms; do
    from=/${d%%:*}; to=/${d##*:}
    if [ -d "$from" ] && [ ! -d "$to" ]; then
        $S mv "$from" "$to"
        echo "    $from -> $to"
    fi
done

# The image is tagged under the old name on hosts updated before the rename;
# tag it under the new one rather than shipping 70MB again.
#
# Only when there is no shrooms image already: a newer one may have been pushed
# here first, and re-tagging would quietly point the new name at the older
# image — a downgrade that looks like a successful migration.
if ! $S docker image inspect ghcr.io/vpavlin/shrooms:latest >/dev/null 2>&1; then
    if $S docker image inspect ghcr.io/vpavlin/logos-vpn:latest >/dev/null 2>&1; then
        $S docker tag ghcr.io/vpavlin/logos-vpn:latest ghcr.io/vpavlin/shrooms:latest
    fi
fi

$S mkdir -p /run/shrooms

$S tee /etc/systemd/system/shrooms.service >/dev/null <<'UNIT'
[Unit]
Description=shrooms overlay mesh
Documentation=https://github.com/vpavlin/shrooms
After=docker.service network-online.target
Wants=network-online.target

[Service]
ExecStartPre=-/usr/bin/docker rm -f shrooms
ExecStart=/usr/bin/docker run --rm --name shrooms \
    --network host \
    --cap-add NET_ADMIN \
    --device /dev/net/tun \
    -v /etc/shrooms:/etc/shrooms \
    -v /var/lib/shrooms:/var/lib/shrooms \
    -v /run/shrooms:/run/shrooms \
    ghcr.io/vpavlin/shrooms:latest daemon --socket /run/shrooms/shrooms.sock
ExecStop=/usr/bin/docker stop shrooms
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

$S tee /usr/local/bin/shrooms >/dev/null <<'WRAP'
#!/bin/sh
# Thin wrapper: run the CLI inside the running node container.
if ! docker inspect -f '{{.State.Running}}' shrooms 2>/dev/null | grep -q true; then
    echo "the shrooms container is not running" >&2
    echo "  sudo systemctl status shrooms" >&2
    exit 1
fi
exec docker exec shrooms shrooms "$@"
WRAP
$S chmod 755 /usr/local/bin/shrooms
$S rm -f /usr/local/bin/logos-vpn /etc/systemd/system/logos-vpn.service

$S systemctl daemon-reload
$S systemctl enable --now shrooms.service
sleep 6
systemctl is-active shrooms.service
REMOTE

echo "==> done; check with: ssh $HOST 'sudo shrooms status'"
