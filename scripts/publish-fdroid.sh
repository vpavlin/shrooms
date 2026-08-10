#!/usr/bin/env bash
# Sign the release APK and publish it to the LAN F-Droid repo.
#
#   make fdroid                       # next versionCode, published
#   FDROID_HOST=user@host make fdroid
#
# The signing key lives on the F-Droid host and never leaves it, so the APK is
# built here unsigned and signed there. The keystore password is read from that
# host's fdroid config.yml inside the ssh session and is never printed, passed
# on a command line, or written to this repository.
#
# Two rules `fdroid update` enforces, both of which fail quietly:
#   - versionCode must be higher than the last published one, or the APK is
#     ignored. This script reads the current highest and increments it.
#   - the APK must be signed with the repo key; a debug-signed one is skipped.
set -euo pipefail
cd "$(dirname "$0")/.."

HOST=${FDROID_HOST:-192.168.0.152}
FDROID_DIR=${FDROID_DIR:-'~/fdroid'}
FDROID_BIN=${FDROID_BIN:-'~/fdroid-venv/bin/fdroid'}
APP_ID=${APP_ID:-xyz.vpavlin.shrooms}
VERSION_NAME=${VERSION_NAME:-0.1-prototype}

echo "==> checking $HOST"
ssh "$HOST" "test -f $FDROID_DIR/config.yml" || { echo "no fdroid config on $HOST"; exit 1; }

# F-Droid orders by versionCode. Publishing one that is not higher than the last
# is a silent no-op, so take the highest already there and add one.
#
# Remote steps are fed over stdin rather than quoted inline: nesting quotes
# through ssh mangled $BT into an empty string, which produced "/zipalign: No
# such file" — a confusing way to say "your quoting is wrong".
echo "==> working out the next versionCode"
LAST=$(ssh "$HOST" "APP_ID='$APP_ID' FDROID_DIR='$FDROID_DIR' bash -s" <<'REMOTE'
set -eu
BT=$(ls -d "$HOME"/Android/Sdk/build-tools/* | sort -V | tail -1)
eval FD="$FDROID_DIR"
highest=0
for apk in "$FD"/repo/*.apk; do
    [ -e "$apk" ] || continue
    info=$("$BT/aapt2" dump badging "$apk" 2>/dev/null | head -1) || continue
    case "$info" in
        *"name='$APP_ID'"*)
            vc=$(printf '%s' "$info" | grep -oE "versionCode='[0-9]+'" | grep -oE '[0-9]+')
            [ -n "$vc" ] && [ "$vc" -gt "$highest" ] && highest=$vc ;;
    esac
done
printf '%s' "$highest"
REMOTE
)
VERSION_CODE=$((LAST + 1))
echo "    last published: $LAST -> building $VERSION_CODE"

# Rebuild the binding first. Calling build-apk.sh directly skips the `apk: aar`
# dependency in the Makefile, so a Go change would be silently packaged against
# a stale .aar — which presents as "Unresolved reference" on a function that
# plainly exists.
echo "==> rebuilding the Go binding"
./scripts/build-aar.sh > /dev/null

echo "==> building an unsigned release APK"
RELEASE=1 VERSION_CODE="$VERSION_CODE" VERSION_NAME="$VERSION_NAME" ./scripts/build-apk.sh

APK=android/shrooms-unsigned.apk
[ -f "$APK" ] || { echo "no unsigned APK"; exit 1; }

echo "==> shipping to $HOST"
# Into a private directory, not a predictable /tmp path. This is not about
# disclosure — an unsigned APK is not secret — but substitution: a local user
# on that host could replace the file between the copy and apksigner, and we
# would sign their code with the repo key.
STAGE=$(ssh "$HOST" 'd=$(mktemp -d); chmod 700 "$d"; printf %s "$d"')
[ -n "$STAGE" ] || { echo "could not create a staging directory on $HOST"; exit 1; }
scp -q "$APK" "$HOST:$STAGE/unsigned.apk"

echo "==> signing with the repo key (the password never leaves that host)"
ssh "$HOST" "FDROID_DIR='$FDROID_DIR' VERSION_CODE='$VERSION_CODE' STAGE='$STAGE' bash -s" <<'REMOTE'
set -eu
BT=$(ls -d "$HOME"/Android/Sdk/build-tools/* | sort -V | tail -1)
eval FD="$FDROID_DIR"
cfg="$FD/config.yml"

# Read from fdroid's own config so there is one source of truth. Kept in shell
# variables and passed to apksigner through the environment, never on a command
# line where it would appear in ps output.
strip() { sed -e 's/^[^:]*:[[:space:]]*//' -e 's/^["'"'"']//' -e 's/["'"'"']$//'; }
ks=$(grep '^keystore:' "$cfg" | strip)
alias=$(grep '^repo_keyalias:' "$cfg" | strip)
KSPASS=$(grep '^keystorepass:' "$cfg" | strip); export KSPASS
KEYPASS=$(grep '^keypass:' "$cfg" | strip); export KEYPASS
case "$ks" in /*) ;; *) ks="$FD/$ks" ;; esac
[ -f "$ks" ] || { echo "keystore not found: $ks"; exit 1; }

# Align before signing; zipalign afterwards would invalidate the signature.
"$BT/zipalign" -p -f 4 "$STAGE/unsigned.apk" "$STAGE/aligned.apk"
"$BT/apksigner" sign \
    --ks "$ks" --ks-key-alias "$alias" \
    --ks-pass env:KSPASS --key-pass env:KEYPASS \
    --out "/tmp/shrooms-$VERSION_CODE.apk" "$STAGE/aligned.apk"
"$BT/apksigner" verify --print-certs "/tmp/shrooms-$VERSION_CODE.apk" | head -2
rm -rf "$STAGE"
REMOTE

echo "==> metadata"
# F-Droid cannot extract an adaptive (XML) icon from an APK, which is why the
# first publish logged "Cannot fetch icon". The fastlane layout is the
# documented way to supply one directly.
# A relative destination is home-relative; scp does not expand $HOME remotely.
ssh "$HOST" "mkdir -p fdroid/metadata/$APP_ID/en-US"
scp -q android/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png \
    "$HOST:fdroid/metadata/$APP_ID/en-US/icon.png"

ssh "$HOST" "cat > fdroid/metadata/$APP_ID.yml" <<'META'
Categories:
  - Internet
  - Security
Name: Shrooms
Summary: The mycelial mesh VPN — your devices, no coordinator
Description: |
  An encrypted overlay network between your own machines, with no coordination
  server to trust or run. Nodes find each other through the network's own
  gossip and move data directly between one another.

  WireGuard carries the traffic. Peers find each other over Logos Messaging,
  which is used only for rendezvous — once a tunnel exists it keeps working
  whether or not the messaging network is reachable.

  Addresses are derived from device keys, so there is nothing to allocate and
  no address collisions. Peers that cannot reach each other directly fall back
  to a relay, discovered from its own announcement rather than configured.

  Prototype. Requires a network key from another device on your mesh.
License: MIT
AuthorName: vpavlin
SourceCode: https://github.com/vpavlin/shrooms
IssueTracker: https://github.com/vpavlin/shrooms/issues
META

echo "==> publishing"
ssh "$HOST" "FDROID_DIR='$FDROID_DIR' FDROID_BIN='$FDROID_BIN' VERSION_CODE='$VERSION_CODE' KEEP='${KEEP:-2}' bash -s" <<'REMOTE'
set -eu
eval FD="$FDROID_DIR"
eval BIN="$FDROID_BIN"
mv "/tmp/shrooms-$VERSION_CODE.apk" "$FD/repo/"

# Keep only the most recent builds. F-Droid publishes whatever APKs are
# present, and this repo already carries every build of another app ever made —
# 2.4GB of them. At ~48MB each this would get there quickly.
ls -1 "$FD"/repo/shrooms-*.apk 2>/dev/null \
    | sed 's/.*shrooms-\([0-9]*\)\.apk/\1 &/' | sort -rn | tail -n +$((KEEP + 1)) \
    | cut -d' ' -f2- | while read -r old; do
        echo "    pruning $(basename "$old")"
        rm -f "$old"
    done

cd "$FD" && "$BIN" update --pretty 2>&1 | tail -8
REMOTE

echo
echo "==> published $APP_ID versionCode $VERSION_CODE ($VERSION_NAME)"
echo "    http://192.168.0.152:8090/fdroid/repo"
echo "    https://192.168.0.152:8444/fdroid/repo"
echo "    refresh the repo in the F-Droid app to see it"
