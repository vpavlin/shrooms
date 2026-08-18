#!/usr/bin/env bash
# M1 end-to-end test in containers.
#
# Two mesh nodes in separate network namespaces discover each other over the
# public Logos Messaging fleet and bring up a WireGuard tunnel, then we ping across
# the overlay.
#
# Requires: docker, /dev/net/tun on the host, and outbound internet.
set -euo pipefail

cd "$(dirname "$0")/.."
D=docker
RUN=$D/run
# The image context is a SUBDIRECTORY of docker/build, never docker/build
# itself: docker/build/lib is where the fetched liblogosdelivery lives, and this
# script wipes its context before staging. Using the parent meant `make m1`
# deleted the library every other target depends on, so the next build failed
# with "run make deps-release" for no reason anyone could see.
BUILD=$D/build
CTX=$BUILD/ctx

LD_LIB=${LD_LIB:-$BUILD/lib}
# Discovery is the slow half and it is somebody else's network: a passing run
# has taken 2m18s to see the first announce. Ninety seconds reported that as a
# failure, which teaches you to distrust the spike rather than the fleet.
WAIT=${WAIT:-240}

echo "==> checking prerequisites"
[ -e /dev/net/tun ] || { echo "no /dev/net/tun on the host"; exit 1; }
[ -f "$LD_LIB/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so in $LD_LIB — run 'make deps-release'"; exit 1; }

echo "==> building shrooms"
make shrooms >/dev/null

echo "==> staging image context"
rm -rf "$CTX" "$RUN"
mkdir -p "$CTX/lib" "$RUN"/{a,b}/{etc,state}
cp bin/shrooms "$CTX/"
cp docker/gateway.sh docker/entrypoint-nat.sh "$CTX/"
# Copy every shared library Basecamp ships alongside liblogosdelivery, not just
# the obvious two: the library dlopen()s libpq at runtime for the Store backend,
# and that failure is fatal and only visible at startup.
cp "$LD_LIB"/*.so "$LD_LIB"/*.so.* "$CTX/lib/" 2>/dev/null || true
[ -f "$CTX/lib/liblogosdelivery.so" ] || { echo "no liblogosdelivery.so staged"; exit 1; }
echo "    staged $(ls "$CTX/lib" | wc -l) libraries"

echo "==> generating configs"
# Generated on the host so both containers share one network key. State dirs
# are per-node, so each gets its own device identity.
# --no-admin: this spike proves discovery and the data plane, and minting an
# authority prompts for a passphrase that a script has no terminal to type into
# — which is what broke these spikes silently when credentials landed. A mesh
# whose membership is the network key is also exactly what M1 was written for.
./bin/shrooms init --no-admin \
    --config "$RUN/a/etc/config.toml" --state "$RUN/a/state" \
    --name node-a >/dev/null
KEY=$(./bin/shrooms key show --config "$RUN/a/etc/config.toml")
./bin/shrooms join "$KEY" \
    --config "$RUN/b/etc/config.toml" --state "$RUN/b/state" \
    --name node-b >/dev/null
echo "    network key: $KEY"

# Edge, not the Core default.
#
# Two Core nodes on one machine is a lot: each maintains its own gossip mesh
# with the fleet, and they compete for the same uplink to prove something that
# has nothing to do with relaying for anybody else. Edge subscribes and
# forwards nothing, which is what these spikes are actually testing around, and
# it is also what a laptop and a phone run.
sed -i 's/^mode *=.*/mode        = "Edge"/' "$RUN"/*/etc/config.toml
# A sed that matches nothing exits 0. Without this the spike would quietly go
# on testing two Core nodes while claiming to test Edge.
if grep -l '^mode.*"Core"' "$RUN"/*/etc/config.toml; then
    echo "FAIL: the mode rewrite matched nothing (config format changed?)"; exit 1
fi

echo "==> building image"
docker build -q -t shrooms:test -f "$D/Dockerfile" "$CTX" >/dev/null

echo "==> starting nodes"
docker compose -f "$D/compose.yml" up -d --force-recreate >/dev/null

cleanup() {
    # Only our own slog lines — the Nim library is extremely chatty and its
    # relay/filter noise buries the application layer.
    for n in a b; do
        echo "==> app log (node-$n)"
        docker logs "shrooms-$n" 2>&1 | grep -E "^time=" | tail -20 || true
    done
    if [ "${KEEP:-0}" = "1" ]; then
        echo "==> KEEP=1, leaving containers up for inspection"
        echo "    docker logs shrooms-a"
        echo "    docker exec shrooms-a shrooms status"
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
    js=$(docker exec shrooms-a shrooms status --json 2>/dev/null)
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
print(int(any(p.get("online") for p in ps)), int(any(p.get("live") for p in ps)))
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
    docker exec shrooms-a shrooms status || true
    exit 1
fi

echo "==> status on node-a"
docker exec shrooms-a shrooms status

echo "==> pinging node-b across the overlay"
# Note: `set -e` + `pipefail` would abort inside a command substitution before
# any error check below could run, so disable it around the extraction.
set +e
PEER=$(docker exec shrooms-a shrooms status --json 2>/dev/null \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["peers"][0]["overlay"] if d.get("peers") else "")' 2>/dev/null)
set -e
if [ -z "$PEER" ]; then
    echo "FAIL: could not determine the peer overlay address from status --json"
    docker exec shrooms-a shrooms status --json || true
    exit 1
fi
echo "    peer overlay: $PEER"

set +e
docker exec shrooms-a ping -c 3 -W 5 "$PEER"
ping_rc=$?
set -e

echo "==> post-ping status (node-a)"
docker exec shrooms-a shrooms status || true
echo "==> interface state (node-a)"
docker exec shrooms-a ip -6 addr show dev shrooms0 || true
docker exec shrooms-a ip -6 route show || true

if [ $ping_rc -eq 0 ]; then
    echo
    echo "M1 PASS: discovery over the public fleet, tunnel up, overlay ping works"
else
    echo
    echo "FAIL: peer discovered but the overlay ping did not get through"
    exit 1
fi
