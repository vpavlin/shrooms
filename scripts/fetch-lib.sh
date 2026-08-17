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

REPO=${REPO:-vpavlin/shrooms}
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

# Verify before unpacking. This is native code that ends up linked into every
# binary and running as root, so "we downloaded it over HTTPS" is not enough:
# TLS says who served the bytes, not what they are. See deps/CHECKSUMS.
want=$(awk -v k="$TAG/$ASSET" '$2 == k {print $1}' deps/CHECKSUMS)
if [ -z "$want" ]; then
    echo "no pinned checksum for $TAG/$ASSET in deps/CHECKSUMS"
    echo "if you are moving to a new build, add its sha256 there in the same commit:"
    echo "  sha256sum <the downloaded tarball>"
    exit 1
fi
got=$(sha256sum "$tmp/$ASSET" | cut -d' ' -f1)
if [ "$got" != "$want" ]; then
    echo "checksum mismatch for $TAG/$ASSET"
    echo "  expected $want"
    echo "  got      $got"
    echo "refusing to unpack. Either the release was rebuilt — in which case"
    echo "update deps/CHECKSUMS deliberately — or this is not the file we pinned."
    exit 1
fi
echo "==> checksum ok"

tar xzf "$tmp/$ASSET" -C "$tmp"
# The bundled files are read-only (they come from a Basecamp install), so a
# plain cp over an existing copy fails with EACCES.
cp -f --no-preserve=mode "$tmp"/lib/* "$DEST/" 2>/dev/null || {
    chmod -R u+w "$DEST" 2>/dev/null || true
    cp -f --no-preserve=mode "$tmp"/lib/* "$DEST/"
}

[ -f "$DEST/liblogosdelivery.so" ] || { echo "bundle did not contain liblogosdelivery.so"; exit 1; }
[ -f "$DEST/liblogosdelivery.h" ]  || { echo "bundle did not contain liblogosdelivery.h"; exit 1; }

echo "==> $(ls "$DEST" | wc -l) files in $DEST"
echo "    glibc required: $(objdump -T "$DEST/liblogosdelivery.so" 2>/dev/null | grep -oE 'GLIBC_[0-9.]+' | sort -Vu | tail -1)"
echo "    now run: make shrooms"
