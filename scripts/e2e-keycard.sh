#!/usr/bin/env bash
# The whole life of a mesh whose authority is a Keycard, against a real card.
#
# NOT in CI, and cannot be: it needs a USB reader, a card in it, a PIN, and a
# binary. Run it on the machine that has those:
#
#     make shrooms
#     SHROOMS_CARD_PIN=nnnnnn ./scripts/e2e-keycard.sh
#
# It pairs AT MOST ONCE, ever. A card has five pairing slots, they are consumed
# permanently, and freeing them needs a device that already holds one — so a
# test that paired per run would eat the card in five runs and then need a
# factory reset. The pairing is kept in a directory of its own and reused, which
# is also what a person does.
#
# Everything else is thrown away per run: the mesh is minted into a temp
# directory and deleted at the end. Minting twice with the same card produces
# the same mesh id, because the id is the hash of the admin key set and that set
# is the card — so runs are repeatable rather than accumulating.
set -uo pipefail

cd "$(dirname "$0")/.."
BIN=$(pwd)/bin/shrooms
# Deliberately outside the throwaway working directory: this is the one thing
# that must survive a run.
PAIRDIR=${SHROOMS_CARD_PAIRDIR:-$HOME/.config/shrooms-e2e-card}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

PASS=0; FAIL=0
ok()   { printf '    \033[32mok\033[0m   %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '    \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
note() { printf '         %s\n' "$1"; }
skip() { printf '\n  skipped: %s\n' "$1"; exit 77; }

[ -x "$BIN" ] || skip "no $BIN — run 'make shrooms'"

probe=$("$BIN" keycard status 2>&1)
case "$probe" in
  *"no PC/SC library"*) skip "no pcsc-lite on this machine" ;;
  *"no smartcard reader found"*)   skip "no reader attached" ;;
  *"No smart card inserted"*)      skip "no card in the reader" ;;
  *"no PC/SC service"*)            skip "pcscd is not reachable" ;;
esac
case "$probe" in
  *"NOT INITIALISED"*|*"never been initialised"*)
    skip "the card is blank — 'shrooms keycard init' first" ;;
  *"HOLDS NO KEY"*|*"holds no key"*)
    skip "the card has no key — 'shrooms keycard init' first" ;;
esac

PIN=${SHROOMS_CARD_PIN:-}
[ -n "$PIN" ] || skip "set SHROOMS_CARD_PIN so this can use the card"

echo "==> a mesh whose authority is a card"
printf '  %s\n' "$(grep -E '^applet|^key |^pairing ' <<<"$probe" | tr '\n' ' ')"

# --- pair, once and only once ---------------------------------------------
if [ -f "$PAIRDIR/keycard-pairing" ]; then
  ok "reusing the pairing in $PAIRDIR"
else
  note "no pairing yet — taking one of five slots, once"
  mkdir -p "$PAIRDIR"
  if printf '\n%s\n' "$PIN" | "$BIN" keycard pair --dir "$PAIRDIR" >"$WORK/pair.log" 2>&1; then
    ok "paired with the card"
  else
    bad "could not pair"; note "$(tail -2 "$WORK/pair.log")"; exit 1
  fi
fi

# --- mint ------------------------------------------------------------------
MESH=$WORK/mesh
mkdir -p "$MESH"
cp "$PAIRDIR/keycard-pairing" "$MESH/" 2>/dev/null

minted=$(printf '%s\n' "$PIN" | "$BIN" admin init --keycard --dir "$MESH" --mesh e2e 2>&1)
if grep -q "Minted the mesh authority from the card" <<<"$minted"; then
  ok "minted a mesh from the card"
else
  bad "could not mint"; note "$(tail -2 <<<"$minted")"; exit 1
fi

MESHID=$(sed -n 's/.*mesh id  *//p' <<<"$minted" | head -1)
AUTH=$(sed -n 's/.*authority  *//p' <<<"$minted" | head -1)
[ -n "$MESHID" ] && ok "it has a mesh id ($MESHID)" || bad "no mesh id reported"

# The property everything in ADR-033 hangs on, and the reason for one key
# rather than two: a single file key would make this false.
shown=$("$BIN" admin show --dir "$MESH" --mesh e2e 2>&1)
if grep -q "every key is a card key" <<<"$shown"; then
  ok "the authority is card-only"
else
  bad "the authority is not card-only, so ADR-033's widening would not apply"
  note "$(head -4 <<<"$shown" | tr '\n' ' ')"
fi
if grep -q "1 trusted" <<<"$shown"; then
  ok "one admin key, not two"
else
  bad "expected exactly one admin key"
fi

# --- issue -----------------------------------------------------------------
"$BIN" prepare --config "$MESH/node.toml" --state "$MESH/state" --name e2enode \
  --port 51930 >/dev/null 2>&1
keys=$("$BIN" keys --state "$MESH/state" 2>&1)
DEV=$(sed -n 's/^device  *//p'  <<<"$keys" | head -1)
WG=$( sed -n 's/^tunnel  *//p'  <<<"$keys" | head -1)
SEAL=$(sed -n 's/^sealing  *//p' <<<"$keys" | head -1)

issued=$(printf '%s\n' "$PIN" | "$BIN" admin issue --dir "$MESH" --mesh e2e \
    --name e2enode --device "$DEV" --wg "$WG" --seal "$SEAL" --write=false 2>&1)
BLOB=$(sed -n 's/.*credential set //p' <<<"$issued" | tr -d ' \n')
if [ -n "$BLOB" ]; then
  # IssueFor verifies what it signed against the authority before returning it,
  # so a credential coming back at all is the proof that a card-signed
  # credential verifies against a card-minted mesh.
  ok "the card signed a credential, and it verifies against the mesh"
else
  bad "no credential issued"; note "$(tail -2 <<<"$issued")"
fi

if [ -n "$BLOB" ]; then
  set_out=$("$BIN" credential set "$BLOB" --config "$MESH/node.toml" \
      --state "$MESH/state" </dev/null 2>&1)
  grep -q "Installed a credential" <<<"$set_out" &&
    ok "the device installs it" || { bad "install failed"; note "$set_out"; }
fi

# --- revoke ----------------------------------------------------------------
revoked=$(printf '%s\n' "$PIN" | "$BIN" admin revoke --dir "$MESH" --mesh e2e \
    --device "$DEV" --publish=false 2>&1)
if grep -qE "^Revoked " <<<"$revoked"; then
  # SignRevocationWith verifies against the signing key before returning, so
  # this also proves the card's revocation is one peers would act on.
  ok "the card signed a revocation, and it verifies"
else
  bad "revocation failed"; note "$(tail -2 <<<"$revoked")"
fi

# A revocation for a device the mesh never knew is still well-formed; what must
# not happen is one that names nobody.
grep -q "${DEV:0:16}" <<<"$revoked" &&
  ok "it names the device it withdraws" || bad "the revocation names something else"

# --- teardown --------------------------------------------------------------
#
# The mesh goes; the pairing stays. Unpairing would cost a slot to get back and
# the card only has five.
rm -rf "$MESH"
[ -f "$PAIRDIR/keycard-pairing" ] &&
  ok "the pairing survives teardown, so the next run spends no slot" ||
  bad "the pairing was destroyed; the next run would take another slot"

after=$("$BIN" keycard status 2>&1 | grep -E "^pairing ")
note "card now: $after"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
