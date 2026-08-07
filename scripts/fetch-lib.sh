#!/usr/bin/env bash
# Fetch a prebuilt liblogosdelivery from this repo's releases.
#
# liblogosdelivery has no canonical distribution, and building it from source is
# blocked upstream, so the only other source is a Logos Basecamp install. That
# makes a fresh machine — CI, a VPS, a new laptop — unable to build at all.
# This downloads a repackaged copy instead.
#
#   make deps-release
set -euo pipefail

cd "$(dirname "$0")/.."

REPO=${REPO:-vpavlin/logos-vpn}
TAG=${DEPS_TAG:-deps-v1}
ASSET=liblogosdelivery-linux-amd64.tar.gz
DEST=${LD_DIR:-docker/build/lib}

if [ -f "$DEST/liblogosdelivery.so" ] && [ -f "$DEST/liblogosdelivery.h" ] && [ "${FORCE:-0}" != "1" ]; then
    echo "==> $DEST already populated (FORCE=1 to refetch)"
    exit 0
fi

echo "==> fetching $ASSET from $REPO@$TAG"
mkdir -p "$DEST"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# curl rather than `gh`, so this works unauthenticated on a bare VPS. Release
# assets on a public repo need no token.
url="https://github.com/$REPO/releases/download/$TAG/$ASSET"
if ! curl -fsSL "$url" -o "$tmp/$ASSET"; then
    echo "download failed: $url"
    echo "if the release moved, set DEPS_TAG to the right tag"
    exit 1
fi

tar xzf "$tmp/$ASSET" -C "$tmp"
cp "$tmp"/lib/* "$DEST/"

[ -f "$DEST/liblogosdelivery.so" ] || { echo "bundle did not contain liblogosdelivery.so"; exit 1; }
[ -f "$DEST/liblogosdelivery.h" ]  || { echo "bundle did not contain liblogosdelivery.h"; exit 1; }

echo "==> $(ls "$DEST" | wc -l) files in $DEST"
echo "    glibc required: $(objdump -T "$DEST/liblogosdelivery.so" 2>/dev/null | grep -oE 'GLIBC_[0-9.]+' | sort -Vu | tail -1)"
echo "    now run: make logos-vpn"
