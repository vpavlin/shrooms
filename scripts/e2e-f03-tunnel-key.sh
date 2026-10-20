#!/usr/bin/env bash
# F-03, new announce: gamma carries beta's WireGuard key in an admin-signed
# credential and announces it without holding the private key. beta is already
# in the mesh and holds the key, so alpha must refuse gamma. Fails before
# c902a10; the audit's lab re-run on 0830e73 is in 06/07.
#
# Needs root and /dev/net/tun, like scripts/e2e-two-nodes.sh.
set -uo pipefail
cd "$(dirname "$0")"
. ./e2e-f03-lib.sh

setup_mesh
start_beta "$BIN"
start_gamma

if wait_log 90 "$WORK/a/daemon.log" 'refusing a peer announcing another device.s tunnel key'; then
  ok "alpha refused gamma's announce"
else
  bad "alpha never logged the duplicate-key refusal"
  note "$(tail -3 "$WORK/a/daemon.log" | tr '\n' ' ')"
fi

saw "$SOCKS/a.sock" beta && ok "alpha still has beta" || bad "alpha lost beta"
if saw "$SOCKS/a.sock" gamma; then
  bad "alpha has gamma despite the duplicate key"
else
  ok "alpha did not add gamma"
fi
saw "$SOCKS/b.sock" alpha && ok "beta still sees alpha" || bad "beta lost alpha"
summary
