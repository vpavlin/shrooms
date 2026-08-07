#!/usr/bin/env bash
# M2: NAT traversal between two nodes behind separate NATs.
#
# Topology (docker/compose-nat.yml): node-a and node-b each sit behind their own
# masquerading gateway, with node-pub directly reachable on the public segment.
# Reaching node-pub is trivial for both; reaching *each other* requires a punch,
# which is the thing under test.
#
#   NAT_MODE=eim   endpoint-independent mapping — punchable (default)
#   NAT_MODE=edm   endpoint-dependent — punching should FAIL, proving the
#                  relay is needed. Expected to fail until M3.
set -euo pipefail

cd "$(dirname "$0")/.."
D=docker
RUN=$D/run
BUILD=$D/build

LD_LIB=${LD_LIB:-$D/build/lib}
WAIT=${WAIT:-150}
NAT_MODE=${NAT_MODE:-eim}
# RELAY=1 configures node-pub as a relay. The NATed nodes discover it from its
# announce, so the test measures relay discovery and the relay path rather than
# punching.
RELAY=${RELAY:-0}

echo "==> prerequisites"
[ -e /dev/net/tun ] || { echo "no /dev/net/tun on the host"; exit 1; }
[ -f "$LD_LIB/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so in $LD_LIB — run 'make deps-release'"; exit 1; }

echo "==> building"
make logos-vpn >/dev/null

echo "==> staging image"
rm -rf "$BUILD/ctx" "$RUN"
mkdir -p "$BUILD/ctx/lib" "$RUN"/{a,b,pub}/{etc,state}
cp bin/logos-vpn "$BUILD/ctx/"
cp "$D"/gateway.sh "$D"/entrypoint-nat.sh "$BUILD/ctx/"
cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$BUILD/ctx/lib/" 2>/dev/null || true

echo "==> generating configs (one network, three devices)"
./bin/logos-vpn init --config "$RUN/pub/etc/config.toml" --state "$RUN/pub/state" \
    --name node-pub --advertise 10.90.0.10:51820 >/dev/null
KEY=$(./bin/logos-vpn key show --config "$RUN/pub/etc/config.toml")
for n in a b; do
    ./bin/logos-vpn join "$KEY" --config "$RUN/$n/etc/config.toml" \
        --state "$RUN/$n/state" --name "node-$n" >/dev/null
done

if [ "$RELAY" = "1" ]; then
    # node-pub relays; the NATed nodes fall back to it when no direct path
    # exists. Appended rather than rewritten so the generated config is intact.
    #
    # Only node-pub is configured. The NATed nodes are told nothing about the
    # relay and must learn it from its announce — deliberately, since a relay
    # nobody can discover is the thing this milestone exists to fix.
    echo 'relay = "true"' >> "$RUN/pub/etc/config.toml"
    echo "    relay: node-pub, to be discovered by node-a and node-b"
fi
echo "    network key: $KEY"

docker build -q -t logos-vpn:test -f "$D/Dockerfile" "$BUILD/ctx" >/dev/null

echo "==> starting topology (NAT_MODE=$NAT_MODE)"
NAT_MODE=$NAT_MODE docker compose -f "$D/compose-nat.yml" up -d --force-recreate >/dev/null

cleanup() {
    for n in pub a b; do
        echo "==> app log (node-$n)"
        # Head as well as tail: startup lines (relay config, mode) are at the
        # beginning and scrolled off when only the tail was shown.
        docker logs "logos-vpn-${n}" 2>&1 | grep -E "^time=" | head -6 || true
        echo "        ..."
        docker logs "logos-vpn-${n}" 2>&1 | grep -E "^time=" | tail -8 || true
    done
    if [ "${KEEP:-0}" = "1" ]; then
        echo "==> KEEP=1, leaving containers up"
        return
    fi
    docker compose -f "$D/compose-nat.yml" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

# peerState <container> <peer-name> -> "online handshaked"
peerState() {
    docker exec "$1" logos-vpn status --json 2>/dev/null | python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: print('0 0'); sys.exit()
for p in d.get('peers') or []:
    if p.get('name')=='$2':
        print(int(bool(p.get('online'))), int(bool(p.get('handshaked')))); sys.exit()
print('0 0')
" 2>/dev/null || echo "0 0"
}

if [ "$RELAY" = "1" ]; then
    echo "==> waiting up to ${WAIT}s for node-a <-> node-b VIA THE RELAY"
    echo "    (both behind separate NATs; neither can reach the other directly)"
else
    echo "==> waiting up to ${WAIT}s for a DIRECT path between node-a and node-b"
    echo "    (both are behind separate NATs; reaching each other needs a punch)"
fi
# Measured from here, not from script start. $SECONDS is process-wide, so the
# earlier numbers this printed silently included the docker build — which made a
# 5s connect read as 350s and looked like a regression.
started=$SECONDS
deadline=$((SECONDS + WAIT))
discovered=0
punched=0
while [ $SECONDS -lt $deadline ]; do
    set +e
    read -r on hs <<<"$(peerState logos-vpn-a node-b)"
    set -e
    if [ "$on" = "1" ] && [ $discovered -eq 0 ]; then
        echo "    node-a discovered node-b after $((SECONDS - started))s"
        discovered=1
    fi
    if [ "$hs" = "1" ]; then
        echo "    node-a <-> node-b handshake after $((SECONDS - started))s"
        punched=1
        break
    fi
    sleep 5
done

echo
echo "==> node-a status"
docker exec logos-vpn-a logos-vpn status || true
echo "==> node-a paths"
docker exec logos-vpn-a logos-vpn paths || true

if [ $discovered -ne 1 ]; then
    echo "FAIL: node-a never saw node-b's announce — discovery is broken, not traversal"
    exit 1
fi

if [ $punched -ne 1 ]; then
    if [ "$RELAY" = "1" ]; then
        echo "FAIL: relay configured but node-a and node-b never handshaked"
        exit 1
    fi
    if [ "$NAT_MODE" = "edm" ]; then
        echo "M2 EXPECTED FAIL: no direct path under endpoint-dependent NAT."
        echo "  This is the case the relay exists for. Implement M3."
        exit 0
    fi
    echo "FAIL: discovered but never punched through under $NAT_MODE"
    exit 1
fi

PEER=$(docker exec logos-vpn-a logos-vpn status --json 2>/dev/null \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(next((p['overlay'] for p in d.get('peers',[]) if p['name']=='node-b'), ''))")
[ -n "$PEER" ] || { echo "FAIL: no overlay address for node-b"; exit 1; }

echo
echo "==> pinging node-b across the overlay (NAT to NAT)"
if docker exec logos-vpn-a ping -c 3 -W 5 "$PEER"; then
    echo
    if [ "$RELAY" = "1" ]; then
        echo "M3 PASS: two NATed nodes carry traffic through the relay"
    else
        echo "M2 PASS: two NATed nodes punched through and carry traffic directly"
    fi
else
    echo
    echo "FAIL: direct path reported but the overlay ping did not get through"
    exit 1
fi
