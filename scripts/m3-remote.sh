#!/usr/bin/env bash
# M3 over the real internet: relay a NATed node on a remote host to this one.
#
#   ./scripts/m3-remote.sh root@vps.example.com
#   ./scripts/m3-remote.sh root@vps.example.com --down     # remove the test node
#
# The all-container M3 test (`make m3`) proves the relay mechanism works. It
# cannot prove it works across the internet, because every hop is a 1 ms bridge
# on one machine — and every timing bug this project has hit was invisible at
# 1 ms and fatal at 100-450 ms.
#
# This runs a third node behind a NAT gateway on the VPS. That node has no
# address reachable from outside the VPS, and this machine is behind a NAT that
# does not forward, so the two cannot reach each other directly by construction.
# The only path is through the relay, over the real internet, with real latency.
#
#     this machine ──── real home NAT ────╮
#                                         │   the internet
#     VPS (relay, public IP) ─────────────╯
#       └── gw (NAT) ── node
#
# WHAT THIS DOES AND DOES NOT PROVE
#
#   Proves:      the relay carries traffic between two peers that genuinely
#                cannot connect directly, across a real internet path with real
#                latency, jitter and MTU — including relay discovery, since the
#                new node is told nothing about the relay.
#
#   Does NOT prove: hole punching (M2). The NATed node is unreachable from
#                outside the VPS no matter how well punching works, so a relayed
#                result here is the expected outcome and says nothing about
#                traversal. M2 needs a second node on a genuinely different
#                network — tethering a machine to a phone is the cheapest way,
#                and gets you CGNAT, which is the case that actually matters.
#
#   Half-real:   the VPS-side NAT is a docker bridge, not a router in the wild.
#                The leg that is fully real is this machine to the relay.
set -euo pipefail

cd "$(dirname "$0")/.."

HOST=${1:-}
[ -n "$HOST" ] || { echo "usage: $0 user@host [--name NAME] [--wait SECONDS] [--down]"; exit 1; }
shift

NAME=natted
WAIT=180
DOWN=0
LOCAL_SOCK=${LOGOS_VPN_SOCKET:-/run/shrooms/shrooms.sock}

while [ $# -gt 0 ]; do
    case "$1" in
        --name) NAME=$2; shift 2 ;;
        --wait) WAIT=$2; shift 2 ;;
        --down) DOWN=1; shift ;;
        *) echo "unknown option $1"; exit 1 ;;
    esac
done

REMOTE_DIR=/etc/shrooms/m3-remote

if [ $DOWN -eq 1 ]; then
    echo "==> removing the test node from $HOST"
    ssh "$HOST" "REMOTE_DIR=$REMOTE_DIR bash -s" <<'REMOTE'
S=""; [ "$(id -u)" -eq 0 ] || S=sudo
$S docker rm -f shrooms-natted shrooms-testgw 2>/dev/null || echo "  no containers"
$S docker network rm m3-lan m3-wan 2>/dev/null || true
$S rm -rf "$REMOTE_DIR"
echo "  removed"
REMOTE
    echo
    echo "The node will drop out of your roster after ~3 minutes (OfflineAfter)."
    echo "If manage_hosts is on, its /etc/hosts entry goes with it."
    exit 0
fi

# ---------------------------------------------------------------------------
# Preflight. Every check here is one that produces a confusing failure later.
# ---------------------------------------------------------------------------

# The daemon needs CAP_NET_ADMIN, so it normally runs as root and its socket is
# root:root 0660. Reading it therefore usually needs sudo — but not always (a
# developer running unprivileged with a custom socket does not). Work out which
# once, here, instead of sprinkling sudo through every later call.
SUDO=""
localVPN() { ${SUDO:+sudo} ./bin/shrooms "$@"; }

echo "==> checking this machine"
[ -S "$LOCAL_SOCK" ] || {
    echo "no control socket at $LOCAL_SOCK — is the local daemon running?"
    echo "this test measures the tunnel from HERE, so the local node must be up"
    exit 1
}
if ! ./bin/shrooms status --socket "$LOCAL_SOCK" >/dev/null 2>&1; then
    # May prompt for a password; that is expected and better than failing with
    # "the daemon is not answering" when the daemon is answering fine.
    if sudo ./bin/shrooms status --socket "$LOCAL_SOCK" >/dev/null 2>&1; then
        SUDO=1
    elif [ -S "$LOCAL_SOCK" ] && ! sudo -n true 2>/dev/null; then
        # The socket exists, so the daemon is almost certainly fine — we just
        # cannot read it. Saying "the daemon is not answering" here sends people
        # to debug a healthy daemon, which is exactly what happened.
        echo "cannot read $LOCAL_SOCK without sudo, and sudo needs a password here."
        echo "Run this from a terminal where sudo can prompt, or pre-authorise with:"
        echo "  sudo -v && make m3-remote HOST=$HOST"
        exit 1
    else
        echo "the local daemon is not answering on $LOCAL_SOCK"
        echo "try:  sudo ./bin/shrooms status --socket $LOCAL_SOCK"
        exit 1
    fi
fi
echo "  local daemon up${SUDO:+ (reading its socket via sudo)}"

echo "==> checking $HOST"
# `sudo` only when not already root: a VPS logged into as root may not have it,
# and prefixing regardless turns a working host into a confusing failure.
ssh "$HOST" '
S=""; [ "$(id -u)" -eq 0 ] || S=sudo
command -v docker >/dev/null || { echo "docker is not installed"; exit 1; }
[ -e /dev/net/tun ] || { echo "no /dev/net/tun"; exit 1; }
$S test -f /etc/shrooms/config.toml || { echo "no /etc/shrooms/config.toml — this host is not a mesh node"; exit 1; }
$S grep -q "^relay *= *\"true\"" /etc/shrooms/config.toml || { echo "this host is not configured as a relay (relay = \"true\") — nothing would forward"; exit 1; }

# The relay may run natively or in a container; both are supported deployments,
# and requiring one silently excluded the other.
if $S docker inspect shrooms >/dev/null 2>&1; then
    echo "  relay: container"
elif pgrep -f "shrooms daemon" >/dev/null; then
    echo "  relay: native process"
else
    echo "no shrooms daemon running (neither a container named shrooms nor a native process)"; exit 1
fi
if docker version 2>/dev/null | grep -qi podman || docker --version 2>&1 | grep -qi podman; then
    echo "  container engine: podman"
fi'

# The key comes off the relay host rather than being passed in: the new node has
# to join the SAME mesh, and retyping it is a way to get that subtly wrong.
echo "==> reading the network key from $HOST"
KEY=$(ssh "$HOST" '
S=""; [ "$(id -u)" -eq 0 ] || S=sudo
if command -v shrooms >/dev/null; then
    $S shrooms key show --config /etc/shrooms/config.toml
else
    $S docker exec shrooms shrooms key show --config /etc/shrooms/config.toml
fi' | tr -d '\r\n')
[ -n "$KEY" ] || { echo "could not read the network key"; exit 1; }
echo "  ${KEY:0:8}… (joining the same mesh as the relay)"

# The test node runs in a container regardless of how the relay runs — it needs
# a NAT topology, which is the whole point. Ship the image if it is not there.
echo "==> ensuring the image exists on $HOST"
REGISTRY_IMAGE=${REGISTRY_IMAGE:-ghcr.io/vpavlin/shrooms:latest}
if [ "${FORCE_IMAGE:-0}" != "1" ] && ssh "$HOST" 'S=""; [ "$(id -u)" -eq 0 ] || S=sudo; $S docker image inspect shrooms:test >/dev/null 2>&1'; then
    echo "  shrooms:test already present (FORCE_IMAGE=1 to replace)"
elif ssh "$HOST" "S=\"\"; [ \"\$(id -u)\" -eq 0 ] || S=sudo; \$S docker pull -q $REGISTRY_IMAGE >/dev/null 2>&1 && \$S docker tag $REGISTRY_IMAGE shrooms:test"; then
    # Pulling beats building and pushing ~200MB over ssh, which is by far the
    # slowest step here. Falls through to that when the registry is
    # unreachable or the package is still private.
    echo "  pulled $REGISTRY_IMAGE"
else
    LD_LIB=${LD_LIB:-docker/build/lib}
    [ -f "$LD_LIB/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so in $LD_LIB — run 'make deps-release'"; exit 1; }
    echo "  building locally"
    CTX=docker/build/m3remote
    rm -rf "$CTX"; mkdir -p "$CTX/lib"
    cp bin/shrooms docker/gateway.sh docker/entrypoint-nat.sh "$CTX/"
    cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$CTX/lib/" 2>/dev/null || true
    docker build -q -t shrooms:test -f docker/Dockerfile "$CTX" >/dev/null
    echo "  shipping (this is the slow part)"
    docker save shrooms:test | gzip -1 | ssh "$HOST" 'S=""; [ "$(id -u)" -eq 0 ] || S=sudo; gunzip | $S docker load' | tail -1
fi

# ---------------------------------------------------------------------------
# Bring the node up.
# ---------------------------------------------------------------------------

echo "==> generating the node's config"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
./bin/shrooms join "$KEY" --config "$TMP/config.toml" --state "$TMP/state" --name "$NAME" >/dev/null
# No advertise, no relay_addr: this node must discover the relay from its
# announce, which is half of what the test is for.

echo "==> installing on $HOST"
# Not scp'd to a predictable /tmp path: this file contains the network key.
# Staged under the remote's own home with a private mode instead.
REMOTE_CFG=$(ssh "$HOST" 'd=$(mktemp -d); printf %s "$d/config.toml"')
ssh "$HOST" "install -m 600 /dev/stdin '$REMOTE_CFG'" < "$TMP/config.toml"

# Plain `docker run` rather than compose. The remote engine may be podman with
# podman-compose, where `internal:` networks, static ipv4_address and SELinux
# bind mounts are the least reliable corner; these commands behave identically
# on docker and podman. It also removes a file that had to be kept in step with
# this script.
ssh "$HOST" "REMOTE_DIR=$REMOTE_DIR REMOTE_CFG=$REMOTE_CFG bash -s" <<'REMOTE'
set -euo pipefail
S=""; [ "$(id -u)" -eq 0 ] || S=sudo

# SELinux relabels bind mounts; without :Z the container cannot read its config
# and reports it as missing.
Z=""
if [ -e /sys/fs/selinux/enforce ]; then
    Z=":Z"
    echo "  SELinux enabled, relabelling mounts"
fi

$S mkdir -p "$REMOTE_DIR/nat/etc" "$REMOTE_DIR/nat/state"
$S install -m 600 "$REMOTE_CFG" "$REMOTE_DIR/nat/etc/config.toml"
rm -f "$REMOTE_CFG"

# Idempotent: a previous run must not block this one.
$S docker rm -f shrooms-natted shrooms-testgw >/dev/null 2>&1 || true
$S docker network rm m3-lan m3-wan >/dev/null 2>&1 || true

$S docker network create m3-wan >/dev/null
$S docker network create --internal --subnet 10.93.0.0/24 m3-lan >/dev/null

# create -> connect -> start, so the gateway has BOTH interfaces before
# gateway.sh runs. It identifies WAN by which one carries the default route,
# and an interface attached after startup would arrive too late.
# --sysctl because podman mounts /proc/sys read-only, so gateway.sh cannot
# enable forwarding from inside. Without it the gateway starts and forwards
# nothing, and everything behind it loses connectivity.
$S docker create --name shrooms-testgw --cap-add NET_ADMIN \
    --sysctl net.ipv4.ip_forward=1 \
    --network m3-wan --entrypoint /usr/bin/gateway.sh shrooms:test >/dev/null
$S docker network connect --ip 10.93.0.2 m3-lan shrooms-testgw
$S docker start shrooms-testgw >/dev/null
echo "  gateway up"

$S docker create --name shrooms-natted --cap-add NET_ADMIN \
    --device /dev/net/tun \
    -e NAT_GW=10.93.0.2 \
    --network m3-lan --ip 10.93.0.100 \
    -v "$REMOTE_DIR/nat/etc:/etc/shrooms$Z" \
    -v "$REMOTE_DIR/nat/state:/var/lib/shrooms$Z" \
    --entrypoint /usr/bin/entrypoint-nat.sh shrooms:test daemon -v >/dev/null
$S docker start shrooms-natted >/dev/null
echo "  NATed node up"
REMOTE

# ---------------------------------------------------------------------------
# Measure from this machine. The remote's own view is not the question —
# whether the tunnel comes up from here is.
# ---------------------------------------------------------------------------

peerField() {
    localVPN status --json --socket "$LOCAL_SOCK" 2>/dev/null | python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit()
for p in d.get('peers') or []:
    if p.get('name')=='$NAME':
        print(json.dumps({'online':bool(p.get('online')),'hs':bool(p.get('live')),
                          'ep':p.get('endpoint') or '','rx':p.get('rx_bytes') or 0}))
        sys.exit()
" 2>/dev/null
}

echo
echo "==> waiting up to ${WAIT}s for '$NAME' to appear and connect FROM THIS MACHINE"
started=$SECONDS
seen=0
up=0
while [ $((SECONDS - started)) -lt "$WAIT" ]; do
    line=$(peerField)
    if [ -n "$line" ]; then
        online=$(echo "$line" | python3 -c 'import json,sys; print(json.load(sys.stdin)["online"])')
        hs=$(echo "$line" | python3 -c 'import json,sys; print(json.load(sys.stdin)["hs"])')
        if [ "$online" = "True" ] && [ $seen -eq 0 ]; then
            echo "    discovered over the public fleet after $((SECONDS - started))s"
            seen=1
        fi
        if [ "$hs" = "True" ]; then
            echo "    tunnel up after $((SECONDS - started))s"
            up=1
            break
        fi
    fi
    sleep 5
done

echo
echo "==> local status"
localVPN status --socket "$LOCAL_SOCK" || true
echo "==> local paths"
localVPN paths --socket "$LOCAL_SOCK" || true

if [ $seen -ne 1 ]; then
    echo
    echo "FAIL: '$NAME' never appeared in the roster."
    echo "  That is discovery, not traversal — the node is not reaching the fleet."
    echo "  Check: sudo docker logs shrooms-natted"
    exit 1
fi

if [ $up -ne 1 ]; then
    echo
    echo "FAIL: '$NAME' is online but no tunnel came up within ${WAIT}s."
    echo "  Discovery works, so this is the relay path. Check, on $HOST:"
    echo "    sudo docker logs shrooms-natted 2>&1 | grep -E 'relay|path'"
    echo "    sudo docker logs shrooms        2>&1 | grep -E 'relay'"
    echo "  and here, whether the endpoint shows relay:… above."
    exit 1
fi

ep=$(peerField | python3 -c 'import json,sys; print(json.load(sys.stdin)["ep"])')
rx=$(peerField | python3 -c 'import json,sys; print(json.load(sys.stdin)["rx"])')

echo
case "$ep" in
    relay:*)
        echo "M3-REMOTE PASS: relayed tunnel over the real internet"
        echo "  endpoint: $ep"
        echo "  received: ${rx} bytes"
        echo
        echo "  This is the case the relay exists for: two peers that cannot"
        echo "  reach each other directly, connected through a third, over a"
        echo "  real path rather than a 1 ms bridge."
        ;;
    "")
        echo "PASS (unclear): handshake completed but no endpoint is reported."
        echo "  Worth investigating before trusting this result."
        ;;
    *)
        echo "UNEXPECTED: the tunnel is direct, not relayed."
        echo "  endpoint: $ep"
        echo
        echo "  The NATed node should have no address reachable from here. Either"
        echo "  the LAN is not actually internal, or this machine can route into"
        echo "  the VPS's docker network. Check the topology before reading this"
        echo "  as a punching success — it is more likely a leak in the test."
        ;;
esac

cat <<EOF

Overlay ping (the honest end-to-end check):
  ping6 -c3 \$(${SUDO:+sudo }./bin/shrooms status --socket $LOCAL_SOCK | awk '/^$NAME/{print \$2}')

On $HOST:
  sudo docker logs -f shrooms-natted
  sudo docker exec shrooms-natted shrooms status --socket /run/shrooms/shrooms.sock

Remove the test node when done:
  $0 $HOST --down
EOF
