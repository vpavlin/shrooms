#!/usr/bin/env bash
# Two nodes, one machine, a mesh that actually comes up — and no public fleet.
#
# m1 does this over the Logos fleet and waits up to four minutes for discovery,
# which is why it has never run in CI: it is somebody else's network, and a red
# build nobody caused teaches you to ignore red builds.
#
# Here the fleet is local. Node A pins its rendezvous port and node B is pointed
# straight at it with entry_nodes, so discovery is two processes on loopback and
# takes seconds. What that costs is realism about NAT and the internet, which is
# exactly what m1/m2/m3 are for; what it buys is that minting, admitting and
# reaching another device get tested on every push.
#
# NEEDS ROOT: a WireGuard device is CAP_NET_ADMIN. Everything else in the
# management suite runs unprivileged; this one cannot.
set -uo pipefail

cd "$(dirname "$0")/.."
BIN=${BIN:-$(pwd)/bin/shrooms}
# Node B from another build, when given: the July library against v0.40
# (ADR-071), since a fleet moves one machine at a time.
BIN_B=${BIN_B:-$BIN}
WORK=$(mktemp -d)
SOCKS=$(mktemp -d /tmp/e2e-sock-XXXXXX)   # short: a unix path is capped near 108
PIDS=()

NS=e2eb$$
cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done
  sleep 1
  for p in "${PIDS[@]:-}"; do kill -9 "$p" 2>/dev/null; done
  ip netns del "$NS" 2>/dev/null
  rm -rf "$WORK" "$SOCKS"
}
trap cleanup EXIT

PASS=0; FAIL=0
ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
note() { printf '         %s\n' "$1"; }

[ -x "$BIN" ] || { echo "no $BIN — run 'make shrooms'"; exit 1; }
[ -x "$BIN_B" ] || { echo "no $BIN_B"; exit 1; }
[ "$(id -u)" -eq 0 ] || { echo "needs root: a WireGuard device is CAP_NET_ADMIN"; exit 77; }
[ -e /dev/net/tun ] || modprobe tun 2>/dev/null
[ -e /dev/net/tun ] || { echo "no /dev/net/tun and modprobe did not help"; exit 77; }

# Node B in a network namespace of its own, joined to this one by a veth pair.
# On one kernel the two overlay addresses are both local: a connection between
# them is delivered on the spot and never crosses WireGuard, so anything that
# needs the tunnel to have carried it — a clip, which knows its sender by the
# address it arrived from — could not be tested at all.
ip netns add "$NS" || { echo "cannot make a network namespace"; exit 77; }
ip link add e2eva type veth peer name e2evb netns "$NS"
ip addr add 10.199.0.1/24 dev e2eva && ip link set e2eva up
ip -n "$NS" addr add 10.199.0.2/24 dev e2evb && ip -n "$NS" link set e2evb up && ip -n "$NS" link set lo up
INB=(ip netns exec "$NS")

# Ports well clear of anything a developer is likely to be running.
A_WG=51990; A_DELIVERY=31990; A_IF=e2ea
B_WG=51991; B_DELIVERY=31991; B_IF=e2eb

echo "==> two nodes, local fleet"

# --- node A: mints the mesh, and is the fleet ------------------------------
mkdir -p "$WORK/a"
# The mesh is named, as every first mesh is (docs/one-kind-of-mesh.md), and
# its interface is pinned on the mesh itself — so a top-level `interface`
# appended afterwards would no longer move it. The pin is set instead.
printf 'pw\npw\n' | "$BIN" init --mesh e2e --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --admin-dir "$WORK/a/admin" --name alpha --port "$A_WG" >"$WORK/a/init.log" 2>&1
sed -i 's|^mesh\.e2e\.iface = .*|mesh.e2e.iface = "'"$A_IF"'"|' "$WORK/a/config.toml"
printf 'delivery_port = %d\n' "$A_DELIVERY" >> "$WORK/a/config.toml"
# A LAN host written the way people write it, without "/tcp:": a daemon that
# cannot use its lan_hosts must still bring its mesh up (k11, 2026-11-04).
printf 'mesh.e2e.lan_hosts = ["ha=192.168.0.116:8123"]\n' >> "$WORK/a/config.toml"

"$BIN" daemon --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --socket "$SOCKS/a.sock" >"$WORK/a/daemon.log" 2>&1 &
PIDS+=($!)

wait_for() { # wait_for <seconds> <file-or-socket>
  local n=0
  while [ ! -e "$2" ] && [ "$n" -lt "$1" ]; do sleep 1; n=$((n+1)); done
  [ -e "$2" ]
}

if wait_for 90 "$SOCKS/a.sock"; then ok "node A is up"; else
  bad "node A never bound its socket"
  note "$(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' "$WORK/a/daemon.log" | tail -3)"
  exit 1
fi
if grep -q 'LAN hosts not offered' "$WORK/a/daemon.log"; then
  ok "node A is up despite a lan_hosts line it cannot use, and says so"
else
  bad "node A did not report its unusable lan_hosts"
fi

PEER_A=$(grep -o 'peer_id=[A-Za-z0-9]*' "$WORK/a/daemon.log" | head -1 | cut -d= -f2)
if [ ${#PEER_A} -gt 20 ]; then ok "node A reports a dialable identity"; else
  bad "no usable peer id for node A (got '${PEER_A}')"; exit 1
fi

# --- node B: same mesh, bootstrapped from A and nothing else ---------------
mkdir -p "$WORK/b"
"$BIN_B" prepare --config "$WORK/b/config.toml" --state "$WORK/b/state" \
    --name beta --port "$B_WG" >"$WORK/b/prepare.log" 2>&1
# The key is written into B's config directly, which is what a config that
# carries it looks like. `set-key` did this until joining by key was removed
# (b78fae6); the invite that replaced it is a separate flow, and this suite is
# about two enrolled nodes finding each other.
#
# In the named form a join writes: the mesh called what A calls it, holding B's
# base identity, with the placeholder `prepare` left removed — a config still
# carrying it reads as waiting for a mesh, and the daemon would wait forever.
KEY=$("$BIN" key show --config "$WORK/a/config.toml" 2>/dev/null | tail -1)
sed -i '/^network_key = /d' "$WORK/b/config.toml"
{
  printf 'mesh.e2e.key = "%s"\n' "$KEY"
  printf 'mesh.e2e.inherits_identity = "true"\n'
  printf 'mesh.e2e.iface = "%s"\nmesh.e2e.port = "%d"\n' "$B_IF" "$B_WG"
} >> "$WORK/b/config.toml"
if [ -n "$KEY" ] && grep -q '^mesh.e2e.key = "'"$KEY"'"' "$WORK/b/config.toml"; then
  ok "node B has the mesh key"
else
  bad "node B never got the mesh key"
  note "key show gave '${KEY}'"
  exit 1
fi
{
  printf 'delivery_port = %d\n' "$B_DELIVERY"
  printf 'entry_nodes = ["/ip4/10.199.0.1/tcp/%d/p2p/%s"]\n' "$A_DELIVERY" "$PEER_A"
  grep '^mesh\.e2e\.admin_keys' "$WORK/a/config.toml"
} >> "$WORK/b/config.toml"
ok "node B is pointed at node A as its only bootstrap"

# B needs a credential before A will look at it.
DEV=$("$BIN_B" keys --state "$WORK/b/state" 2>&1 | sed -n 's/^device  *//p'  | head -1)
WG=$( "$BIN_B" keys --state "$WORK/b/state" 2>&1 | sed -n 's/^tunnel  *//p'  | head -1)
SEAL=$("$BIN_B" keys --state "$WORK/b/state" 2>&1 | sed -n 's/^sealing  *//p' | head -1)
BLOB=$(printf 'pw\n' | "$BIN" admin issue --dir "$WORK/a/admin" \
    --config "$WORK/a/config.toml" --state "$WORK/b/state" \
    --name beta --device "$DEV" --wg "$WG" --seal "$SEAL" --write=false 2>&1 |
    sed -n 's/.*credential set //p' | tr -d ' \n')
if [ -n "$BLOB" ]; then ok "node A issues node B a credential"; else
  bad "no credential issued"; exit 1
fi
"$BIN_B" credential set "$BLOB" --config "$WORK/b/config.toml" --state "$WORK/b/state" \
  >/dev/null 2>&1

"${INB[@]}" "$BIN_B" daemon --config "$WORK/b/config.toml" --state "$WORK/b/state" \
    --socket "$SOCKS/b.sock" >"$WORK/b/daemon.log" 2>&1 &
PIDS+=($!)
if wait_for 90 "$SOCKS/b.sock"; then ok "node B is up"; else
  bad "node B never bound its socket"
  note "$(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' "$WORK/b/daemon.log" | tail -3)"
  exit 1
fi

# --- do they find each other, and does a tunnel come up? -------------------
saw() { "$BIN" status --socket "$1" 2>/dev/null | grep -q "$2"; }

DISCOVERY=${DISCOVERY:-120}
n=0
while [ "$n" -lt "$DISCOVERY" ]; do
  if saw "$SOCKS/a.sock" beta && saw "$SOCKS/b.sock" alpha; then break; fi
  sleep 2; n=$((n+2))
done
if saw "$SOCKS/a.sock" beta && saw "$SOCKS/b.sock" alpha; then
  ok "each node has the other on its roster (${n}s)"
else
  bad "they never saw each other in ${DISCOVERY}s"
  note "A: $("$BIN" status --socket "$SOCKS/a.sock" 2>&1 | head -3 | tr '\n' ' ')"
  note "B: $("$BIN_B" status --socket "$SOCKS/b.sock" 2>&1 | head -3 | tr '\n' ' ')"
fi

# A credential that verifies is the difference between being seen and being
# admitted, so assert the thing that would be missing if it did not.
"$BIN_B" status --socket "$SOCKS/b.sock" 2>&1 | grep -q "no credential" &&
  bad "node B still reports itself unenrolled" ||
  ok "node B is admitted, not merely present"

n=0
while [ "$n" -lt 60 ]; do
  if saw "$SOCKS/a.sock" "up "; then break; fi
  sleep 2; n=$((n+2))
done
if saw "$SOCKS/a.sock" "up "; then ok "a tunnel is established (${n}s)"; else
  bad "no handshake in 60s"
  note "$("$BIN" status --socket "$SOCKS/a.sock" 2>&1 | tail -3 | tr '\n' ' ')"
fi

# --- a clip each way (ADR-083) ---------------------------------------------
# Through the tunnel, named by its sender's address: B learns it came from
# alpha because WireGuard only takes alpha's address from alpha.
CLIP="e2e clip $$ https://example.org/?a=1&b=2"
"$BIN" copy --socket "$SOCKS/a.sock" --to beta "$CLIP" >"$WORK/a/copy.log" 2>&1 ||
  bad "node A could not send a clip: $(cat "$WORK/a/copy.log")"
n=0
while [ "$n" -lt 40 ]; do
  [ "$("$BIN_B" paste --socket "$SOCKS/b.sock" 2>/dev/null)" = "$CLIP" ] && break
  sleep 2; n=$((n+2))
done
if [ "$("$BIN_B" paste --socket "$SOCKS/b.sock" 2>/dev/null)" = "$CLIP" ]; then
  ok "a clip goes from A to B (${n}s)"
else
  bad "the clip never reached node B"
  note "$("$BIN" clips --socket "$SOCKS/a.sock" 2>&1 | tail -2 | tr '\n' ' ')"
  grep -i clip "$WORK/a/daemon.log" | tail -3 | sed 's/^/         /'
fi
if "$BIN_B" clips --json --socket "$SOCKS/b.sock" 2>/dev/null | grep -q '"from":"alpha"'; then
  ok "node B knows it came from alpha"
else
  bad "node B does not say who sent the clip"
fi
"$BIN_B" copy --socket "$SOCKS/b.sock" --all --secret "e2e secret $$" >/dev/null 2>&1
n=0
while [ "$n" -lt 40 ]; do
  [ "$("$BIN" paste --socket "$SOCKS/a.sock" 2>/dev/null)" = "e2e secret $$" ] && break
  sleep 2; n=$((n+2))
done
if [ "$("$BIN" paste --socket "$SOCKS/a.sock" 2>/dev/null)" = "e2e secret $$" ]; then
  ok "a secret goes from B to all of the mesh (${n}s)"
else
  bad "the secret never reached node A"
fi
if grep -q "e2e secret $$" "$WORK/a/state/clips.json" "$WORK/b/state/clips.json" 2>/dev/null; then
  bad "a secret was written to disk"
else
  ok "the secret is on neither disk"
fi

# --- does a restart come back on remembered endpoints? --------------------
#
# The remembered roster is meant to give WireGuard an endpoint to try before the
# rendezvous plane is up, so a restarted node reaches its peers without waiting
# to be told where they are. That claim has never been checked: it needs a node
# that has peers, a restart, and a look at what happened first.
#
# "from=memory" on a tunnel means exactly that — the handshake completed on a
# stored endpoint before any announce arrived. It was structurally impossible
# before the roster was persisted, so it is a yes-or-no rather than a judgement
# about whether something felt quicker.
kill "${PIDS[1]}" 2>/dev/null; sleep 2
mv "$WORK/b/daemon.log" "$WORK/b/daemon-first.log"

if [ -f "$WORK/b/state/roster-"*.json ] 2>/dev/null || ls "$WORK/b/state"/roster-*.json >/dev/null 2>&1; then
  ok "node B wrote down its roster"
else
  bad "node B kept no roster to come back to"
fi

"${INB[@]}" "$BIN_B" daemon --config "$WORK/b/config.toml" --state "$WORK/b/state" \
    --socket "$SOCKS/b2.sock" >"$WORK/b/daemon.log" 2>&1 &
PIDS+=($!)
if wait_for 90 "$SOCKS/b2.sock"; then ok "node B restarts"; else
  bad "node B did not come back"
fi

n=0
while [ "$n" -lt 60 ]; do
  grep -q "tunnel established" "$WORK/b/daemon.log" && break
  sleep 2; n=$((n+2))
done
if grep -q "from=memory" "$WORK/b/daemon.log"; then
  ok "a tunnel came up on a remembered endpoint, before any announce"
elif grep -q "tunnel established" "$WORK/b/daemon.log"; then
  bad "the tunnel waited for an announce; the remembered endpoint was not used"
  note "$(grep 'tunnel established' "$WORK/b/daemon.log" | head -1)"
else
  bad "no tunnel at all ${n}s after the restart"
fi
note "what node B remembered:"
for f in "$WORK/b/state"/roster-*.json; do
  [ -f "$f" ] || continue
  python3 - "$f" <<'PYEOF' | sed 's/^/         /'
import json,sys
for p in (json.load(open(sys.argv[1])).get("peers") or []):
    print(" ", p.get("name"), "endpoints:", p.get("endpoints"), "seen:", p.get("seen"))
PYEOF
done
grep -E "remembered peers|tunnel established|peer discovered|provisional" "$WORK/b/daemon.log" |
  sed 's/^/         /' | head -6

# --- revocation, which is the one that has to work --------------------------
#
# A revoked device must stop being carried, and the check is on the OTHER node:
# revocation is something peers enforce, not something the revoked device
# cooperates with.
DEV_B=$("$BIN_B" keys --state "$WORK/b/state" 2>&1 | sed -n 's/^device  *//p' | head -1)
# No --config: admin revoke does not take one, and flag.ExitOnError turns an
# unknown flag into usage text and an exit — which a grep for "revok" then
# matched, reporting a revocation that never happened.
printf 'pw\n' | "$BIN" admin revoke --dir "$WORK/a/admin" \
    --socket "$SOCKS/a.sock" --device "$DEV_B" >"$WORK/a/revoke.log" 2>&1
REVOKE_RC=$?
# What it printed, always: a loose grep for "revok" matches the word in an
# error just as happily as in a success, which is how a failing revocation
# reported itself as working.
sed 's/^/         /' "$WORK/a/revoke.log" | head -6
# The exit status, not a word in the output: an error mentioning revocation
# reads exactly like a success to a grep.
if [ "$REVOKE_RC" -eq 0 ]; then ok "node A revokes node B"; else
  bad "revocation did not go out (exit $REVOKE_RC)"
fi

n=0
while [ "$n" -lt 90 ]; do
  "$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q "beta" || break
  sleep 3; n=$((n+3))
done
if "$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q "beta"; then
  bad "node B is still on node A's roster ${n}s after being revoked"
  note "$("$BIN" status --socket "$SOCKS/a.sock" 2>&1 | tail -3 | tr '\n' ' ')"
  grep -iE "revok" "$WORK/a/daemon.log" | sed 's/^/         /' | tail -4
else
  ok "node B is gone from node A's roster (${n}s)"
fi

# --- a config edit the daemon refuses (ADR-080) -----------------------------
# Node A started cleanly above, so it kept that config as the last good one.
# An edit it refuses must not keep it down: it starts on the last good copy,
# says so in status, and leaves the edited file as it was.
kill "${PIDS[0]}" 2>/dev/null
n=0; while kill -0 "${PIDS[0]}" 2>/dev/null && [ "$n" -lt 20 ]; do sleep 1; n=$((n+1)); done
rm -f "$SOCKS/a.sock"
if [ -f "$WORK/a/config.toml.last-good" ]; then ok "node A kept the config it started with"; else
  bad "node A kept no last good config"
fi
printf 'listen_port = "not a port\n' >> "$WORK/a/config.toml"
cp "$WORK/a/config.toml" "$WORK/a/edited.toml"
"$BIN" daemon --config "$WORK/a/config.toml" --state "$WORK/a/state" \
    --socket "$SOCKS/a.sock" >"$WORK/a/daemon2.log" 2>&1 &
PIDS+=($!)
if wait_for 90 "$SOCKS/a.sock"; then ok "node A is up after a config edit it refuses"; else
  bad "a refused config edit kept node A down"
  note "$(grep -vE '^(INF|DBG|WRN|ERR|TRC|NTC) 20' "$WORK/a/daemon2.log" | tail -3)"
fi
if "$BIN" status --socket "$SOCKS/a.sock" 2>/dev/null | grep -q "CONFIG REFUSED"; then
  ok "status says it runs on the last good config"
else
  bad "status does not say the config was refused"
fi
if cmp -s "$WORK/a/config.toml" "$WORK/a/edited.toml"; then ok "the edited config is left as it was"; else
  bad "the edited config was changed"
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
