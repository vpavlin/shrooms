#!/usr/bin/env bash
# Hand the built Basecamp packages to the repository host's own publisher.
#
# It does not write an index. The host has one — the logos-publish-artifacts
# skill's publish.sh — and that script regenerates the index by rescanning every
# .lgx in the repository directory, so every module stays listed and updating
# one is "drop the new file, rescan". Writing entries from here meant
# maintaining a second implementation of somebody else's format, which went
# exactly as well as that usually goes.
#
#   ./scripts/publish-lan.sh view.lgx core.lgx
#   LAN_HOST=user@host ./scripts/publish-lan.sh view.lgx
#
# Three traps this avoids, each of which makes a publish look successful while
# shipping nothing:
#
#   Publishing to a repository nothing reads. There is more than one repo
#   directory on that host, and the version of this script that wrote its own
#   index published to the wrong one — every publish "succeeded" and Basecamp
#   kept offering a module from three days earlier.
#
#   More than one file per module. The index lists one entry per .lgx found, so
#   a version-in-the-filename scheme accumulates files and shows the same module
#   several times. The host's publisher installs each as
#   logos-<name>-module.lgx and overwrites it.
#
#   Publishing a -dev build, which Basecamp shows as NOT AVAILABLE. `make
#   basecamp-lgx` builds the portable variant for this reason.
set -euo pipefail

cd "$(dirname "$0")/.."

# The mesh name, so this works from anywhere the mesh reaches rather than only
# from the LAN. Falls back to the address, for a machine whose tunnel is down —
# publishing should not be the thing that needs the VPN to be working.
HOST=${LAN_HOST:-jimmy-crib.mesh}
FALLBACK=${LAN_HOST_FALLBACK:-192.168.0.152}
PUBLISH=${LAN_PUBLISH:-.claude/skills/logos-publish-artifacts/publish.sh}
STAGE=${LAN_STAGE:-.cache/shrooms-publish}

[ "$#" -gt 0 ] || { echo "usage: $0 <module.lgx>..." >&2; exit 2; }

if ! ssh -o BatchMode=yes -o ConnectTimeout=6 "$HOST" true 2>/dev/null; then
    echo "==> $HOST did not answer, using $FALLBACK"
    HOST=$FALLBACK
fi

ssh "$HOST" "mkdir -p '$STAGE'"

args=()
for lgx in "$@"; do
    lgx=$(readlink -f "$lgx")
    name=$(basename "$lgx")
    scp -q "$lgx" "$HOST:$STAGE/$name"
    # Writable on arrival: these come out of the nix store at mode 444 and scp
    # carries that across, so the next publish could not overwrite them.
    ssh "$HOST" "chmod 644 '$STAGE/$name'"
    args+=("\$HOME/$STAGE/$name")
done

echo "==> publishing on $HOST"
# --no-fdroid-update: the APK is published by `make fdroid`, which signs the
# index with the repo keystore. Asking for it here would regenerate that index
# with nothing new in it.
ssh "$HOST" "\$HOME/$PUBLISH --no-fdroid-update $(printf -- '--lgx %s ' "${args[@]}")"
