#!/usr/bin/env bash
# Common setup for the F-03 end-to-end scenarios: a two-node mesh and a third
# process that announces the second node's WireGuard public key without
# holding its private key. The new-announce variant asserts the announce is
# refused; the remembered variant seeds a duplicate into the second node's
# remembered roster and asserts a restart does not bring it back.
#
# Needs root and /dev/net/tun, like scripts/e2e-two-nodes.sh.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

BIN=$(pwd)/bin/shrooms
ATT=$(pwd)/bin/shrooms-attacker
WORK=$(mktemp -d)
SOCKS=$(mktemp -d /tmp/e2e-f03-sock-XXXXXX)
PIDS=()
PASS=0; FAIL=0

cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done
  sleep 1
  for p in "${PIDS[@]:-}"; do kill -9 "$p" 2>/dev/null; done
  rm -rf "$WORK" "$SOCKS"
}
trap cleanup EXIT

ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
note() { printf '         %s\n' "$1"; }
die()  { bad "$1"; exit 1; }

wait_for() { # wait_for <seconds> <path>
  local n=0
  while [ ! -e "$2" ] && [ "$n" -lt "$1" ]; do sleep 1; n=$((n+1)); done
  [ -e "$2" ]
}
wait_log() { # wait_log <seconds> <file> <extended-regex>
  local n=0
  while [ "$n" -lt "$1" ]; do
    grep -qE "$3" "$2" 2>/dev/null && return 0
    sleep 1; n=$((n+1))
  done
  return 1
}
saw() { "$BIN" status --socket "$1" 2>/dev/null | grep -q "$2"; }

A_WG=51990; A_DELIVERY=31990; A_IF=f03a
B_WG=51991; B_DELIVERY=31991; B_IF=f03b
C_WG=51992; C_DELIVERY=31992; C_IF=f03c

start_node() { # start_node <dir> <socket> <binary> [env…]
  local dir=$1 sock=$2 bin=$3; shift 3
  env "$@" "$bin" daemon --config "$dir/config.toml" --state "$dir/state" \
    --socket "$sock" >"$dir/daemon.log" 2>&1 &
  PIDS+=($!)
}

prepare_node() { # prepare_node <dir> <name> <port>
  local dir=$1 name=$2 port=$3
  mkdir -p "$dir/state"
  "$BIN" prepare --config "$dir/config.toml" --state "$dir/state" \
    --name "$name" --port "$port" >"$dir/prepare.log" 2>&1 || die "prepare $name"
  sed -i '/^network_key = /d' "$dir/config.toml"
  {
    printf 'mesh.f03.key = "%s"\n' "$MESH_KEY"
    printf 'mesh.f03.inherits_identity = "true"\n'
    printf 'mesh.f03.iface = "%s"\nmesh.f03.port = "%s"\n' "$4" "$port"
    grep '^mesh\.f03\.admin_keys' "$WORK/a/config.toml"
    printf 'delivery_port = %s\n' "$5"
    printf 'entry_nodes = ["/ip4/127.0.0.1/tcp/%s/p2p/%s"]\n' "$A_DELIVERY" "$PEER_A"
  } >>"$dir/config.toml"
}

node_keys() { # node_keys <dir> → DEV WG SEAL on separate lines
  "$BIN" keys --state "$1/state" 2>&1 |
    sed -n -e 's/^device  *//p' -e 's/^tunnel  *//p' -e 's/^sealing  *//p'
}

issue() { # issue <name> <device> <wg> <seal> → credential blob
  printf 'pw\n' | "$BIN" admin issue --dir "$WORK/a/admin" \
    --config "$WORK/a/config.toml" --name "$1" --device "$2" --wg "$3" --seal "$4" \
    --write=false 2>&1 |
    sed -n 's/.*credential set //p' | tr -d ' \n'
}

setup_mesh() {
  [ -x "$BIN" ] || die "no $BIN — run 'make shrooms'"
  [ -x "$ATT" ] || die "no $ATT — run 'make shrooms-attacker'"
  [ "$(id -u)" -eq 0 ] || die "needs root: a WireGuard device is CAP_NET_ADMIN"
  [ -e /dev/net/tun ] || modprobe tun 2>/dev/null
  [ -e /dev/net/tun ] || die "no /dev/net/tun"

  echo "==> F-03 lab: alpha, beta and a gamma that announces beta's tunnel key"
  mkdir -p "$WORK/a" "$WORK/b" "$WORK/c"
  printf 'pw\npw\n' | "$BIN" init --mesh f03 --config "$WORK/a/config.toml" \
    --state "$WORK/a/state" --admin-dir "$WORK/a/admin" --name alpha --port "$A_WG" \
    >"$WORK/a/init.log" 2>&1 || die "alpha init"
  sed -i 's|^mesh\.f03\.iface = .*|mesh.f03.iface = "'"$A_IF"'"|' "$WORK/a/config.toml"
  printf 'delivery_port = %s\n' "$A_DELIVERY" >>"$WORK/a/config.toml"
  start_node "$WORK/a" "$SOCKS/a.sock" "$BIN"
  wait_for 90 "$SOCKS/a.sock" || die "alpha never bound its socket"
  ok "alpha is up"

  PEER_A=$(grep -o 'peer_id=[A-Za-z0-9]*' "$WORK/a/daemon.log" | head -1 | cut -d= -f2)
  [ ${#PEER_A} -gt 20 ] || die "no peer id for alpha"
  MESH_KEY=$("$BIN" key show --config "$WORK/a/config.toml" 2>/dev/null | tail -1)
  [ -n "$MESH_KEY" ] || die "no mesh key"

  prepare_node "$WORK/b" beta "$B_WG" "$B_IF" "$B_DELIVERY" || die "beta prepare"
  prepare_node "$WORK/c" gamma "$C_WG" "$C_IF" "$C_DELIVERY" || die "gamma prepare"

  { read -r DEV_B; read -r WG_B; read -r SEAL_B; } < <(node_keys "$WORK/b")
  { read -r DEV_C; read -r WG_C; read -r SEAL_C; } < <(node_keys "$WORK/c")
  [ -n "$DEV_B$WG_B$SEAL_B$DEV_C$WG_C$SEAL_C" ] || die "no beta/gamma keys"
  BETA_WG=$WG_B

  B_BLOB=$(issue beta "$DEV_B" "$WG_B" "$SEAL_B"); [ -n "$B_BLOB" ] || die "beta credential"
  C_BLOB=$(issue gamma "$DEV_C" "$BETA_WG" "$SEAL_C"); [ -n "$C_BLOB" ] || die "gamma credential"
  "$BIN" credential set "$B_BLOB" --config "$WORK/b/config.toml" --state "$WORK/b/state" >/dev/null 2>&1 || die "beta credential set"
  "$BIN" credential set "$C_BLOB" --config "$WORK/c/config.toml" --state "$WORK/c/state" >/dev/null 2>&1 || die "gamma credential set"
  ok "beta has its own credential; gamma has beta's tunnel key"
}

start_beta() { # start_beta <binary> [env…]
  local bin=$1; shift
  start_node "$WORK/b" "$SOCKS/b.sock" "$bin" "$@"
  wait_for 90 "$SOCKS/b.sock" || die "beta never bound its socket"
}
start_gamma() {
  start_node "$WORK/c" "$SOCKS/c.sock" "$ATT" SHROOMS_ANNOUNCE_WG="$BETA_WG"
  wait_for 90 "$SOCKS/c.sock" || die "gamma never bound its socket"
}

summary() {
  echo
  echo "  $PASS passed, $FAIL failed"
  [ "$FAIL" -eq 0 ]
}
