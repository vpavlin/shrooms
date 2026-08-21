#!/usr/bin/env bash
# Build android/logosvpn.aar from the Go core.
#
# Runs in a container: gomobile needs a JDK and Go >= 1.25, and hard-coding
# those onto every developer's machine is worse than one docker run. The host
# Android SDK/NDK is mounted read-only rather than re-downloaded.
#
#   make aar
set -euo pipefail
cd "$(dirname "$0")/.."

SDK=${ANDROID_SDK:-$HOME/Android/Sdk}
NDK_VER=${NDK_VER:-android-ndk-r27c}
API=${ANDROID_API:-24}
OUT=android/logosvpn.aar

[ -d "$SDK/ndk/$NDK_VER" ] || { echo "no NDK at $SDK/ndk/$NDK_VER — set ANDROID_SDK/NDK_VER"; exit 1; }
[ -f android/libs/arm64-v8a/liblogosdelivery.so ] || { echo "run 'make android-deps' first"; exit 1; }

# gomobile must be the same version as the x/mobile the module already pins.
#
# It was installed at @latest, which is a supply-chain hole that closed itself
# on 2026-08-21: x/mobile published a build requiring Go 1.26 while this image
# has 1.25, and the APK stopped building for reasons nothing in this repository
# had changed. Reading the version out of go.mod means the tool and the library
# cannot drift apart, and that an old checkout builds the way it did.
MOBILE_VER=$(awk '/golang.org\/x\/mobile v/ {print $2; exit}' mobile/go.mod)
[ -n "$MOBILE_VER" ] || { echo "no golang.org/x/mobile version in mobile/go.mod"; exit 1; }

echo "==> binding (container: JDK + Go 1.25 + gomobile $MOBILE_VER)"
docker run --rm \
    -v "$PWD:/src" -v "$SDK:/sdk:ro" -w /src/mobile \
    -e ANDROID_HOME=/sdk -e ANDROID_NDK_HOME="/sdk/ndk/$NDK_VER" \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    golang:1.25 bash -euc '
        apt-get update -qq && apt-get install -y -qq default-jdk-headless >/dev/null 2>&1
        go install golang.org/x/mobile/cmd/gomobile@'"$MOBILE_VER"' golang.org/x/mobile/cmd/gobind@'"$MOBILE_VER"'

        export PATH=$PATH:/go/bin:/root/go/bin
        export CGO_CFLAGS="-I/src/android/libs"
        export CGO_LDFLAGS="-L/src/android/libs/arm64-v8a -llogosdelivery"
        gomobile bind -target=android/arm64 -androidapi '"$API"' -o /src/'"$OUT"' .
        # The container runs as root; hand the artefacts back or the host cannot
        # even append to the file it just asked for.
        chown "$HOST_UID:$HOST_GID" /src/'"$OUT"' 2>/dev/null || true
        chown "$HOST_UID:$HOST_GID" /src/mobile/go.mod /src/mobile/go.sum 2>/dev/null || true
    '

# gomobile packages only what it built, so the prebuilt node and its
# dependencies have to be added. Without them the .aar loads and then dies at
# the first call into liblogosdelivery, which reads as a Go bug.
echo "==> adding the native libraries"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/jni/arm64-v8a"
cp android/libs/arm64-v8a/*.so "$tmp/jni/arm64-v8a/"
( cd "$tmp" && zip -qr "$OLDPWD/$OUT" jni )

echo
echo "==> $OUT ($(du -h "$OUT" | cut -f1))"
unzip -l "$OUT" | grep -E '\.so|classes\.jar' | awk '{printf "    %-40s %8.1f MB\n", $4, $1/1048576}'

# A missing dependency here is a runtime crash on a phone, a long way from here.
for lib in libgojni.so liblogosdelivery.so librln.so libc++_shared.so; do
    unzip -l "$OUT" | grep -q "$lib" || { echo "MISSING $lib"; exit 1; }
done
echo "    all four native libraries present"
