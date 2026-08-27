#!/usr/bin/env bash
# Ship shrooms to a remote host and run it.
#
#   ./scripts/deploy.sh user@vps.example.com                    # deploy, then join by invite
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
#   /etc/shrooms/       config
#   /var/lib/shrooms/   device identity and announce sequence number
#   /run/shrooms/       control socket
#   a `shrooms` container, and a TUN interface
#
# Nothing is destroyed: an existing config is left alone unless you pass --force.
set -euo pipefail

cd "$(dirname "$0")/.."

HOST=${1:-}
[ -n "$HOST" ] || { echo "usage: $0 user@host [--init] [--relay] [--advertise IP:PORT] [--name NAME] [--force]"; exit 1; }
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
        # Kept because scripts use it, and warned about below: an argument is
        # visible in `ps` to every user on the machine and lands in the shell
        # history of whoever ran it. LOGOS_VPN_KEY, or an invite, avoids both.
        --key)       KEY=$2; KEY_ON_ARGV=1; shift 2 ;;
        *) echo "unknown option $1"; exit 1 ;;
    esac
done

LD_LIB=${LD_LIB:-docker/build/lib}
IMAGE=shrooms:latest

echo "==> checking the remote host"
ssh "$HOST" 'command -v docker >/dev/null || { echo "docker is not installed"; exit 1; }
             [ -e /dev/net/tun ] || { echo "no /dev/net/tun"; exit 1; }
             echo "  $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME"), docker $(docker version --format "{{.Server.Version}}" 2>/dev/null)"'

# Pull from the registry when we can: building and shipping the image is by far
# the slowest step, and CI publishes one on every push to master.
REGISTRY_IMAGE=${REGISTRY_IMAGE:-ghcr.io/vpavlin/shrooms:latest}
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
make shrooms >/dev/null

CTX=docker/build/deploy
rm -rf "$CTX"; mkdir -p "$CTX/lib"
cp bin/shrooms "$CTX/"
cp docker/gateway.sh docker/entrypoint-nat.sh "$CTX/" 2>/dev/null || true
cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$CTX/lib/" 2>/dev/null || true
docker build -q -t "$IMAGE" -f docker/Dockerfile "$CTX" >/dev/null
echo "  $(docker image inspect "$IMAGE" --format '{{.Size}}' | awk '{printf "%.0f MB\n", $1/1048576}')"

echo "==> shipping image (this is the slow part)"
docker save "$IMAGE" | gzip -1 | ssh "$HOST" 'gunzip | docker load' | tail -1
fi

echo "==> preparing directories"
ssh "$HOST" 'sudo mkdir -p /etc/shrooms /var/lib/shrooms /run/shrooms
             sudo chmod 700 /etc/shrooms /var/lib/shrooms'

# Generate the config locally so the network key never has to be typed on the
# remote host, then copy it over.
if ssh "$HOST" 'sudo test -f /etc/shrooms/config.toml' && [ $FORCE -eq 0 ]; then
    echo "==> config already present on $HOST, leaving it alone (use --force to replace)"
else
    echo "==> generating config"
    TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
    ARGS=(--config "$TMP/config.toml" --state "$TMP/state")
    [ -n "$NAME" ] && ARGS+=(--name "$NAME")
    [ -n "$ADVERTISE" ] && ARGS+=(--advertise "$ADVERTISE")

    if [ $INIT -eq 1 ]; then
        ./bin/shrooms init "${ARGS[@]}"
    else
        # A config with NO key, joined by invite once it is running.
        #
        # This used to build the config around a network key — from --key or
        # LOGOS_VPN_KEY — and ship it. That was the prototype path: the key was
        # the membership, so the file crossing the wire was a bearer credential
        # for the whole mesh, and every warning below it was about damage
        # control. `shrooms join <KEY>` was removed on 2026-08-27 and this went
        # with it.
        #
        # What ships now holds a name, a port and nothing secret at all.
        if [ -n "$KEY" ]; then
            echo "note: --key and LOGOS_VPN_KEY are ignored and no longer needed."
            echo "      This deploys a keyless config and joins by invite; the key"
            echo "      never leaves the machine that holds it."
            echo
        fi
        ./bin/shrooms prepare "${ARGS[@]}"
        JOIN_BY_INVITE=1
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
    ssh "$HOST" 'sudo install -d -m 755 /etc/shrooms &&
                 sudo install -m 600 /dev/stdin /etc/shrooms/config.toml' \
        < "$TMP/config.toml"
fi

echo "==> installing compose file"
ssh "$HOST" 'sudo install -d -m 755 /etc/shrooms &&
             sudo install -m 644 /dev/stdin /etc/shrooms/compose.yml' \
    < docker/compose-node.yml

echo "==> starting"
ssh "$HOST" 'cd /etc/shrooms && sudo docker compose -f compose.yml up -d --force-recreate' 2>&1 | tail -3

echo
echo "==> status (give it ~20s to reach the fleet)"
sleep 20
ssh "$HOST" 'sudo docker exec shrooms shrooms status --socket /run/shrooms/shrooms.sock' 2>&1 | head -12 || \
    ssh "$HOST" 'sudo docker logs shrooms 2>&1 | grep "^time=" | tail -10'

if [ "${JOIN_BY_INVITE:-0}" = 1 ]; then
cat <<EOF

==> $HOST is running and waiting for a mesh.

It has its own keys and its own name, and no mesh. To admit it, on a machine
already in the mesh:

  shrooms invite

then, with the token it prints:

  ssh $HOST 'sudo docker exec shrooms shrooms join --invite <TOKEN> \
      --socket /run/shrooms/shrooms.sock'

The token is good for one device and fifteen minutes, and what makes this node
a member afterwards is an admin-signed credential — which you can revoke.
EOF
fi

cat <<EOF

Useful on $HOST:
  sudo docker exec shrooms shrooms status --socket /run/shrooms/shrooms.sock
  sudo docker exec shrooms shrooms paths  --socket /run/shrooms/shrooms.sock
  sudo docker logs -f shrooms

If this node is publicly reachable, open its UDP port:
  sudo ufw allow 51820/udp        # or the equivalent for your firewall
EOF
