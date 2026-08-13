#!/usr/bin/env bash
# Load the Basecamp view offscreen and assert it read a fixture.
#
# There is no display here and Basecamp cannot be driven headlessly, but the
# QML runtime can: this catches the failures that matter — a view that does not
# load, and one that loads but understands nothing.
#
#   make basecamp-check                    # uses the checked-in fixture
#   FIXTURE=/run/shrooms/status.json make basecamp-check
set -euo pipefail
cd "$(dirname "$0")/../.."

FIXTURE=${FIXTURE:-basecamp/test/status.json}
# Where the qml runtime lives depends on how Qt was installed, and this script
# runs in two places that answer differently: a nix shell here, and a
# distribution package in CI. Looking only in the nix store made the check pass
# locally and fail in CI with a message nobody reads as "Qt is somewhere else".
if [ -z "${QML:-}" ]; then
    # Every layout this has actually been run under. Debian and Ubuntu put the
    # binary under a multiarch directory rather than /usr/lib/qt6, which is why
    # the first attempt at this still failed in CI with "no qml runtime found"
    # on a machine that had just installed one.
    for candidate in \
        $(command -v qml6 2>/dev/null) \
        $(command -v qml 2>/dev/null) \
        /usr/lib/qt6/bin/qml \
        $(ls -d /usr/lib/*/qt6/bin/qml 2>/dev/null | head -1) \
        $(ls -d /usr/lib/qt6/libexec/qml 2>/dev/null | head -1) \
        $(ls -d /nix/store/*-qtdeclarative-*/bin/qml 2>/dev/null | sort -V | tail -1) \
        $(find /usr/lib /usr/lib64 /usr/libexec /usr/local/lib -maxdepth 4 \
              -name qml -type f -perm -u+x 2>/dev/null | head -1)
    do
        [ -x "$candidate" ] || continue
        QML=$candidate
        break
    done
fi
if [ -z "${QML:-}" ] || [ ! -x "$QML" ]; then
    echo "no qml runtime found; set QML=/path/to/qml" >&2
    # What was actually there, because "not found" on a machine that just
    # installed Qt is a packaging question and the answer is a directory
    # listing.
    # The last candidate is a bounded find over the usual prefixes, so
    # reaching here means Qt's qml runtime is genuinely not installed rather
    # than installed somewhere this script has not heard of — which is what
    # every previous version of this message meant and did not say.
    echo "looked in: PATH, /usr/lib/qt6/bin, /usr/lib/*/qt6/bin, /nix/store," >&2
    echo "and a find under /usr/lib, /usr/lib64, /usr/libexec, /usr/local/lib" >&2
    ls -d /usr/lib/*/qt6/bin /usr/lib/qt6/* 2>/dev/null >&2 || true
    exit 1
fi
echo "==> qml runtime: $QML"

# The module path sits beside the binary, but the layout differs between a nix
# store path and a distribution one, so take whichever exists.
QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt-6/qml
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/qml
[ -d "$QMLDIR" ] || QMLDIR=$(ls -d /usr/lib/*/qt6/qml 2>/dev/null | head -1)
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt6/qml
echo "==> qml modules: $QMLDIR"

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

echo "==> reads a status file sitting beside the view (the Basecamp case)"
out=$(QML_XHR_ALLOW_FILE_READ=1 run "$QML" -I "$work" "$work/Harness.qml" "$work/status.json")
echo "$out" | grep -E "^qml: (PEERS|VERSION|SERVICES|SWITCHABLE|MODE|BOUNDHERE|  )" || true
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: the view loaded but read no peers"; echo "$out" | head -20; exit 1; }
echo "$out" | grep -q "TypeError\|ReferenceError\|is not a" && { echo "FAIL: script errors"; echo "$out"; exit 1; }

# The sections that are not the roster. A Repeater over an empty model renders
# perfectly, so without these the check would pass on a view that understood
# none of the payload the daemon has gained since.
svcs=$(echo "$out" | sed -n 's/.*SERVICES=\([0-9]*\).*/\1/p' | head -1)
[ "${svcs:-0}" -gt 0 ] || { echo "FAIL: read no services from a fixture that has three"; exit 1; }
echo "$out" | grep -q "ssh jimmy-crib.test.mesh:22 bound" \
    || { echo "FAIL: a bound port did not render as host:port (ADR-026)"; exit 1; }
# The name has to reach the mesh the port is on. The short form is answered by
# the first mesh alone, so an unqualified name for a peer on any other mesh
# points at an address on a network it is not on — the same bug three times.
echo "$out" | grep -q "jimmy-crib.mesh:22" && { echo "FAIL: an unqualified name for a peer on a second mesh"; exit 1; }
true
echo "$out" | grep -q "http://immich.jimmy-crib.test.mesh" \
    || { echo "FAIL: an announced service did not render as a URL (ADR-023)"; exit 1; }
echo "$out" | grep -q "DNS=resolving" \
    || { echo "FAIL: did not read the daemon's name-resolution state"; exit 1; }
echo "$out" | grep -q "VERSION=v0.9.1-test" \
    || { echo "FAIL: did not read the daemon's version"; exit 1; }
# A credential that has run out and one that never arrived are different
# things, and rendering them alike sends somebody chasing a renewal.
echo "$out" | grep -q "membership nothing ended" \
    || { echo "FAIL: an expired credential did not read as ended"; exit 1; }

# The settings section. A mesh switched off has no instance behind it, so it is
# absent from everything derived from the running meshes — and the list with the
# switch on it must not be one of those. That is how a mesh went missing.
echo "$out" | grep -q "SWITCHABLE=4 RUNNING=2" \
    || { echo "FAIL: a switched-off mesh is missing from the list that can switch it on"; exit 1; }
# Lit for the option in force, dim for the other. Fixed colours are why "on"
# appeared highlighted next to a mesh that was off.
echo "$out" | grep -q "mesh test on=false lit=#6b7680" \
    || { echo "FAIL: a switched-off mesh is not drawn as off"; exit 1; }
echo "$out" | grep -q "mesh default on=true lit=#35f0a0 primary=true" \
    || { echo "FAIL: the primary mesh is not lit, or was not recognised as primary"; exit 1; }
# The config and the running process disagree between a change and the restart
# that applies it; both have to reach the view or the click looks like a no-op.
# The switch that discloses bound ports must sit next to the ports it would
# disclose, carrying the mesh label — an unqualified host names an address on
# another network.
echo "$out" | grep -q "BOUNDHERE=2 \[ssh:22@vps.default.mesh:22\*,dev:3000@vps.default.mesh:3000\*\]" \
    || { echo "FAIL: this device's bound ports are missing or misnamed"; exit 1; }
echo "$out" | grep -q "MODE=Edge RUNNING=Core ANNOUNCE=true BOUND=true RELAY=true PORTMAP=true" \
    || { echo "FAIL: did not read the configured settings alongside the running ones"; exit 1; }
# The two states between a click and the restart that applies it. Each of these
# belonged to neither list once, and each time the section emptied or doubled.
echo "$out" | grep -q "mesh pending on=true .*state=\[starts at restart\]" \
    || { echo "FAIL: a mesh switched on but not yet started is not shown as pending"; exit 1; }
echo "$out" | grep -q "mesh leaving .*state=\[left · stops at restart\]" \
    || { echo "FAIL: a mesh left but still running is not shown as leaving"; exit 1; }

# Inside Basecamp only the sibling file resolves; the two below are for running
# outside it, where there is no sandbox. Removing the sibling is what forces
# the view past its first source.
rm -f "$work/status.json"

echo "==> falls back to an absolute path when there is no sibling file"
cp "$FIXTURE" "$work/absolute.json"
out=$(QML_XHR_ALLOW_FILE_READ=1 run "$QML" -I "$work" "$work/Harness.qml" "$work/absolute.json")
echo "$out" | grep -E "^qml: PEERS" || true
peers=$(echo "$out" | sed -n 's/.*PEERS=\([0-9]*\).*/\1/p' | head -1)
[ "${peers:-0}" -gt 0 ] || { echo "FAIL: did not fall back to the absolute path"; echo "$out" | head -20; exit 1; }
rm -f "$work/absolute.json"

echo "==> falls back to the endpoint when no file can be read"
# Served under the name the harness points the view at, which is deliberately
# not "status.json" — that is the sibling file source 0 tries, and serving it
# here would mean the endpoint was never actually exercised.
cp "$FIXTURE" "$work/endpoint.json"

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
