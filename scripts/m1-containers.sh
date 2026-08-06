#!/usr/bin/env bash
# M1 end-to-end test in containers.
#
# Two mesh nodes in separate network namespaces discover each other over the
# public logos.dev fleet and bring up a WireGuard tunnel, then we ping across
# the overlay.
#
# Requires: docker, /dev/net/tun on the host, and outbound internet.
set -euo pipefail

cd "$(dirname "$0")/.."
D=docker
RUN=$D/run
BUILD=$D/build

LD_LIB=${LD_LIB:-$HOME/.local/share/Logos/LogosBasecamp/modules/delivery_module}
WAIT=${WAIT:-90}

echo "==> checking prerequisites"
[ -e /dev/net/tun ] || { echo "no /dev/net/tun on the host"; exit 1; }
[ -f "$LD_LIB/liblogosdelivery.so" ] || { echo "missing liblogosdelivery.so in $LD_LIB"; exit 1; }

echo "==> building logos-vpn"
make logos-vpn >/dev/null

echo "==> staging image context"
rm -rf "$BUILD" "$RUN"
mkdir -p "$BUILD/lib" "$RUN"/{a,b}/{etc,state}
cp bin/logos-vpn "$BUILD/"
# Copy every shared library Basecamp ships alongside liblogosdelivery, not just
# the obvious two: the library dlopen()s libpq at runtime for the Store backend,
# and that failure is fatal and only visible at startup.
cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$BUILD/lib/" 2>/dev/null || true
[ -f "$BUILD/lib/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so staged"; exit 1; }
echo "    staged $(ls "$BUILD/lib" | wc -l) libraries"

echo "==> generating configs"
# Generated on the host so both containers share one network key. State dirs
# are per-node, so each gets its own device identity.
./bin/logos-vpn init \
    --config "$RUN/a/etc/config.toml" --state "$RUN/a/state" \
    --name node-a >/dev/null
KEY=$(./bin/logos-vpn key show --config "$RUN/a/etc/config.toml")
./bin/logos-vpn join "$KEY" \
    --config "$RUN/b/etc/config.toml" --state "$RUN/b/state" \
    --name node-b >/dev/null
echo "    network key: $KEY"

echo "==> building image"
docker build -q -t logos-vpn:test -f "$D/Dockerfile" "$BUILD" >/dev/null

echo "==> starting nodes"
docker compose -f "$D/compose.yml" up -d --force-recreate >/dev/null

cleanup() {
    # Only our own slog lines — the Nim library is extremely chatty and its
    # relay/filter noise buries the application layer.
    for n in a b; do
        echo "==> app log (node-$n)"
        docker logs "logos-vpn-$n" 2>&1 | grep -E "^time=" | tail -20 || true
    done
    if [ "${KEEP:-0}" = "1" ]; then
        echo "==> KEEP=1, leaving containers up for inspection"
        echo "    docker logs logos-vpn-a"
        echo "    docker exec logos-vpn-a logos-vpn status"
        echo "    docker compose -f $D/compose.yml down -v   # when done"
        return
    fi
    docker compose -f "$D/compose.yml" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Wait for the TUNNEL to be up, not merely for the peer to appear in the
# roster. Discovery completes seconds before the WireGuard handshake does, and
# pinging in between fails in a way that looks like a routing bug.
echo "==> waiting up to ${WAIT}s for discovery and handshake"
deadline=$((SECONDS + WAIT))
discovered=0
handshaked=0
while [ $SECONDS -lt $deadline ]; do
    set +e
    js=$(docker exec logos-vpn-a logos-vpn status --json 2>/dev/null)
    set -e
    if [ -n "$js" ]; then
        # Parse rather than grep: the CLI pretty-prints, so the JSON contains
        # `"online": true` with a space and a naive grep silently never matches.
        set +e
        flags=$(echo "$js" | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: print("0 0"); sys.exit()
ps=d.get("peers") or []
print(int(any(p.get("online") for p in ps)), int(any(p.get("handshaked") for p in ps)))
' 2>/dev/null)
        set -e
        on=${flags%% *}; hs=${flags##* }
        if [ "$on" = "1" ] && [ $discovered -eq 0 ]; then
            echo "    discovered peer after $((SECONDS))s"
            discovered=1
        fi
        if [ "$hs" = "1" ]; then
            echo "    handshake complete after $((SECONDS))s"
            handshaked=1
            break
        fi
    fi
    sleep 3
done

if [ $discovered -ne 1 ]; then
    echo "FAIL: node-a never saw node-b announce"
    exit 1
fi
if [ $handshaked -ne 1 ]; then
    echo "FAIL: peer discovered but the WireGuard handshake never completed"
    docker exec logos-vpn-a logos-vpn status || true
    exit 1
fi

echo "==> status on node-a"
docker exec logos-vpn-a logos-vpn status

echo "==> pinging node-b across the overlay"
# Note: `set -e` + `pipefail` would abort inside a command substitution before
# any error check below could run, so disable it around the extraction.
set +e
PEER=$(docker exec logos-vpn-a logos-vpn status --json 2>/dev/null \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["peers"][0]["overlay"] if d.get("peers") else "")' 2>/dev/null)
set -e
if [ -z "$PEER" ]; then
    echo "FAIL: could not determine the peer overlay address from status --json"
    docker exec logos-vpn-a logos-vpn status --json || true
    exit 1
fi
echo "    peer overlay: $PEER"

set +e
docker exec logos-vpn-a ping -c 3 -W 5 "$PEER"
ping_rc=$?
set -e

echo "==> post-ping status (node-a)"
docker exec logos-vpn-a logos-vpn status || true
echo "==> interface state (node-a)"
docker exec logos-vpn-a ip -6 addr show dev logos0 || true
docker exec logos-vpn-a ip -6 route show || true

if [ $ping_rc -eq 0 ]; then
    echo
    echo "M1 PASS: discovery over logos.dev, tunnel up, overlay ping works"
else
    echo
    echo "FAIL: peer discovered but the overlay ping did not get through"
    exit 1
fi
