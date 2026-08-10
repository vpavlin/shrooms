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

# The fake endpoint is torn down by the same trap as the workdir, not on the
# success path. It used to be killed after the last assertion, so any earlier
# failure leaked the server — and because the port is baked into the view, the
# next run then talked to a leftover server holding a deleted directory, got a
# 404, and failed with "status is not readable JSON". One failure poisoned every
# run after it, which is a miserable thing to debug.
srv=""
work=$(mktemp -d)
cleanup() {
    [ -n "$srv" ] && kill "$srv" 2>/dev/null
    rm -rf "$work"
}
trap cleanup EXIT
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
# The view asks for /status, which is what the daemon serves. A static server
# has no such route, so the fixture is copied to that exact name — without it
# the server answers 404, the view reads zero peers, and the failure looks like
# a bug in the view rather than in the harness.
cp "$FIXTURE" "$work/status"

# The port is baked into the view, so it cannot simply be moved. Anything
# already holding it will answer instead of us and the test would report a
# false failure — so say what is wrong rather than testing the wrong server.
if (exec 3<>/dev/tcp/127.0.0.1/8787) 2>/dev/null; then
    exec 3<&-
    echo "FAIL: something already listens on 127.0.0.1:8787; it would answer instead of this test"
    ss -lntp 2>/dev/null | grep ':8787' || true
    exit 1
fi

( cd "$work" && exec python3 -m http.server 8787 --bind 127.0.0.1 >/dev/null 2>&1 ) &
srv=$!
sleep 1
out=$(run "$QML" -I "$work" "$work/Harness.qml" "$work/missing.json")
echo "$out" | grep -E "^qml: PEERS" || true
echo "$out" | grep -q "FILEBLOCKED=true" || { echo "FAIL: did not escalate to the endpoint"; exit 1; }
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: fallback read no peers"; exit 1; }

echo
echo "both transports OK"
