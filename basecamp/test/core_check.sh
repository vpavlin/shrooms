#!/usr/bin/env bash
# Builds core_check against the core's sources and runs it against a stand-in
# daemon. Needs the Logos C++ SDK headers: LOGOS_SDK_INCLUDE, or found in the
# nix store.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
inc="${LOGOS_SDK_INCLUDE:-$(dirname "$(find /nix/store -maxdepth 4 -name logos_module_context.h -path '*sdk-headers*' 2>/dev/null | sort | tail -1)")}"
[ -f "$inc/logos_module_context.h" ] || { echo "no Logos SDK headers; set LOGOS_SDK_INCLUDE"; exit 2; }
work="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$work"' EXIT
g++ -std=c++17 -O1 -Wall -pthread -I"$inc" -o "$work/core_check" \
    "$here/core_check.cpp" "$here/../core/src/shrooms_core_impl.cpp" "$here/../core/src/shrooms_agents.cpp"
python3 -I "$here/fake_daemon.py" "$work/sock" &
for _ in $(seq 50); do [ -S "$work/sock" ] && break; sleep 0.1; done
SHROOMS_CONTROL_SOCKET="$work/sock" "$work/core_check"
