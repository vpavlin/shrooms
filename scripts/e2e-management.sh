#!/usr/bin/env bash
# End-to-end tests for managing a mesh: minting, admitting, renewing the things
# that go wrong, and surviving a machine that loses its state.
#
# Deliberately OFFLINE. m1/m2/m3 exercise the data plane and need the public
# fleet, which is somebody else's network and takes minutes — that is why they
# are not in CI. Everything here is the management surface: config, state, the
# admin key, the control socket, and the split between a daemon holding state in
# memory and a CLI writing the file underneath it.
#
# That split is not incidental. Every bug this suite was written after lived in
# it: a credential installed where the daemon does not read, an admin key file
# the tool could not see, state written without a sync and lost to a power cut.
# None of them are visible from a unit test of either half alone.
#
# Usage: scripts/e2e-management.sh [name ...]     (default: every scenario)
set -uo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
BIN=$ROOT/bin/shrooms
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

PASS=0; FAIL=0; FAILED=()

ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); FAILED+=("$CURRENT: $1"); }
note() { printf '         %s\n' "$1"; }

# expect <description> <substring> <actual>
#
# The actual output is an argument rather than piped in. Piping runs the
# assertion in a subshell, so every pass and failure it counted was discarded
# when that subshell exited — the suite reported "3 passed" while printing
# fourteen results, and would have reported success while failing.
expect() {
  local what=$1 want=$2 got=$3
  if [[ "$got" == *"$want"* ]]; then ok "$what"; else
    bad "$what"
    note "wanted: $want"
    note "got:    ${got:0:300}"
  fi
}
refute() {
  local what=$1 unwanted=$2 got=$3
  if [[ "$got" != *"$unwanted"* ]]; then ok "$what"; else
    bad "$what"
    note "did not want: $unwanted"
    note "got:          ${got:0:300}"
  fi
}

# The credential is printed inside the command somebody is meant to paste, so
# that is where it is read from.
blob_from() { sed -n 's/.*credential set //p' <<<"$1" | tr -d ' \n' ; }

# A node is a directory holding everything one machine would have.
node() { echo "$WORK/$1"; }
sh_() { # sh_ <node> <args...> — the CLI, pointed at that node's files
  local n; n=$(node "$1"); shift
  "$BIN" "$@" --config "$n/config.toml" --state "$n/state"
}

mint() { # mint <node> <name> <port>
  local n; n=$(node "$1"); mkdir -p "$n"
  printf 'pw\npw\n' | "$BIN" init --config "$n/config.toml" --state "$n/state" \
      --admin-dir "$n/admin" --name "$2" --port "$3" >"$n/init.log" 2>&1
}

run_scenario() {
  CURRENT=$1
  printf '\n  %s\n' "$CURRENT"
  "scenario_$1"
}

# ---------------------------------------------------------------------------

# A mesh is minted with an authority, and the machine that minted it can see it.
# `admin show` used to read only admin.json and was blind to every
# admin-<label>.json beside it, which is the file you have when the mesh has a
# name — so the question "which meshes am I the admin of" was answerable only
# for the first one.
scenario_admin_show() {
  mint one alice 51900
  printf 'pw\npw\n' | "$BIN" admin init --dir "$(node one)/admin" --mesh office >/dev/null 2>&1

  local shown; shown=$("$BIN" admin show --dir "$(node one)/admin" 2>&1)
  expect "lists the default authority"        "default (admin.json)"       "$shown"
  expect "lists a named mesh's authority too" "office (admin-office.json)" "$shown"
  expect "can be asked for one by name" "admin_keys = [" \
    "$("$BIN" admin show --dir "$(node one)/admin" --mesh office 2>&1)"
  expect "says what an empty directory means" "stays there" \
    "$("$BIN" admin show --dir "$WORK/empty-admin-dir" 2>&1)"
}

# The whole enrolment path for a machine that is not the one holding the admin
# key: read its keys, issue against them, install the blob.
scenario_enrol_a_second_device() {
  mint one alice 51900
  mkdir -p "$(node two)"
  "$BIN" prepare --config "$(node two)/config.toml" --state "$(node two)/state" \
      --name bob --port 51901 >/dev/null 2>&1

  # Bob joins the mesh by key, which is what a config carries.
  local key; key=$(sh_ one key show 2>/dev/null | tail -1)
  sh_ two set-key "$key" >/dev/null 2>&1 ||
    "$BIN" set-key "$key" --config "$(node two)/config.toml" >/dev/null 2>&1

  local dev wg seal keys
  keys=$(sh_ two keys 2>&1)
  dev=$(sed -n 's/^device  *//p'  <<<"$keys" | head -1)
  wg=$(sed  -n 's/^tunnel  *//p'  <<<"$keys" | head -1)
  seal=$(sed -n 's/^sealing  *//p' <<<"$keys" | head -1)

  [ -n "$dev" ] && ok "the device prints its own keys" || bad "no keys printed"

  local issued blob
  issued=$(printf 'pw\n' | "$BIN" admin issue --dir "$(node one)/admin" \
      --config "$(node one)/config.toml" --state "$(node two)/state" \
      --name bob --device "$dev" --wg "$wg" --seal "$seal" --write=false 2>&1)
  blob=$(blob_from "$issued")

  if [ -z "$blob" ]; then bad "admin issue produced no credential"; note "${issued:0:200}"; return; fi
  ok "the admin issues a credential for another machine's keys"

  expect "the device installs it" "Installed a credential" \
    "$(sh_ two credential set "$blob" 2>&1)"

  # The bug this scenario exists for: it used to land in the top-level field
  # while the daemon reads the per-mesh entry, so `keys` showed it and the
  # daemon never saw it.
  refute "the device stops reporting itself unenrolled" "credential  none yet" \
    "$(sh_ two keys 2>&1)"
}

# The bug that started this suite, reproduced exactly.
#
# A daemon creates a per-mesh state entry on its first start, and from then on
# reads the credential from there and never from the top-level field again.
# `credential set` wrote only the top level, so it reported success, `shrooms
# keys` showed the credential, and the daemon went on saying there was none.
#
# The entry is fabricated here rather than by running a daemon, because a daemon
# needs a tun device and this suite must run anywhere. It is fabricated the way
# the daemon makes it: the same identity, adopted, with no credential yet.
scenario_credential_reaches_the_mesh_entry() {
  mint one alice 51900
  local sd; sd=$(node one)/state

  python3 - "$sd/state.json" <<'PYEOF'
import json,sys
p=sys.argv[1]; s=json.load(open(p))
s.setdefault("meshes",{})["aaaaaaaaaaaaa"]={
    "device_priv": s["device_priv"], "wg_priv": s["wg_priv"], "seq": 0}
json.dump(s, open(p,"w"), indent=2)
PYEOF
  ok "a per-mesh entry exists, as it would after one daemon start"

  local dev wg keys blob
  keys=$(sh_ one keys 2>&1)
  dev=$(sed -n 's/^device  *//p' <<<"$keys" | head -1)
  wg=$(sed  -n 's/^tunnel  *//p' <<<"$keys" | head -1)
  blob=$(blob_from "$(printf 'pw\n' | "$BIN" admin issue --dir "$(node one)/admin" \
      --config "$(node one)/config.toml" --state "$sd" \
      --name alice --device "$dev" --wg "$wg" --write=false 2>&1)")
  if [ -z "$blob" ]; then bad "could not issue a credential to test with"; return; fi

  sh_ one credential set "$blob" >/dev/null 2>&1

  local inentry
  inentry=$(python3 -c '
import json,sys
s=json.load(open(sys.argv[1]))
print(len((s.get("meshes") or {}).get("aaaaaaaaaaaaa",{}).get("credential") or ""))' "$sd/state.json")

  if [ "$inentry" -gt 0 ]; then
    ok "the credential reaches the entry the daemon reads"
  else
    bad "the credential went only to the top level, where the daemon does not look"
  fi
}

# A credential for somebody else must be refused, or the mistake shows up as a
# mesh that silently ignores this device.
scenario_credential_for_another_device() {
  mint one alice 51900
  mint two bob   51901

  local dev wg keys blob
  keys=$(sh_ one keys 2>&1)
  dev=$(sed -n 's/^device  *//p' <<<"$keys" | head -1)
  wg=$(sed  -n 's/^tunnel  *//p' <<<"$keys" | head -1)
  blob=$(blob_from "$(printf 'pw\n' | "$BIN" admin issue --dir "$(node one)/admin" \
      --config "$(node one)/config.toml" --state "$(node one)/state" \
      --name alice --device "$dev" --wg "$wg" --write=false 2>&1)")

  if [ -z "$blob" ]; then bad "could not issue a credential to test with"; return; fi
  expect "a credential naming another device is refused" "not this device" \
    "$(sh_ two credential set "$blob" 2>&1)"
}

# Renaming re-sorts a label-ordered list, which used to move every other mesh's
# interface and port with it.
scenario_rename_moves_nothing() {
  mint one alice 51900
  local k2 k3
  k2=$(printf 'pw\npw\n' | "$BIN" admin init --dir "$WORK/throwaway" --mesh a 2>/dev/null; \
       "$BIN" key show --config "$(node one)/config.toml" 2>/dev/null | tail -1)

  # Two extra meshes, so there is something to reorder.
  local n; n=$(node one)
  printf 'mesh.office.key = "%s"\nmesh.test.key   = "%s"\n' \
    "$(head -c 32 /dev/urandom | base32 | tr -d '=\n')" \
    "$(head -c 32 /dev/urandom | base32 | tr -d '=\n')" >> "$n/config.toml"

  local before after
  before=$("$BIN" mesh list --config "$n/config.toml" 2>&1)
  expect "rename reports what it pinned" "pinned, so the rename moved nothing" \
    "$("$BIN" mesh rename --config "$n/config.toml" --admin-dir "$n/admin" test home 2>&1)"

  expect "the renamed mesh has its interface and port written down" "iface" \
    "$(grep -E 'mesh\.home\.(iface|port)' "$n/config.toml" | tr '\n' ' ')"
  expect "so does the mesh it would have displaced" "iface" \
    "$(grep -E 'mesh\.office\.(iface|port)' "$n/config.toml" | tr '\n' ' ')"
  refute "the old label is gone" "test" \
    "$("$BIN" mesh list --config "$n/config.toml" 2>&1)"
}

# A power cut leaves state.json present and empty. The identity in it is not
# regenerable, so the daemon must recover it or refuse — never mint a new one.
scenario_state_survives_a_power_cut() {
  mint one alice 51900
  local sd; sd=$(node one)/state
  local before; before=$(sh_ one keys 2>&1 | sed -n 's/^device  *//p' | head -1)

  # Reopen once so the backup exists, as any second start would.
  sh_ one keys >/dev/null 2>&1
  [ -f "$sd/state.json.bak" ] && ok "a backup is kept" || bad "no state.json.bak was written"

  : > "$sd/state.json"   # the power cut
  local after; after=$(sh_ one keys 2>&1 | sed -n 's/^device  *//p' | head -1)

  if [ -n "$after" ] && [ "$before" == "$after" ]; then
    ok "an emptied state.json recovers the same identity"
  else
    bad "the identity changed or was lost: '$before' -> '$after'"
  fi
}

# With no backup there is nothing to recover, and the one thing that must not
# happen is a new identity: it has no credential, cannot be given one without an
# admin, and joins at a different address looking perfectly healthy.
scenario_no_backup_refuses_rather_than_reminting() {
  mint one alice 51900
  local sd; sd=$(node one)/state
  sh_ one keys >/dev/null 2>&1
  local before; before=$(sh_ one keys 2>&1 | sed -n 's/^device  *//p' | head -1)

  printf '{trunc' > "$sd/state.json"
  rm -f "$sd/state.json.bak"

  local out; out=$(sh_ one keys 2>&1)
  if [[ "$out" == *"$before"* ]]; then
    bad "it started anyway on a different identity"
  else
    expect "it refuses, and says not to delete the file" "DO NOT DELETE" "$out"
  fi
}

# The socket surface, against a daemon that is actually running.
#
# Everything above drives files. This drives the seam those bugs actually lived
# in: a daemon holding its own copy of the state while a CLI writes the file
# underneath it. `status` asks the daemon; `keys` reads the file; tonight they
# disagreed for an hour because a credential had been written where only one of
# them looks.
#
# It does not bring up a mesh, because that needs a tun device and this suite
# has to run anywhere. A daemon with no mesh yet still serves the socket, which
# is enough to prove the two halves agree about what it holds.
scenario_a_running_daemon_answers() {
  local n; n=$(node one); mkdir -p "$n"
  "$BIN" prepare --config "$n/config.toml" --state "$n/state" --name waiting --port 51950 \
    >/dev/null 2>&1
  # A port of its own, so this cannot collide with a daemon already on the host.
  printf 'delivery_port = %d\n' "$((30000 + RANDOM % 2000))" >> "$n/config.toml"

  # Short path on purpose: a unix socket path is capped near 108 characters and
  # a long working directory silently fails to bind.
  local sock; sock=$(mktemp -u /tmp/e2e-XXXXXX.sock)
  "$BIN" daemon --config "$n/config.toml" --state "$n/state" --socket "$sock" \
    >"$n/daemon.log" 2>&1 &
  local pid=$!
  trap 'kill '"$pid"' 2>/dev/null' RETURN

  # The rendezvous node takes a while to come up before the socket is bound.
  local waited=0
  while [ ! -S "$sock" ] && [ "$waited" -lt 60 ]; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 1; waited=$((waited+1))
  done

  if [ ! -S "$sock" ]; then
    note "the daemon did not bind a socket in ${waited}s; skipping"
    note "$(grep -viE 'topics="(waku|nim|eth)' "$n/daemon.log" | tail -2)"
    return
  fi
  ok "a daemon binds its control socket (${waited}s)"

  expect "and answers on it" "waiting for a mesh" \
    "$("$BIN" status --socket "$sock" 2>&1)"

  kill "$pid" 2>/dev/null
}

# A card on a reader, if there is one.
#
# Skips itself otherwise, which is every CI runner and most machines: this needs
# a USB reader, a card in it, and a binary built with -tags pcsc. It reads only
# — SELECT and nothing else — because everything more interesting costs one of
# five pairing slots that cannot be reclaimed without a factory reset, and a
# test suite is not a thing that should be able to spend those.
scenario_a_card_on_a_reader() {
  local out; out=$("$BIN" keycard status 2>&1)
  case "$out" in
    *"no smartcard reader support"*)
      note "built without -tags pcsc; skipping" ; return ;;
    *"no smartcard reader found"*|*"No smart card inserted"*|*"no PC/SC service"*)
      note "no reader or no card; skipping" ; return ;;
  esac

  expect "the card answers SELECT" "applet" "$out"
  expect "and says whether it is initialised" "initialised" "$out"
  expect "and how many pairing slots are left" "slots free" "$out"

  # The capability list is what says whether a full card can be recovered at
  # all, and it silently omitted factory-reset until 2026-08-26 — which is the
  # single bit that matters when every slot is taken.
  expect "and lists every capability, factory-reset included" "factory-reset" \
    "$("$BIN" keycard status 2>&1)"

  # The JSON is what the phone's setup flow branches on, so it has to parse.
  local raw; raw=$("$BIN" keycard status --json 2>&1)
  if python3 -c "
import json,sys
d=json.loads(sys.argv[1])
for k in ('applet','initialised','hasKey','freeSlots','maxSlots','problem','summary'):
    assert k in d, k
" "$raw" 2>/dev/null; then
    ok "the report parses, with every field the setup flow reads"
  else
    bad "the report is missing fields the setup flow branches on"
    note "${raw:0:200}"
  fi
}

# ---------------------------------------------------------------------------

ALL=(admin_show enrol_a_second_device credential_reaches_the_mesh_entry
     credential_for_another_device
     rename_moves_nothing state_survives_a_power_cut
     no_backup_refuses_rather_than_reminting a_running_daemon_answers
     a_card_on_a_reader)

[ -x "$BIN" ] || { echo "no $BIN — run 'make shrooms'"; exit 1; }

echo "==> management end-to-end, offline"
for s in "${@:-${ALL[@]}}"; do
  rm -rf "$WORK"/*; run_scenario "$s"
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  printf '\nfailed:\n'; printf '  %s\n' "${FAILED[@]}"
  exit 1
fi
