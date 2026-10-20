#!/usr/bin/env bash
# F-03, remembered state: run beta with the e2e accept hook once, so gamma is
# written into beta's remembered roster with beta's own tunnel key (the state
# a pre-c902a10 node could leave behind). Restart beta from the same state with
# a normal build and assert gamma is gone from both the live roster and the
# file. Fails on 0830e73; passes on 861afe3.
#
# Needs root and /dev/net/tun, like scripts/e2e-two-nodes.sh.
set -uo pipefail
cd "$(dirname "$0")"
. ./e2e-f03-lib.sh

setup_mesh
start_beta "$ATT" SHROOMS_E2E_ALLOW_DUPLICATE_WG=1
start_gamma

# The old state is only interesting once beta has actually written gamma down.
n=0
while [ "$n" -lt 90 ]; do
  grep -qs '"gamma"' "$WORK/b/state"/roster-*.json && break
  sleep 1; n=$((n+1))
done
if grep -qs '"gamma"' "$WORK/b/state"/roster-*.json; then
  ok "beta remembered gamma with beta's tunnel key (the old state)"
else
  bad "beta never wrote the duplicate to its remembered roster"
  summary; exit 1
fi

# Stop everything, then start beta again from the same state, normal build.
for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
sleep 2
PIDS=()
ip link del "$B_IF" 2>/dev/null || true
start_beta "$BIN"

if wait_log 60 "$WORK/b/daemon.log" 'not remembering a peer with another device.s tunnel key'; then
  ok "beta dropped gamma on load"
else
  bad "beta did not log dropping the remembered duplicate"
fi
if saw "$SOCKS/b.sock" gamma; then
  bad "beta restored gamma"
else
  ok "beta did not restore gamma"
fi
if grep -qs '"gamma"' "$WORK/b/state"/roster-*.json; then
  bad "the stale gamma entry is still in the roster file"
else
  ok "the roster file no longer has gamma"
fi
summary
