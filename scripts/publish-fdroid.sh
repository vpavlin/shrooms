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
# Where the SIGNING key comes from, which is not always where the app is
# published. An APK's signature is its identity forever — Android refuses an
# update signed by a different key, and the only way past that is uninstalling,
# which for this app means losing the device's mesh identity and its
# credentials. The repo's index key is a separate thing and may differ freely.
#
# So publishing into a second repo means: sign with the key this app has always
# had, place the file in the other repo, and let `fdroid update` sign that
# repo's index with its own key.
SIGN_DIR=${SIGN_DIR:-$FDROID_DIR}
# Repos to consider when picking the next versionCode. The target alone is not
# enough: publishing into a repo that has never held this app would start again
# from 1, and a device that already has a higher code installed treats that as a
# downgrade and never offers it.
CODE_DIRS=${CODE_DIRS:-"$FDROID_DIR $SIGN_DIR"}
FDROID_BIN=${FDROID_BIN:-'~/fdroid-venv/bin/fdroid'}
APP_ID=${APP_ID:-xyz.vpavlin.shrooms}
# Derived, the way the daemon and the portable build already derive theirs.
#
# It was the literal string "1.2-invites" for every publish, so three releases
# in a row — including a day of fixes to path selection and the IPv4 translator
# — all read the same in F-Droid, and neither a user nor the person who built
# them could tell which was installed. A name that has to be remembered is a
# name that stops being true; --dirty is kept so a publish from an uncommitted
# tree says so out loud.
VERSION_NAME=${VERSION_NAME:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}

echo "==> checking $HOST"
ssh "$HOST" "test -f $FDROID_DIR/config.yml" || { echo "no fdroid config on $HOST"; exit 1; }

# F-Droid orders by versionCode. Publishing one that is not higher than the last
# is a silent no-op, so take the highest already there and add one.
#
# Remote steps are fed over stdin rather than quoted inline: nesting quotes
# through ssh mangled $BT into an empty string, which produced "/zipalign: No
# such file" — a confusing way to say "your quoting is wrong".
echo "==> working out the next versionCode"
LAST=$(ssh "$HOST" "APP_ID='$APP_ID' CODE_DIRS='$CODE_DIRS' bash -s" <<'REMOTE'
set -eu
BT=$(ls -d "$HOME"/Android/Sdk/build-tools/* | sort -V | tail -1)
highest=0
for d in $CODE_DIRS; do
eval FD="$d"
for apk in "$FD"/repo/*.apk; do
    [ -e "$apk" ] || continue
    info=$("$BT/aapt2" dump badging "$apk" 2>/dev/null | head -1) || continue
    case "$info" in
        *"name='$APP_ID'"*)
            vc=$(printf '%s' "$info" | grep -oE "versionCode='[0-9]+'" | grep -oE '[0-9]+')
            [ -n "$vc" ] && [ "$vc" -gt "$highest" ] && highest=$vc ;;
    esac
done
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
ssh "$HOST" "SIGN_DIR='$SIGN_DIR' VERSION_CODE='$VERSION_CODE' STAGE='$STAGE' bash -s" <<'REMOTE'
set -eu
BT=$(ls -d "$HOME"/Android/Sdk/build-tools/* | sort -V | tail -1)
# The signing repo, which may not be the publishing one. See SIGN_DIR.
eval FD="$SIGN_DIR"
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
# Into the repo being published to. This said `fdroid/...` outright, so a
# publish aimed anywhere else wrote metadata to the wrong repo and left the
# APK in the right one with none — and `fdroid update` drops an APK with no
# metadata without saying so.
MD=$(ssh "$HOST" "eval echo $FDROID_DIR")
ssh "$HOST" "mkdir -p '$MD/metadata/$APP_ID/en-US'"
scp -q android/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png \
    "$HOST:$MD/metadata/$APP_ID/en-US/icon.png"

ssh "$HOST" "cat > '$MD/metadata/$APP_ID.yml'" <<'META'
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

  To join, scan the code that "shrooms invite" prints on a machine already on
  the mesh. The invite is good for one device and fifteen minutes, and brings
  back a membership credential signed for this phone's own keys. A network key
  still works where there is no invite to scan.

  Prototype.
License: Apache-2.0 OR MIT
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
# Derived from the directory rather than hardcoded: the served name is a
# symlink under ~/vpavlin-home and does not always match the directory, so a
# fixed URL here would send somebody to a repo that does not hold this build.
SERVED=$(ssh "$HOST" "for l in \$HOME/vpavlin-home/*; do [ -L \"\$l\" ] && [ \"\$(readlink -f \"\$l\")\" = \"\$(eval echo $FDROID_DIR)\" ] && basename \"\$l\"; done" | head -1)
SERVED=${SERVED:-fdroid}
echo "    https://$HOST:8444/$SERVED/repo"
echo "    signed with the key from $SIGN_DIR"
echo "    refresh the repo in the F-Droid app to see it"
