#!/usr/bin/env bash
# Fetch the arm64 Android build of liblogosdelivery.
#
# There is no public distribution of it, and no x86_64 build at all — which is
# why an emulator has no node and testing needs a real phone. The only source is
# another project that ships it; qaku-logos does, and its header is a strict
# superset of the one we build against on Linux.
#
#   make android-deps
set -euo pipefail
cd "$(dirname "$0")/.."

DEST=${ANDROID_LIB_DIR:-android/libs}
SRC=${ANDROID_LIB_SRC:-https://github.com/vpavlin/qaku-logos.git}
SUBDIR=mobile/native/logosdelivery
# A commit, not a branch. `--depth 1` of a default branch fetches whatever HEAD
# is that day, so the native code inside the APK people install could change
# without anything here changing. The hashes in deps/CHECKSUMS are what make it
# safe — a ref can be moved, a hash cannot — and this makes it reproducible.
REF=${ANDROID_LIB_REF:-b5f5e33e81a51da713853389602413fc0c42e7f4}

if [ -f "$DEST/arm64-v8a/liblogosdelivery.so" ] && [ "${FORCE:-0}" != "1" ]; then
    echo "==> $DEST already populated (FORCE=1 to refetch)"
    exit 0
fi

echo "==> fetching the Android liblogosdelivery from $SRC"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
# Fetch the pinned commit specifically, rather than cloning a branch and
# hoping. init+fetch is the only way to ask for one commit by name.
git init -q "$tmp/src"
git -C "$tmp/src" remote add origin "$SRC"
git -C "$tmp/src" config core.sparseCheckout true
git -C "$tmp/src" sparse-checkout set --no-cone "$SUBDIR" >/dev/null 2>&1 || true
if ! git -C "$tmp/src" fetch -q --depth 1 --filter=blob:none origin "$REF"; then
    echo "could not fetch $REF from $SRC"
    echo "if the library moved, update REF and the hashes in deps/CHECKSUMS together"
    exit 1
fi
git -C "$tmp/src" checkout -q FETCH_HEAD

mkdir -p "$DEST/arm64-v8a"
# All three: liblogosdelivery has DT_NEEDED on librln and libc++_shared, so it
# will not load without them.
for f in liblogosdelivery.so librln.so libc++_shared.so; do
    cp "$tmp/src/$SUBDIR/arm64-v8a/$f" "$DEST/arm64-v8a/$f"
done
cp "$tmp/src/$SUBDIR/jni/liblogosdelivery.h" "$DEST/"

# Verify what we just copied. This is the native code that goes inside the VPN
# app — the process that sees every packet and holds the network key — so it is
# checked against hashes committed here rather than trusted for having come from
# a repository we recognise.
fail=0
while read -r want name; do
    case "$want" in ''|\#*) continue;; esac
    case "$name" in android/*) ;; *) continue;; esac
    f="$DEST/${name#android/}"
    [ -f "$f" ] || { echo "missing after fetch: $f"; fail=1; continue; }
    got=$(sha256sum "$f" | cut -d' ' -f1)
    if [ "$got" != "$want" ]; then
        echo "checksum mismatch: ${name#android/}"
        echo "  expected $want"
        echo "  got      $got"
        fail=1
    fi
done < deps/CHECKSUMS
if [ "$fail" != 0 ]; then
    echo "refusing to ship unverified native code into the APK."
    echo "If the library was deliberately updated, change REF and deps/CHECKSUMS in one commit."
    rm -rf "$DEST"
    exit 1
fi
echo "==> checksums ok"

echo "==> $(du -sh "$DEST" | cut -f1) in $DEST"
file "$DEST/arm64-v8a/liblogosdelivery.so" | grep -q aarch64 || { echo "not an arm64 library"; exit 1; }
echo "    now run: make android-core"
