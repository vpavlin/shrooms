#!/usr/bin/env bash
# Produce a portable shrooms distribution.
#
#   dist/
#     bin/shrooms      built against an older glibc, rpath $ORIGIN/../lib
#     lib/*.so           liblogosdelivery and friends
#     shrooms.service  systemd unit
#
# Everything is built in containers, so the result does not depend on the
# developer machine's toolchain or glibc.
#
#   ./scripts/build-portable.sh              # build the library from source
#   LIB_FROM=basecamp ./scripts/build-portable.sh   # reuse Basecamp's prebuilt
set -euo pipefail

cd "$(dirname "$0")/.."

LIB_FROM=${LIB_FROM:-source}
LD_REF=${LD_REF:-master}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
BASECAMP_LIB=${BASECAMP_LIB:-$HOME/.local/share/Logos/LogosBasecamp/modules/delivery_module}

STAGE=docker/build
DIST=dist

echo "==> version $VERSION, library from '$LIB_FROM'"
rm -rf "$DIST"
mkdir -p "$STAGE/lib"

case "$LIB_FROM" in
source)
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
    echo "LIB_FROM must be 'source' or 'basecamp'"; exit 1 ;;
esac

echo "==> staged $(ls "$STAGE/lib" | wc -l) files"

echo "==> building shrooms against an older glibc"
docker build -f docker/build-vpn.Dockerfile \
    --build-arg "VERSION=$VERSION" \
    --target dist -o "$DIST" .

cp packaging/shrooms.service "$DIST/"

echo
echo "==> $DIST"
find "$DIST" -maxdepth 2 -type f -printf '  %P\n' | sort
echo
echo "glibc requirement:"
objdump -T "$DIST/bin/shrooms" 2>/dev/null \
    | grep -oE 'GLIBC_[0-9.]+' | sort -Vu | tail -1 | sed 's/^/  /'
echo
echo "Install on a target:"
echo "  scp -r $DIST/* host:/opt/shrooms/"
echo "  ssh host 'ln -sf /opt/shrooms/bin/shrooms /usr/bin/shrooms'"
