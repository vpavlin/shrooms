#!/usr/bin/env bash
# Add this build of the Basecamp module to the LAN repository.
#
# Basecamp installs and updates modules from a repository — a logos-repo.json
# pointing at an index that lists each package with its URL, size and hashes —
# and there is one of those on the machine that also serves the F-Droid repo.
# Copying an .lgx next to it is not the same thing, which is the mistake that
# left an old version showing in Basecamp for a day: it will not offer a file
# it was never told about.
#
#   ./scripts/publish-lan.sh
#   LAN_HOST=user@host LAN_DIR=/srv/lan LAN_BASE=https://host:8443 ./scripts/publish-lan.sh
#
# Idempotent: publishing the same version twice replaces the entry rather than
# adding a second one.
set -euo pipefail

cd "$(dirname "$0")/.."

HOST=${LAN_HOST:-192.168.0.152}
DIR=${LAN_DIR:-/home/vpavlin/perun/dist/lan}
# What Basecamp fetches from, which is not the path we write to.
BASE=${LAN_BASE:-https://192.168.0.152:8443}

LGX=$(readlink -f result/*.lgx 2>/dev/null || true)
[ -n "$LGX" ] || { echo "no .lgx in result/ — run 'make basecamp-lgx' first" >&2; exit 1; }

read -r NAME VERSION <<<"$(tar xzOf "$LGX" manifest.json |
    python3 -c 'import json,sys; m=json.load(sys.stdin); print(m["name"], m["version"])')"

echo "==> $NAME $VERSION -> $HOST:$DIR"

# The source is in the nix store and therefore mode 444, and scp carries that
# across — so the second publish of a version cannot overwrite the first, even
# as its owner, because write permission is checked for the owner too. Make
# room first, and leave the copies writable so this never happens again.
ssh "$HOST" "chmod -f u+w '$DIR/$NAME-$VERSION.lgx' '$DIR/$NAME.lgx' 2>/dev/null || true"
scp -q "$LGX" "$HOST:$DIR/$NAME-$VERSION.lgx"
scp -q "$LGX" "$HOST:$DIR/$NAME.lgx"
ssh "$HOST" "chmod 644 '$DIR/$NAME-$VERSION.lgx' '$DIR/$NAME.lgx'"

# Computed here, from the file that was just built, and applied there.
python3 scripts/index-entry.py >/dev/null
ENTRY=$(python3 -c "
import json
e = json.load(open('basecamp/index-entry.json'))
e['versions'][0]['url'] = '$BASE/$NAME-$VERSION.lgx'
print(json.dumps(e))
")

# One ssh, with the entry passed as an environment variable and the program on
# stdin. Quoted with printf %q rather than interpolated: the entry is JSON full
# of quotes and braces, and a remote shell would otherwise eat them.
ssh "$HOST" "ENTRY=$(printf %q "$ENTRY") DIR=$(printf %q "$DIR") python3 -" <<'PY'
import datetime
import json
import os
import tempfile

entry = json.loads(os.environ["ENTRY"])
path = os.path.join(os.environ["DIR"], "index.json")
with open(path) as f:
    idx = json.load(f)

# Replace this package wholesale rather than appending a version: a LAN
# repository exists to hold the current build, and a list of every one ever
# published is a different thing that nobody asked for here.
idx["packages"] = [p for p in idx["packages"] if p["name"] != entry["name"]]
idx["packages"].append(entry)
idx["generatedAt"] = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

# Written to a temporary file and moved into place, so an interruption leaves
# the index valid rather than half-written. Basecamp reading a truncated index
# is a worse failure than it reading an old one.
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path))
with os.fdopen(fd, "w") as f:
    json.dump(idx, f, indent=2)
    f.write("\n")
os.replace(tmp, path)
print("    index now lists:", ", ".join(sorted(p["name"] for p in idx["packages"])))
PY

echo "    $BASE/$NAME-$VERSION.lgx"
echo "    repository: $BASE/logos-repo.json"
