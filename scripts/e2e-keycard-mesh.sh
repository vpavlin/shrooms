#!/usr/bin/env bash
# The whole life of a card-backed mesh, in containers, with no sudo:
# mint, invite, join, renew, revoke, teardown.
#
#     make shrooms TAGS=pcsc
#     SHROOMS_CARD_PIN=nnnnnn ./scripts/e2e-keycard-mesh.sh
#
# Not in CI, and only because of the card: it needs a reader with a Keycard on
# it. Everything else that used to need root does not any more. Creating a
# WireGuard device wants CAP_NET_ADMIN, which a rootless container can be given
# for its own network namespace, and the reader is reached through the pcscd
# socket rather than the USB device — so the admin commands run inside a node,
# exactly as they would on a real machine rather than beside one.
#
# The fleet is local: node A pins its rendezvous port and node B is pointed at
# it with --entry-node, so nothing here touches the public fleet.
set -uo pipefail

cd "$(dirname "$0")/.."
BIN=$(pwd)/bin/shrooms
D=docker
CTX=$D/build/cardctx
RUN=$D/run
COMPOSE="$D/compose-keycard.yml"
PAIRDIR=${SHROOMS_CARD_PAIRDIR:-$HOME/.config/shrooms-e2e-card}

PASS=0; FAIL=0
ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
note() { printf '         %s\n' "$1"; }
skip() { printf '\n  skipped: %s\n' "$1"; exit 77; }

# KEEP=1 leaves the containers running, so a failure can be looked at rather
# than reproduced. A run that tears down what it was investigating costs
# another four minutes every time.
DOWN_DONE=""
down() {
  [ -n "$DOWN_DONE" ] && return
  DOWN_DONE=1
  if [ "${KEEP:-}" = "1" ]; then
    printf '\n  containers left running (KEEP=1):\n'
    printf '    docker exec shrooms-card-a shrooms status\n'
    printf '    docker exec shrooms-card-a cat /tmp/daemon.log\n'
    printf '    docker compose -f %s down -v\n' "$COMPOSE"
    return
  fi
  docker compose -f "$COMPOSE" down -v >/dev/null 2>&1 || true
}
trap 'down' EXIT

# a <args...>  — run in node A;  b <args...> — in node B. -i so a PIN can be piped.
a() { docker exec -i shrooms-card-a "$@"; }
b() { docker exec -i shrooms-card-b "$@"; }

[ -x "$BIN" ] || skip "no $BIN — run 'make shrooms TAGS=pcsc'"
command -v docker >/dev/null || skip "no docker/podman"
[ -e /dev/net/tun ] || skip "no /dev/net/tun on the host"
[ -S /run/pcscd/pcscd.comm ] || skip "pcscd is not listening — plug in a reader"
PIN=${SHROOMS_CARD_PIN:-}
[ -n "$PIN" ] || skip "set SHROOMS_CARD_PIN so this can use the card"
[ -f "$PAIRDIR/keycard-pairing" ] || skip "no pairing in $PAIRDIR — run e2e-keycard.sh first, which pairs once"

probe=$("$BIN" keycard status 2>&1)
case "$probe" in
  *"reader support"*|*"reader found"*|*"No smart card inserted"*|*"PC/SC service"*)
    skip "no card on a reader" ;;
esac

echo "==> a card-backed mesh, in containers"

# --- image ----------------------------------------------------------------
rm -rf "$CTX" && mkdir -p "$CTX/lib"
cp "$BIN" "$CTX/"
cp "$D/build/lib/"*.so* "$CTX/lib/" 2>/dev/null
cp "$D/gateway.sh" "$D/entrypoint-nat.sh" "$CTX/" 2>/dev/null
if docker build -q -t shrooms:keycard -f "$D/Dockerfile" "$CTX" >/dev/null 2>&1; then
  ok "built an image with the reader-capable binary"
else
  bad "image build failed"; exit 1
fi

down
rm -rf "$RUN/card-a" "$RUN/card-b"
mkdir -p "$RUN/card-a/etc" "$RUN/card-a/state" "$RUN/card-a/admin" \
         "$RUN/card-b/etc" "$RUN/card-b/state"
cp "$PAIRDIR"/keycard-* "$RUN/card-a/admin/" 2>/dev/null

docker compose -f "$COMPOSE" up -d --force-recreate >/dev/null 2>&1
sleep 2
if a true 2>/dev/null && b true 2>/dev/null; then
  ok "two nodes are running, unprivileged"
else
  bad "containers did not come up"
  docker compose -f "$COMPOSE" logs 2>&1 | tail -5 | sed 's/^/         /'
  exit 1
fi

# The card, from inside a container, through the mounted pcscd socket.
if a shrooms keycard status 2>&1 | grep -q "^applet"; then
  ok "node A can reach the card on the host's reader"
else
  bad "no card access inside the container"
  a shrooms keycard status 2>&1 | head -2 | sed 's/^/         /'
  exit 1
fi

# --- mint from the card ---------------------------------------------------
KEY=$(python3 -c "
import base64,os
print(base64.b32encode(os.urandom(32)).decode().rstrip('='))")
a shrooms prepare --name alpha --port 51820 >/dev/null 2>&1
printf '%s\n' "$KEY" | a shrooms set-key >/dev/null 2>&1

minted=$(printf '%s\n' "$PIN" | a shrooms admin init --keycard 2>&1)
AUTHKEY=$(sed -n 's/.*admin_keys = \["\(.*\)"\].*/\1/p' <<<"$minted" | head -1)
if [ -n "$AUTHKEY" ]; then
  ok "minted a mesh authority from the card"
else
  bad "could not mint"; note "$(tail -3 <<<"$minted" | tr '\n' ' ')"; exit 1
fi

# Written from INSIDE the container. The volume's files are owned by the
# container's root, which in a rootless runtime is a subuid the host user is
# not — so appending from here failed silently, the mesh got no admin_keys at
# all, and half of what followed passed by having nothing to check.
a sh -c "printf 'admin_keys = [\"%s\"]\ndelivery_port = 31820\n' '$AUTHKEY' >> /etc/shrooms/config.toml"
if a grep -q "^admin_keys" /etc/shrooms/config.toml; then
  ok "the card's key is the mesh's authority in the config"
else
  bad "admin_keys never reached the config, so this mesh has no authority"; exit 1
fi

keys=$(a shrooms keys 2>&1)
AD=$(sed -n 's/^device  *//p' <<<"$keys"|head -1)
AW=$(sed -n 's/^tunnel  *//p' <<<"$keys"|head -1)
AS=$(sed -n 's/^sealing  *//p' <<<"$keys"|head -1)
blob=$(printf '%s\n' "$PIN" | a shrooms admin issue --name alpha \
    --device "$AD" --wg "$AW" --seal "$AS" --write=false 2>&1 |
    sed -n 's/.*credential set //p' | tr -d ' \r\n')
if [ -n "$blob" ]; then ok "the card issued node A its own credential"; else
  bad "no credential for node A"; exit 1; fi
a shrooms credential set "$blob" >/dev/null 2>&1

# --- node A's daemon ------------------------------------------------------
docker exec -d shrooms-card-a sh -c 'shrooms daemon -v >/tmp/daemon.log 2>&1'
alog() { a cat /tmp/daemon.log 2>/dev/null; }
n=0
while [ "$n" -lt 90 ]; do
  a shrooms status >/dev/null 2>&1 && break
  sleep 2; n=$((n+2))
done
if a shrooms status >/dev/null 2>&1; then
  ok "node A is up on the card's mesh (${n}s)"
else
  bad "node A never answered"; alog | grep -v '^\(INF\|DBG\|WRN\|ERR\) 20' | tail -4 | sed 's/^/         /'; exit 1
fi

# Meaningful only because admin_keys is set above: Unenrolled() is false for a
# mesh with no authority, so without one this asserted nothing at all.
if a shrooms status 2>&1 | grep -q "no credential"; then
  bad "node A reports itself unenrolled on the mesh it minted"
else
  ok "node A is admitted by the authority it minted"
fi

PEER_A=$(alog | grep -o 'peer_id=[A-Za-z0-9]*' | head -1 | cut -d= -f2)
IP_A=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' shrooms-card-a 2>/dev/null | head -1)
ENTRY="/ip4/$IP_A/tcp/31820/p2p/$PEER_A"
if [ ${#PEER_A} -gt 20 ] && [ -n "$IP_A" ]; then
  ok "node A is a dialable local fleet ($IP_A)"
else
  bad "no rendezvous address for node A"; note "peer=$PEER_A ip=$IP_A"
fi

# --- invite: the card signs for a device that turns up --------------------
docker exec -d shrooms-card-a sh -c \
  "printf '%s\n' '$PIN' | shrooms invite --ttl 5m >/tmp/invite.log 2>&1"

TOKEN=""
for _ in $(seq 1 60); do
  TOKEN=$(a cat /tmp/invite.log 2>/dev/null | sed -n 's/.*join --invite \([A-Za-z0-9._-]*\).*/\1/p' | head -1)
  [ -n "$TOKEN" ] && break
  sleep 1
done
if [ -n "$TOKEN" ]; then ok "the card holder opened an invite"; else
  bad "no invite token appeared"
  a cat /tmp/invite.log 2>/dev/null | tail -4 | sed 's/^/         /'; exit 1
fi

b shrooms join --invite "$TOKEN" --name beta --port 51820 \
    --entry-node "$ENTRY" --local >/tmp/join.log 2>&1
joined=$(cat /tmp/join.log 2>/dev/null)
# "expires", not "^credential  ": `shrooms keys` prints "credential  none yet"
# when there is none, which the obvious grep matches — so this asserted that B
# had a credential by matching the words saying it did not, and only the
# failure two steps later gave it away.
if b shrooms keys 2>&1 | grep -q "expires"; then
  ok "node B joined and holds a card-signed credential"
else
  bad "node B did not end up with a credential"
  note "keys says: $(b shrooms keys 2>&1 | grep -i credential | tr '\n' ' ')"
  # The top of a panic, not the tail: the message is the first line and the
  # stack is the rest, so tailing it showed frames and hid the reason.
  if grep -q "panic:" <<<"$joined"; then
    note "join PANICKED:"
    grep -A6 "panic:" <<<"$joined" | head -8 | sed 's/^/         /'
  else
    note "join said: $(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' <<<"$joined" | tail -4 | tr '\n' ' ')"
  fi
fi
a cat /tmp/invite.log 2>/dev/null | grep -qi "Admitted" &&
  ok "the invite recorded admitting it" ||
  bad "the invite never admitted anybody"

# --- node B's daemon, and do they meet? -----------------------------------
b sh -c "printf 'delivery_port = 31821\n' >> /etc/shrooms/config.toml"
docker exec -d shrooms-card-b sh -c 'shrooms daemon -v >/tmp/daemon.log 2>&1'
n=0
while [ "$n" -lt 90 ]; do b shrooms status >/dev/null 2>&1 && break; sleep 2; n=$((n+2)); done
b shrooms status >/dev/null 2>&1 && ok "node B is up" || bad "node B never answered"

n=0
while [ "$n" -lt 150 ]; do
  a shrooms status 2>/dev/null | grep -q beta && break
  sleep 3; n=$((n+3))
done
if a shrooms status 2>/dev/null | grep -q beta; then
  ok "node A sees node B on the card's mesh (${n}s)"
else
  bad "they never met in ${n}s"
  note "A sees:"; a shrooms status 2>&1 | tail -3 | sed 's/^/         /'
  note "B sees:"; b shrooms status 2>&1 | tail -3 | sed 's/^/         /'
  note "B refused anything?"
  b sh -c 'grep -iE "refus|credential|membership" /tmp/daemon.log | tail -3' 2>/dev/null | sed 's/^/         /'
  note "A refused anything?"
  a sh -c 'grep -iE "refus|credential|membership" /tmp/daemon.log | tail -3' 2>/dev/null | sed 's/^/         /' 
fi

# --- renew: reads the roster from a running daemon, then signs ------------
# The serial before, so the check is that a credential was REPLACED rather than
# that renew printed something. "left" appears in the line for a member it
# skipped, so grepping for it passed whether or not anything happened.
# From the credential LINE, not from anywhere the word appears. `shrooms keys`
# also prints an enrolment hint containing "--serial 1", and a naive match takes
# that — so BEFORE and AFTER were both the example text, and the assertion
# compared it to itself and called a working renewal broken.
serial_of() {
  b shrooms keys 2>&1 | grep '^credential ' | sed -n 's/.*serial \([0-9]*\).*/\1/p' | head -1
}
BEFORE=$(serial_of)
[ -n "$BEFORE" ] && ok "node B has a credential with a serial ($BEFORE)" ||
  bad "node B has no serial to renew"

renewed=$(printf '%s\n' "$PIN" | a shrooms admin renew --all 2>&1)
COUNT=$(sed -n 's/^Renewed \([0-9]*\),.*/\1/p' <<<"$renewed" | head -1)
if [ -n "$COUNT" ] && [ "$COUNT" -gt 0 ]; then
  ok "the card renewed $COUNT credential(s)"
else
  bad "renew reported nothing renewed"
  note "$(grep -vE '^(INF|DBG)' <<<"$renewed" | tail -4 | tr '\n' ' ')"
fi

# And that it reached the device. A renewal is published on the mesh and
# travels to the device it names, so this is the half that proves the loop
# closed rather than that a signature was produced.
n=0; AFTER=$BEFORE
while [ "$n" -lt 60 ]; do
  AFTER=$(serial_of)
  [ -n "$AFTER" ] && [ "$AFTER" != "$BEFORE" ] && break
  sleep 3; n=$((n+3))
done
if [ -n "$AFTER" ] && [ "$AFTER" != "$BEFORE" ]; then
  ok "node B holds the renewed credential (serial $BEFORE -> $AFTER, ${n}s)"
else
  bad "node B still holds serial $BEFORE ${n}s after the renewal"
  note "renew said:"; grep -vE '^(INF|DBG)' <<<"$renewed" | tail -6 | sed 's/^/         /'
  note "A on granting:"; a sh -c 'grep -iE "grant|renew" /tmp/daemon.log | tail -3' 2>/dev/null | sed 's/^/         /'
  note "B on granting:"; b sh -c 'grep -iE "grant|credential|refus" /tmp/daemon.log | tail -4' 2>/dev/null | sed 's/^/         /'
fi

# --- revoke: enforced by the other node ----------------------------------
BD=$(b shrooms keys 2>&1 | sed -n 's/^device  *//p' | head -1)
revoked=$(printf '%s\n' "$PIN" | a shrooms admin revoke --device "$BD" 2>&1)
grep -qE "^Revoked " <<<"$revoked" &&
  ok "the card signed a revocation" || { bad "revoke failed"; note "$(tail -2 <<<"$revoked" | tr '\n' ' ')"; }

n=0
while [ "$n" -lt 90 ]; do
  a shrooms status 2>/dev/null | grep -q beta || break
  sleep 3; n=$((n+3))
done
a shrooms status 2>/dev/null | grep -q beta &&
  bad "node B is still on the roster ${n}s after being revoked" ||
  ok "node B is gone from the mesh (${n}s)"

# --- teardown -------------------------------------------------------------
down
[ -f "$PAIRDIR/keycard-pairing" ] &&
  ok "the pairing survives, so the next run spends no slot" ||
  bad "the pairing was destroyed"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
