#!/usr/bin/env bash
# Produce a portable shrooms distribution.
#
#   dist/
#     bin/shrooms        built against an older glibc, rpath $ORIGIN/../lib
#     bin/shrooms-agent  the agent (docs/agents.md), pure Go
#     lib/*.so           liblogosdelivery and friends
#     shrooms.service    systemd unit
#     install.sh, install-agent.sh, uninstall.sh
#
# Everything is built in containers, so the result does not depend on the
# developer machine's toolchain or glibc.
#
#   ./scripts/build-portable.sh              # build the library from source
#   LIB_FROM=basecamp ./scripts/build-portable.sh   # reuse Basecamp's prebuilt
#   LIB_FROM=release ./scripts/build-portable.sh    # the deps-v1 release asset
#   ARCH=arm64 ./scripts/build-portable.sh          # for arm64, into dist-arm64/
#
# Another architecture is cross-compiled (docker/build-vpn.Dockerfile), with
# its library from the release (LIB_FROM defaults to release then: building
# the library from source is for this machine's architecture only).
set -euo pipefail

cd "$(dirname "$0")/.."

case "$(uname -m)" in x86_64|amd64) HOST_ARCH=amd64 ;; aarch64|arm64) HOST_ARCH=arm64 ;; *) HOST_ARCH=$(uname -m) ;; esac
ARCH=${ARCH:-$HOST_ARCH}
case "$ARCH" in amd64|arm64) ;; *) echo "ARCH must be amd64 or arm64"; exit 1 ;; esac
if [ "$ARCH" = "$HOST_ARCH" ]; then LIB_FROM=${LIB_FROM:-source}; else LIB_FROM=${LIB_FROM:-release}; fi
LD_REF=${LD_REF:-master}
# Release tags only; deps-v1 is a dependency drop, not a version. See Makefile.
VERSION=${VERSION:-$(git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)}
BASECAMP_LIB=${BASECAMP_LIB:-$HOME/.local/share/Logos/LogosBasecamp/modules/delivery_module}

# Per architecture: the library staged for one must never be linked into the
# other, and both packages can sit side by side.
if [ "$ARCH" = "$HOST_ARCH" ]; then STAGE=docker/build; DIST=${DIST:-dist}
else STAGE=docker/build/$ARCH; DIST=${DIST:-dist-$ARCH}; fi

echo "==> version $VERSION, $ARCH, library from '$LIB_FROM'"
rm -rf "$DIST"
mkdir -p "$STAGE/lib"

case "$LIB_FROM" in
release)
    ARCH=$ARCH LD_DIR="$STAGE/lib" ./scripts/fetch-lib.sh
    ;;
source)
    [ "$ARCH" = "$HOST_ARCH" ] || { echo "the library builds from source for this machine's architecture only; LIB_FROM=release"; exit 1; }
    # Reuse a previous container build if present — it takes tens of minutes.
    if [ -f "$STAGE/lib/liblogosdelivery.so" ] && [ -f "$STAGE/lib/liblogosdelivery.h" ]; then
        echo "==> reusing staged library ($(cat "$STAGE/lib/.ld-rev" 2>/dev/null || echo 'unknown rev'))"
    else
        echo "==> building liblogosdelivery from source (this takes a while)"
        docker build -f docker/build-lib.Dockerfile \
            --build-arg "LD_REF=$LD_REF" \
            --target lib -o "$STAGE/lib" .
    fi
    ;;
basecamp)
    echo "==> copying the library Basecamp installed"
    [ -f "$BASECAMP_LIB/liblogosdelivery.so" ] \
        || { echo "no liblogosdelivery.so in $BASECAMP_LIB"; exit 1; }
    # Every library, not just the obvious two: liblogosdelivery dlopens libpq
    # at runtime and the failure is fatal.
    cp "$BASECAMP_LIB"/*.so "$BASECAMP_LIB"/*.so.* "$STAGE/lib/" 2>/dev/null || true
    # The matching header is not shipped with the module; take it from the
    # delivery-module checkout that vendors it.
    HDR=${HDR:-$HOME/devel/github.com/logos-co/logos-workspace/repos/logos-modules/logos-delivery-module/vendor/logos-delivery/liblogosdelivery/liblogosdelivery.h}
    [ -f "$HDR" ] || { echo "no liblogosdelivery.h — set HDR"; exit 1; }
    cp "$HDR" "$STAGE/lib/"
    ;;
*)
    echo "LIB_FROM must be 'source', 'basecamp' or 'release'"; exit 1 ;;
esac

echo "==> staged $(ls "$STAGE/lib" | wc -l) files"

echo "==> building shrooms against an older glibc"
# The build context is the staged directory's parent, so lib/ is where the
# Dockerfile expects it.
BUILD_CTX=$(mktemp -d); trap 'rm -rf "$BUILD_CTX"' EXIT
# docker/run holds root-owned state from the container tests, which is not
# part of a build and cannot be read here anyway.
tar -c --exclude=.git --exclude=dist --exclude='dist-*' --exclude=bin --exclude=docker/run --exclude=docker/build . \
    | tar -x -C "$BUILD_CTX"
cp -r "$STAGE/lib" "$BUILD_CTX/lib"

docker buildx build --platform "linux/$ARCH" -f docker/build-vpn.Dockerfile \
    --build-arg "VERSION=$VERSION" \
    --target dist -o "$DIST" "$BUILD_CTX"

cp packaging/shrooms.service "$DIST/"
cp packaging/shrooms.bash "$DIST/"
cp packaging/install-dist.sh "$DIST/install.sh"
chmod +x "$DIST/install.sh"
# The agent's installer, which takes bin/shrooms-agent from beside itself.
cp scripts/install-agent.sh "$DIST/install-agent.sh"
chmod +x "$DIST/install-agent.sh"

# The uninstaller ships alongside, because the machine this lands on is the one
# with no checkout to fetch it from later.
cp scripts/uninstall.sh "$DIST/uninstall.sh"
chmod +x "$DIST/uninstall.sh"

echo
echo "==> $DIST"
find "$DIST" -maxdepth 2 -type f -printf '  %P\n' | sort
echo
echo "built for: $(file -b "$DIST/bin/shrooms" | cut -d, -f1-2)"

# arm64: run it on the oldest core it must run on. The library is native code
# built elsewhere; one built with -march=native on a CPU with LSE atomics died
# with SIGILL on a Chromebook's Cortex-A53/A73 (ARMv8.0, 2026-10-06), while the
# Pi 5 and plain emulation, both with LSE, ran it fine. QEMU_CPU makes the
# emulator an A53. Passing needs the daemon to have started the delivery node
# and gone on waiting, not merely not to have crashed.
if [ "$ARCH" = arm64 ] && [ "${SKIP_A53:-0}" != 1 ]; then
    echo "==> running it on an emulated Cortex-A53 (ARMv8.0, no LSE)"
    # Into a file, not a variable piped to grep -q: with pipefail, grep -q
    # stopping at its match kills printf with SIGPIPE and a match reads as none.
    a53=$(mktemp)
    docker run --rm --platform linux/arm64 -e QEMU_CPU=cortex-a53 -v "$PWD/$DIST:/d:ro" \
        debian:trixie timeout 150 /d/bin/shrooms daemon > "$a53" 2>&1 || true
    if grep -q "SIGILL" "$a53"; then
        echo "FAIL: illegal instruction on an ARMv8.0 core — the library was built for a newer CPU" >&2
        echo "      (rebuild it with -d:disableMarchNative, docker/build-lib.Dockerfile)" >&2
        rm -f "$a53"; exit 1
    fi
    grep -q "waiting to be told which mesh" "$a53" || {
        echo "FAIL: the daemon did not get as far as waiting for a mesh on the A53:" >&2
        grep -v "^TRC\|^DBG" "$a53" | tail -8 >&2
        rm -f "$a53"; exit 1
    }
    rm -f "$a53"
    echo "    ok: the delivery node starts on ARMv8.0"
fi
echo "glibc requirement:"
objdump -T "$DIST/bin/shrooms" 2>/dev/null \
    | grep -oE 'GLIBC_[0-9.]+' | sort -Vu | tail -1 | sed 's/^/  /'
echo
echo "Install on a target — no container runtime, no Go toolchain:"
echo "  scp -r $DIST/ host:shrooms-dist"
echo "  ssh host 'sudo ./shrooms-dist/install.sh'"
echo "and agents on the mesh, once it has joined one:"
echo "  ssh host 'sudo ./shrooms-dist/install-agent.sh'"
echo
echo "Remove it again:"
echo "  ssh host 'sudo ./shrooms-dist/uninstall.sh --purge'"
