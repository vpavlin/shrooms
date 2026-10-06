#!/usr/bin/env bash
# Build liblogosdelivery at the pinned revision WITHOUT nimble resolving anything.
#
# Runs inside docker/build-lib.Dockerfile's builder stage, at /src (a logos-delivery checkout at
# $LD_REF). Self-contained on purpose: one file the image can COPY and run.
#
# Why nimble is removed entirely: at this revision nimble cannot resolve its own graph -- it fails
# in solveLockFileDeps on a pristine tree with a freshly downloaded package list, and it fails
# AGAIN when running the compile task (`nimble liblogosdelivery ...`), so staging deps and touching
# the setup stamp is not enough. nimble.lock is itself a complete resolution (46 packages, each
# with a url, a vcsRevision and a sha1), so this materialises from the lock and calls the compiler
# directly, using the flags the .nimble's own buildLibrary proc uses.
#
# Verified on Atlas (amd64): two cold runs from clean, both rc=0, 233 MB liblogosdelivery.so,
# 420680 lines, ~21 min each. See agenteam/reports atlas/2026-09-17-shrooms-nimblefree-lib-build.md
set -euo pipefail

# Materialise a Nim dependency tree directly from nimble.lock.
#
# Why this exists: on the revision shrooms pins (logos-delivery 7a3a064b), nimble can no
# longer RESOLVE its own dependency graph -- 237 / 6 suppressed conflicts, and it fails the
# same way on a pristine tree with a freshly downloaded package list. Patching requirement
# pins is non-monotonic (237 -> 6 -> 4 -> 6); reordering does not help. So resolution is
# removed from the build: nimble.lock is already a complete resolution (every package with
# a url, a vcsRevision and a sha1), and we fetch each package at its recorded revision --
# git's own object id, which is a content hash.
#
# Layout notes, each learned by running it:
#   * BearSSL.mk and Nat.mk discover packages as nimbledeps/pkgs2/<name>-* and $(error) at
#     PARSE time without them, so the destination must be nimbledeps/pkgs2 and the dirs must
#     start with <name>-.
#   * nimble parses the third field of that name as a hash and validates it: a short field
#     is re-read as a version and rejected ("Wrong version: <field>"). Use the lock's full
#     40-char sha1.
#   * nimble validates the version field strictly: "v3.1.4" is rejected as "Wrong version:
#     v3.1.4", so a leading "v" before a digit is dropped.
#   * a revision-only fetch has no tags, so git describe fails and nimble falls back to a
#     bare SHA, which it also rejects. Fetch the tag naming the locked version, and for
#     packages the lock only names by ref, read the version out of the package itself.
#   * packages can carry submodules whose contents the build needs (bearssl/csources is
#     run with `make -C ... lib`), so initialise them.
#
# same tree, and repairs submodules/tags on checkouts that predate those steps.

materialise_from_lock() {
SRC="."
DEST="./nimbledeps/pkgs2"
LOCK="$SRC/nimble.lock"

[ -f "$LOCK" ] || { echo "no nimble.lock in $SRC" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 required" >&2; exit 1; }
command -v git >/dev/null || { echo "git required" >&2; exit 1; }

mkdir -p "$DEST"

python3 - "$LOCK" <<'PY' > "$DEST/.pkgs.tsv"
import json, sys
lock = json.load(open(sys.argv[1]))
for name, v in lock.get("packages", {}).items():
    url = (v.get("url") or "").strip()
    rev = (v.get("vcsRevision") or "").strip()
    if not url or not rev:
        continue
    ver = str(v.get("version") or "").lstrip("#")
    if len(ver) > 1 and ver[0] == "v" and ver[1].isdigit():
        ver = ver[1:]
    ver = "".join(c for c in ver if c.isalnum() or c in ".-_")
    sha = (v.get("checksums") or {}).get("sha1") or ""
    print(f"{name}\t{url}\t{rev}\t{ver}\t{sha}")
PY

total=$(wc -l < "$DEST/.pkgs.tsv")
echo "==> $total packages to materialise into $DEST"

# A lock version can be a ref rather than a version ("#b12f5ee…"), and nimble rejects a bare
# SHA as a version. Derive one: the nearest tag, else the package's own .nimble, else 0.0.0.
derive_version() {
    dir="$1"; hint="$2"
    case "$hint" in
        *[!0-9a-f]*|"") ;;                       # not a bare hex SHA: keep the hint
        *) if [ ${#hint} -eq 40 ]; then
               d=$(git -C "$dir" describe --tags --abbrev=0 2>/dev/null || true)
               d=${d#v}
               if [ -n "$d" ]; then echo "$d"; return 0; fi
               f=$(ls "$dir"/*.nimble 2>/dev/null | head -1 || true)
               if [ -n "$f" ]; then
                   d=$(sed -nE 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$f" | head -1 || true)
                   if [ -n "$d" ]; then echo "$d"; return 0; fi
               fi
               echo "0.0.0"; return 0
           fi ;;
    esac
    if [ -n "$hint" ]; then echo "$hint"; else echo "0.0.0"; fi
}

fetch_version_tag() {
    dir="$1"; ver="$2"
    [ -n "$ver" ] || return 1
    candidates="$ver"
    case "$ver" in v*) ;; *) candidates="$candidates v$ver" ;; esac
    for c in $candidates; do
        if git -C "$dir" fetch -q --depth 1 origin "refs/tags/$c:refs/tags/$c" 2>/dev/null; then
            return 0
        fi
    done
    return 1
}

ensure_extras() {   # submodules + a describing tag, on any path that considers a dir usable
    dir="$1"; ver="$2"
    [ -f "$dir/.gitmodules" ] && git -C "$dir" submodule update --init --recursive -q 2>/dev/null || true
    git -C "$dir" describe --tags >/dev/null 2>&1 || fetch_version_tag "$dir" "$ver" || true
}

ok=0; failed=0; skipped=0
while IFS=$'\t' read -r name url rev ver sha; do
    want_peel_of() { git -C "$1" rev-parse "$rev^{commit}" 2>/dev/null || echo ""; }

    # Already materialised at the right revision? Find it by revision, not by guessing the name.
    existing=""
    for d in "$DEST/$name"-*; do
        [ -d "$d/.git" ] || continue
        have=$(git -C "$d" rev-parse HEAD 2>/dev/null || echo "")
        w=$(want_peel_of "$d")
        [ -n "$have" ] && [ "$have" = "$w" ] && { existing="$d"; break; }
    done

    if [ -n "$existing" ]; then
        v=$(basename "$existing" | sed -E "s/^[^-]+-([^-]+)-[0-9a-f]{40}$/\1/")
        ensure_extras "$existing" "$v"
        skipped=$((skipped + 1))
        printf '  = %-22s already at %s\n' "$name" "${rev:0:12}"
        continue
    fi

    stage="$DEST/.stage-$name"
    rm -rf "$stage"; mkdir -p "$stage"

    if git -C "$stage" init -q \
        && git -C "$stage" remote add origin "$url" \
        && git -C "$stage" fetch -q --depth 1 origin "$rev" 2>/dev/null \
        && git -C "$stage" checkout -q FETCH_HEAD; then
        have=$(git -C "$stage" rev-parse HEAD)
        # A lock revision can be an annotated TAG object, so compare PEELED.
        want=$(git -C "$stage" rev-parse "$rev^{commit}")
        if [ "$have" != "$want" ]; then
            echo "  ! $name fetched $have but the lock pins $rev (peels to $want)" >&2
            failed=$((failed + 1)); rm -rf "$stage"; continue
        fi
        final_ver=$(derive_version "$stage" "$ver")
        target="$DEST/$name-$final_ver-$sha"
        [ -n "$sha" ] || target="$DEST/$name-$final_ver-${rev:0:12}"
        rm -rf "$target"
        mv "$stage" "$target"
        ensure_extras "$target" "$final_ver"
        fetch_version_tag "$target" "$final_ver" || true
        ok=$((ok + 1))
        printf '  + %-22s %s  (v%s)\n' "$name" "${rev:0:12}" "$final_ver"
    else
        echo "  ! $name could not fetch $rev from $url" >&2
        failed=$((failed + 1)); rm -rf "$stage"
    fi
done < "$DEST/.pkgs.tsv"

echo
echo "==> materialised: $ok   already present: $skipped   failed: $failed"
[ "$failed" -eq 0 ] || exit 1
echo "==> every package verified against its nimble.lock vcsRevision"
}

# Build liblogosdelivery from the pinned logos-delivery revision WITHOUT nimble resolving anything.
#
# Why: nimble can no longer resolve this revision's dependency graph -- it fails in
# solveLockFileDeps on a pristine tree with a freshly downloaded package list, and it fails again
# when it runs a task (the compile is `nimble liblogosdelivery ...`), so touching the setup stamp
# is not enough. nimble.lock, however, IS a complete resolution: every package with a url, a
# vcsRevision and a sha1. So we materialise from the lock and invoke the compiler directly.
#
# The compile command is the one the .nimble's own `buildLibrary` proc runs for a dynamic Linux
# build (plus getMyCPU/getNimParams), minus nimble. Paths are derived per package: nimble adds the
# package root AND its declared srcDir, which is arbitrary ("sds", "ffi", "src"), so read it from
# each package's own .nimble rather than assuming.
#
# Usage: build-lib-nimblefree.sh [logos-delivery-dir]     (run at the pinned revision)
set -euo pipefail

REPO="${1:-$(pwd)}"
cd "$REPO"

[ -f nimble.lock ] || { echo "not a logos-delivery checkout: $REPO" >&2; exit 1; }


export PATH="$HOME/.nimble/bin:$(ls -d "$HOME"/.nim/nim-* 2>/dev/null | head -1)/bin:$PATH"

echo "==> 1/4 materialise dependencies from nimble.lock (no resolution)"
materialise_from_lock 

# build-deps also had non-nimble work: it builds the C libraries vendored inside packages
# (bearssl/csources and nat_traversal's miniupnpc + libnatpmp). Those are LINK inputs, and
# skipping them fails at link time with "cannot find .../libminiupnpc.a". They invoke no
# resolver, so they stay.
echo "==> 2/4 build the vendored C libraries (bearssl + nat), which are link inputs"
make rebuild-bearssl-nimbledeps rebuild-nat-libs-nimbledeps

echo "==> 3/4 derive --path entries (package root + declared srcDir)"
PATHS=""
for d in nimbledeps/pkgs2/*/; do
    d=${d%/}
    PATHS="$PATHS --path:$d"
    nb=$(ls "$d"/*.nimble 2>/dev/null | head -1 || true)
    if [ -n "$nb" ]; then
        sdir=$(sed -nE 's/^[[:space:]]*srcDir[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$nb" | head -1 || true)
        if [ -n "$sdir" ] && [ "$sdir" != "." ] && [ -d "$d/$sdir" ]; then
            PATHS="$PATHS --path:$d/$sdir"
        fi
    fi
    [ -d "$d/src" ] && PATHS="$PATHS --path:$d/src"
done
echo "    $(echo $PATHS | wc -w) path entries"

echo "==> 4/4 compile (no nimble)"
LIBRLN=$(ls librln_*.a 2>/dev/null | head -1 || true)
if [ -z "$LIBRLN" ]; then
    echo "    no librln_*.a present; run 'make librln' (cargo) first" >&2
    exit 1
fi
export NIM_PARAMS="--passL:$LIBRLN --passL:-lm"

# The flags below are `buildLibrary`'s dynamic branch for Linux:
#   --out:build/<lib>.so --threads:on --app:lib --opt:speed --noMain --mm:refc --header
#   -d:metrics --nimMainPrefix:<prefix> --skipParentCfg:off -d:discv5_protocol_id=d5waku
# plus getMyCPU()'s --cpu:<arch> and the Linux dynamic params.
CPU="--cpu:amd64"
[ "$(uname -m)" = "aarch64" ] && CPU="--cpu:arm64"

# -d:disableMarchNative: without it logos-delivery's config.nims passes
# -march=native on every Linux build, so the library takes on the instruction
# set of whatever machine built it. An arm64 build made on a CPU with LSE
# atomics died with SIGILL on an ARMv8.0 Chromebook (Cortex-A53/A73,
# 2026-10-06); with it, arm64 gets GCC's generic armv8-a (outline atomics:
# LSE where the CPU has it, found at run time) and amd64 -mssse3.
nim c --out:build/liblogosdelivery.so --threads:on --app:lib --opt:speed --noMain --mm:refc --header \
    -d:disableMarchNative \
    -d:metrics --nimMainPrefix:liblogosdelivery --skipParentCfg:off -d:discv5_protocol_id=d5waku \
    $CPU $NIM_PARAMS -d:chronicles_line_numbers --warning:Deprecated:off --warning:UnusedImport:on \
    -d:chronicles_log_level=TRACE $PATHS library/liblogosdelivery.nim

echo
echo "==> built: $(ls -lh build/liblogosdelivery.so | awk '{print $5, $NF}')"
echo "==> glibc ceiling: $(objdump -T build/liblogosdelivery.so 2>/dev/null | grep -oE 'GLIBC_[0-9.]+' | sort -uV | tail -1)"
