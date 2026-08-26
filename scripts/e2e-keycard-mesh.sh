#!/usr/bin/env bash
# The whole life of a card-backed mesh, with a daemon and a second device:
# mint, invite, join, renew, revoke, teardown.
#
# NOT in CI and cannot be. It needs a USB reader, a card, a PIN, a -tags pcsc
# build, and root for the WireGuard device. Run it where all of those are:
#
#     make shrooms TAGS=pcsc
#     sudo SHROOMS_CARD_PIN=nnnnnn ./scripts/e2e-keycard-mesh.sh
#
# e2e-keycard.sh covers the signing paths with no daemon, which is most of what
# a card does. This covers the two that need one: `invite`, where the card signs
# a credential for a device that turned up while the invite was open, and
# `renew`, which reads the roster from a running daemon before signing.
#
# The fleet is local: node A pins its rendezvous port and everything else is
# pointed at it with --entry-node, so nothing here touches the public fleet.
set -uo pipefail

cd "$(dirname "$0")/.."
BIN=$(pwd)/bin/shrooms
PAIRDIR=${SHROOMS_CARD_PAIRDIR:-$HOME/.config/shrooms-e2e-card}
WORK=$(mktemp -d)
SOCKS=$(mktemp -d /tmp/e2e-card-XXXXXX)
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done
  sleep 1
  for p in "${PIDS[@]:-}"; do kill -9 "$p" 2>/dev/null; done
  rm -rf "$WORK" "$SOCKS"
}
trap cleanup EXIT

PASS=0; FAIL=0
ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
note() { printf '         %s\n' "$1"; }
skip() { printf '\n  skipped: %s\n' "$1"; exit 77; }
tailof() { sed 's/^/         /' <<<"$(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' "$1" | tail -"${2:-3}")"; }

[ -x "$BIN" ] || skip "no $BIN — run 'make shrooms TAGS=pcsc'"
[ "$(id -u)" -eq 0 ] || skip "needs root: a WireGuard device is CAP_NET_ADMIN"
PIN=${SHROOMS_CARD_PIN:-}
[ -n "$PIN" ] || skip "set SHROOMS_CARD_PIN"
[ -f "$PAIRDIR/keycard-pairing" ] || skip "no pairing in $PAIRDIR — run e2e-keycard.sh first, which pairs once"
[ -e /dev/net/tun ] || modprobe tun 2>/dev/null
[ -e /dev/net/tun ] || skip "no /dev/net/tun"

probe=$("$BIN" keycard status 2>&1)
case "$probe" in
  *"reader support"*|*"reader found"*|*"No smart card inserted"*|*"PC/SC service"*)
    skip "no card on a reader" ;;
esac

A_WG=51920; A_DELIVERY=31920; A_IF=e2ca
B_WG=51921; B_DELIVERY=31921; B_IF=e2cb

echo "==> a card-backed mesh, end to end"

wait_for() { local n=0; while [ ! -e "$2" ] && [ "$n" -lt "$1" ]; do sleep 1; n=$((n+1)); done; [ -e "$2" ]; }

# --- node A: minted from the card -----------------------------------------
mkdir -p "$WORK/a"
cp "$PAIRDIR"/keycard-* "$WORK/a/" 2>/dev/null
"$BIN" prepare --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --name alpha --port "$A_WG" >/dev/null 2>&1
KEY=$(python3 -c "
import base64,os
print(base64.b32encode(os.urandom(32)).decode().rstrip('='))")
printf '%s\n' "$KEY" | "$BIN" set-key --config "$WORK/a/config.toml" \
    --socket "$SOCKS/none.sock" >/dev/null 2>&1

minted=$(printf '%s\n' "$PIN" | "$BIN" admin init --keycard --dir "$WORK/a" 2>&1)
AUTHKEY=$(sed -n 's/.*admin_keys = \["\(.*\)"\].*/\1/p' <<<"$minted" | head -1)
if [ -z "$AUTHKEY" ]; then bad "could not mint from the card"; note "$(tail -3 <<<"$minted")"; exit 1; fi
ok "minted a mesh authority from the card"
printf 'admin_keys = ["%s"]\ndelivery_port = %d\ninterface = "%s"\n' \
    "$AUTHKEY" "$A_DELIVERY" "$A_IF" >> "$WORK/a/config.toml"

# A needs its own credential, signed by the card.
keys=$("$BIN" keys --state "$WORK/a/state" 2>&1)
AD=$(sed -n 's/^device  *//p' <<<"$keys"|head -1); AW=$(sed -n 's/^tunnel  *//p' <<<"$keys"|head -1)
AS=$(sed -n 's/^sealing  *//p' <<<"$keys"|head -1)
blob=$(printf '%s\n' "$PIN" | "$BIN" admin issue --dir "$WORK/a" --name alpha \
    --device "$AD" --wg "$AW" --seal "$AS" --write=false 2>&1 |
    sed -n 's/.*credential set //p' | tr -d ' \n')
[ -n "$blob" ] && ok "the card issued node A its own credential" || { bad "no credential for A"; exit 1; }
"$BIN" credential set "$blob" --config "$WORK/a/config.toml" --state "$WORK/a/state" \
  </dev/null >/dev/null 2>&1

"$BIN" daemon --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --socket "$SOCKS/a.sock" >"$WORK/a/daemon.log" 2>&1 &
PIDS+=($!)
wait_for 90 "$SOCKS/a.sock" && ok "node A is up on the card's mesh" || {
  bad "node A never bound its socket"; tailof "$WORK/a/daemon.log"; exit 1; }

PEER_A=$(grep -o 'peer_id=[A-Za-z0-9]*' "$WORK/a/daemon.log" | head -1 | cut -d= -f2)
ENTRY="/ip4/127.0.0.1/tcp/$A_DELIVERY/p2p/$PEER_A"
[ ${#PEER_A} -gt 20 ] && ok "node A is dialable as a local fleet" || bad "no peer id for A"

"$BIN" status --socket "$SOCKS/a.sock" 2>&1 | grep -q "no credential" &&
  bad "node A reports itself unenrolled on its own mesh" ||
  ok "node A is admitted by the authority it minted"

# --- invite: the card signs for a device that turns up --------------------
mkdir -p "$WORK/b"
inv="$WORK/a/invite.log"
( printf '%s\n' "$PIN" | "$BIN" invite --admin-dir "$WORK/a" \
    --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --socket "$SOCKS/a.sock" --ttl 5m >"$inv" 2>&1 ) &
PIDS+=($!)

TOKEN=""
for _ in $(seq 1 60); do
  TOKEN=$(sed -n 's/.*join --invite \([A-Za-z0-9._-]*\).*/\1/p' "$inv" | head -1)
  [ -n "$TOKEN" ] && break
  sleep 1
done
if [ -n "$TOKEN" ]; then ok "the card holder opened an invite"; else
  bad "no invite token appeared"; tailof "$inv" 5; exit 1
fi

joined=$("$BIN" join --invite "$TOKEN" --config "$WORK/b/config.toml" \
    --state "$WORK/b/state" --name beta --port "$B_WG" \
    --entry-node "$ENTRY" --local </dev/null 2>&1)
if grep -qiE "joined|welcome|admitted|you are in" <<<"$joined"; then
  ok "node B redeemed the invite"
else
  bad "node B could not join"
  note "$(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' <<<"$joined" | tail -3 | tr '\n' ' ')"
fi

grep -qi "Admitted" "$inv" &&
  ok "the card signed a credential for it" || { bad "the invite never admitted anybody"; tailof "$inv" 4; }

if [ -f "$WORK/b/state/state.json" ] && "$BIN" keys --state "$WORK/b/state" 2>&1 | grep -q "^credential"; then
  ok "node B holds a credential the card signed"
else
  bad "node B has no credential"
fi

# --- renew: reads the roster from a running daemon, then signs ------------
# No entry_nodes here: `join --entry-node` already wrote them into the config
# it created, and a second copy of the key would be a config that does not load.
printf 'delivery_port = %d\ninterface = "%s"\n' "$B_DELIVERY" "$B_IF" \
    >> "$WORK/b/config.toml"
"$BIN" daemon --config "$WORK/b/config.toml" --state "$WORK/b/state" \
    --socket "$SOCKS/b.sock" >"$WORK/b/daemon.log" 2>&1 &
PIDS+=($!)
wait_for 90 "$SOCKS/b.sock" && ok "node B is up" || { bad "node B never started"; tailof "$WORK/b/daemon.log"; }

n=0
while [ "$n" -lt 120 ]; do
  "$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q beta && break
  sleep 3; n=$((n+3))
done
"$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q beta &&
  ok "node A sees node B on the card's mesh (${n}s)" ||
  bad "they never met in ${n}s"

renewed=$(printf '%s\n' "$PIN" | "$BIN" admin renew --dir "$WORK/a" \
    --socket "$SOCKS/a.sock" --all 2>&1)
if grep -qiE "renewed|issued|up to date" <<<"$renewed"; then
  ok "the card renewed the mesh's credentials"
else
  bad "renew failed"
  note "$(grep -vE '^(INF|DBG)' <<<"$renewed" | tail -3 | tr '\n' ' ')"
fi

# --- revoke: enforced by the other node ----------------------------------
BD=$("$BIN" keys --state "$WORK/b/state" 2>&1 | sed -n 's/^device  *//p' | head -1)
revoked=$(printf '%s\n' "$PIN" | "$BIN" admin revoke --dir "$WORK/a" \
    --socket "$SOCKS/a.sock" --device "$BD" 2>&1)
grep -qE "^Revoked " <<<"$revoked" &&
  ok "the card signed a revocation" || { bad "revoke failed"; note "$(tail -2 <<<"$revoked")"; }

n=0
while [ "$n" -lt 90 ]; do
  "$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q beta || break
  sleep 3; n=$((n+3))
done
"$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q beta &&
  bad "node B is still on the roster ${n}s after being revoked" ||
  ok "node B is gone from the mesh (${n}s)"

# --- teardown -------------------------------------------------------------
[ -f "$PAIRDIR/keycard-pairing" ] &&
  ok "the pairing survives, so the next run spends no slot" ||
  bad "the pairing was destroyed"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
