#!/usr/bin/env bash
# Ship logos-vpn to a remote host and run it.
#
#   ./scripts/deploy.sh user@vps.example.com                    # join an existing mesh
#   ./scripts/deploy.sh user@vps --init                         # create a new mesh
#   ./scripts/deploy.sh user@vps --advertise 203.0.113.4:51820  # publicly reachable node
#   ./scripts/deploy.sh user@vps --relay                        # also relay for others
#
# Ships a container image rather than a binary. liblogosdelivery requires
# glibc 2.38, so a tarball fails on Debian 12 (2.36) and works only on Ubuntu
# 24.04+ (2.39). The image carries its own userland and works anywhere docker
# does.
#
# What this touches on the remote host:
#   /etc/logos-vpn/       config
#   /var/lib/logos-vpn/   device identity and announce sequence number
#   /run/logos-vpn/       control socket
#   a `logos-vpn` container, and a TUN interface
#
# Nothing is destroyed: an existing config is left alone unless you pass --force.
set -euo pipefail

cd "$(dirname "$0")/.."

HOST=${1:-}
[ -n "$HOST" ] || { echo "usage: $0 user@host [--init] [--relay] [--advertise IP:PORT] [--name NAME] [--key KEY] [--force]"; exit 1; }
shift

INIT=0
FORCE=0
RELAY=0
ADVERTISE=""
NAME=""
KEY="${LOGOS_VPN_KEY:-}"

while [ $# -gt 0 ]; do
    case "$1" in
        --init)      INIT=1; shift ;;
        --relay)     RELAY=1; shift ;;
        --force)     FORCE=1; shift ;;
        --advertise) ADVERTISE=$2; shift 2 ;;
        --name)      NAME=$2; shift 2 ;;
        --key)       KEY=$2; shift 2 ;;
        *) echo "unknown option $1"; exit 1 ;;
    esac
done

LD_LIB=${LD_LIB:-docker/build/lib}
IMAGE=logos-vpn:latest

echo "==> checking the remote host"
ssh "$HOST" 'command -v docker >/dev/null || { echo "docker is not installed"; exit 1; }
             [ -e /dev/net/tun ] || { echo "no /dev/net/tun"; exit 1; }
             echo "  $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME"), docker $(docker version --format "{{.Server.Version}}" 2>/dev/null)"'

# Pull from the registry when we can: building and shipping the image is by far
# the slowest step, and CI publishes one on every push to master.
REGISTRY_IMAGE=${REGISTRY_IMAGE:-ghcr.io/vpavlin/logos-vpn:latest}
if [ "${BUILD_IMAGE:-0}" != "1" ] && \
   ssh "$HOST" "sudo docker pull -q $REGISTRY_IMAGE >/dev/null 2>&1 && sudo docker tag $REGISTRY_IMAGE $IMAGE"; then
    echo "==> pulled $REGISTRY_IMAGE on the remote host (BUILD_IMAGE=1 to build locally instead)"
    SKIP_SHIP=1
else
    SKIP_SHIP=0
fi

if [ "$SKIP_SHIP" = "0" ]; then
echo "==> building image"
[ -f "$LD_LIB/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so in $LD_LIB — run 'make deps-release'"; exit 1; }
make logos-vpn >/dev/null

CTX=docker/build/deploy
rm -rf "$CTX"; mkdir -p "$CTX/lib"
cp bin/logos-vpn "$CTX/"
cp docker/gateway.sh docker/entrypoint-nat.sh "$CTX/" 2>/dev/null || true
cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$CTX/lib/" 2>/dev/null || true
docker build -q -t "$IMAGE" -f docker/Dockerfile "$CTX" >/dev/null
echo "  $(docker image inspect "$IMAGE" --format '{{.Size}}' | awk '{printf "%.0f MB\n", $1/1048576}')"

echo "==> shipping image (this is the slow part)"
docker save "$IMAGE" | gzip -1 | ssh "$HOST" 'gunzip | docker load' | tail -1
fi

echo "==> preparing directories"
ssh "$HOST" 'sudo mkdir -p /etc/logos-vpn /var/lib/logos-vpn /run/logos-vpn
             sudo chmod 700 /etc/logos-vpn /var/lib/logos-vpn'

# Generate the config locally so the network key never has to be typed on the
# remote host, then copy it over.
if ssh "$HOST" 'sudo test -f /etc/logos-vpn/config.toml' && [ $FORCE -eq 0 ]; then
    echo "==> config already present on $HOST, leaving it alone (use --force to replace)"
else
    echo "==> generating config"
    TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
    ARGS=(--config "$TMP/config.toml" --state "$TMP/state")
    [ -n "$NAME" ] && ARGS+=(--name "$NAME")
    [ -n "$ADVERTISE" ] && ARGS+=(--advertise "$ADVERTISE")

    if [ $INIT -eq 1 ]; then
        ./bin/logos-vpn init "${ARGS[@]}"
    else
        [ -n "$KEY" ] || { echo "need a network key: pass --key KEY, set LOGOS_VPN_KEY, or use --init"; exit 1; }
        ./bin/logos-vpn join "$KEY" "${ARGS[@]}"
    fi

    if [ $RELAY -eq 1 ]; then
        # A reachable node should relay: it is what lets two NATed peers reach
        # each other when punching fails.
        echo 'relay = "true"' >> "$TMP/config.toml"
    fi

    # Ship the config; the remote generates its own device identity on first
    # start, so no private key ever crosses the wire.
    #
    # Piped over ssh stdin rather than scp'd to /tmp. The config carries the
    # network key, which is a bearer credential — anyone holding it is a member
    # — and staging it at a predictable path in shared /tmp left it readable by
    # every local user on the remote for the window between landing and chmod.
    # `install -m600` creates it with the right mode from the start, so there is
    # no window at all.
    ssh "$HOST" 'sudo install -d -m 755 /etc/logos-vpn &&
                 sudo install -m 600 /dev/stdin /etc/logos-vpn/config.toml' \
        < "$TMP/config.toml"
fi

echo "==> installing compose file"
ssh "$HOST" 'sudo install -d -m 755 /etc/logos-vpn &&
             sudo install -m 644 /dev/stdin /etc/logos-vpn/compose.yml' \
    < docker/compose-node.yml

echo "==> starting"
ssh "$HOST" 'cd /etc/logos-vpn && sudo docker compose -f compose.yml up -d --force-recreate' 2>&1 | tail -3

echo
echo "==> status (give it ~20s to reach the fleet)"
sleep 20
ssh "$HOST" 'sudo docker exec logos-vpn logos-vpn status --socket /run/logos-vpn/logos-vpn.sock' 2>&1 | head -12 || \
    ssh "$HOST" 'sudo docker logs logos-vpn 2>&1 | grep "^time=" | tail -10'

cat <<EOF

Useful on $HOST:
  sudo docker exec logos-vpn logos-vpn status --socket /run/logos-vpn/logos-vpn.sock
  sudo docker exec logos-vpn logos-vpn paths  --socket /run/logos-vpn/logos-vpn.sock
  sudo docker logs -f logos-vpn

If this node is publicly reachable, open its UDP port:
  sudo ufw allow 51820/udp        # or the equivalent for your firewall
EOF
