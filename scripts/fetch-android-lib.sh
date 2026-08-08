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

if [ -f "$DEST/arm64-v8a/liblogosdelivery.so" ] && [ "${FORCE:-0}" != "1" ]; then
    echo "==> $DEST already populated (FORCE=1 to refetch)"
    exit 0
fi

echo "==> fetching the Android liblogosdelivery from $SRC"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
git clone -q --depth 1 --filter=blob:none --sparse "$SRC" "$tmp/src"
git -C "$tmp/src" sparse-checkout set "$SUBDIR" >/dev/null

mkdir -p "$DEST/arm64-v8a"
# All three: liblogosdelivery has DT_NEEDED on librln and libc++_shared, so it
# will not load without them.
for f in liblogosdelivery.so librln.so libc++_shared.so; do
    cp "$tmp/src/$SUBDIR/arm64-v8a/$f" "$DEST/arm64-v8a/$f"
done
cp "$tmp/src/$SUBDIR/jni/liblogosdelivery.h" "$DEST/"

echo "==> $(du -sh "$DEST" | cut -f1) in $DEST"
file "$DEST/arm64-v8a/liblogosdelivery.so" | grep -q aarch64 || { echo "not an arm64 library"; exit 1; }
echo "    now run: make android-core"
