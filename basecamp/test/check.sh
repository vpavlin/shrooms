#!/usr/bin/env bash
# Load the Basecamp view offscreen and assert it read a fixture.
#
# There is no display here and Basecamp cannot be driven headlessly, but the
# QML runtime can: this catches the failures that matter — a view that does not
# load, and one that loads but understands nothing.
#
#   make basecamp-check                    # uses the checked-in fixture
#   FIXTURE=/run/logos-vpn/status.json make basecamp-check
set -euo pipefail
cd "$(dirname "$0")/../.."

FIXTURE=${FIXTURE:-basecamp/test/status.json}
QML=${QML:-$(ls -d /nix/store/*-qtdeclarative-*/bin/qml 2>/dev/null | sort -V | tail -1)}
[ -n "${QML:-}" ] && [ -x "$QML" ] || { echo "no qml runtime found; set QML=/path/to/qml"; exit 1; }
QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt-6/qml

work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
cp basecamp/Main.qml basecamp/test/Harness.qml "$work/"
cp "$FIXTURE" "$work/status.json"

run() {
    QT_QPA_PLATFORM=offscreen QT_ASSUME_STDERR_HAS_CONSOLE=1 \
    QML_IMPORT_PATH="$QMLDIR" QML2_IMPORT_PATH="$QMLDIR" "$@" 2>&1
}

echo "==> view loads and reads the fixture"
out=$(QML_XHR_ALLOW_FILE_READ=1 run "$QML" -I "$work" "$work/Harness.qml" "$work/status.json")
echo "$out" | grep -E "^qml: (PEERS|  )" || true
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: the view loaded but read no peers"; echo "$out" | head -20; exit 1; }
echo "$out" | grep -q "TypeError\|ReferenceError\|is not a" && { echo "FAIL: script errors"; echo "$out"; exit 1; }

echo "==> falls back to the endpoint when the file cannot be read"
( cd "$work" && python3 -m http.server 8787 --bind 127.0.0.1 >/dev/null 2>&1 & echo $! > "$work/pid" )
sleep 1
out=$(run "$QML" -I "$work" "$work/Harness.qml" "$work/missing.json")
kill "$(cat "$work/pid")" 2>/dev/null || true
echo "$out" | grep -E "^qml: PEERS" || true
echo "$out" | grep -q "FILEBLOCKED=true" || { echo "FAIL: did not escalate to the endpoint"; exit 1; }
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: fallback read no peers"; exit 1; }

echo
echo "both transports OK"
