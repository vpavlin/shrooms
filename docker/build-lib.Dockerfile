# Build liblogosdelivery from source in a controlled environment.
#
# Building on a current host (Ubuntu 25.10, git 2.51) fails two ways, neither in
# our code:
#
#   1. nimble's lockfile checksum for Nim itself does not match what it computes
#      from the git checkout — the checksum is sensitive to how the checkout
#      materialises, so this is plausibly git-version dependent.
#   2. a nimble path bug on git-ref-pinned deps: for bearssl_pkey_decoder the
#      staging directory name contains '#', and nimble creates the directory
#      truncated at the '#' then runs `git -C` on the full name.
#
# This image pins an older toolchain to test whether (1) goes away. Build with:
#
#   docker build -f docker/build-lib.Dockerfile --target lib -o docker/build/lib .
#
# Pinned by digest-free tag deliberately: if this ever needs to be reproducible
# to the byte, pin the digest.
FROM debian:bookworm AS builder

# bookworm ships git 2.39, notably older than the host's 2.51.
#
# cmake is for upstream master, not the pinned rev: master depends on nim-leopard, which builds
# its vendored Leopard-RS with cmake in the middle of the Nim compile, and without it the build
# dies at "cmake: not found". The pinned nimble.lock has no leopard, so there it is unused.
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates cmake curl git \
        libpcre3-dev libssl-dev pkg-config \
        python3 which xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Rust is needed for zerokit (librln).
ENV RUSTUP_HOME=/opt/rustup CARGO_HOME=/opt/cargo
ENV PATH=/opt/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
        | sh -s -- -y --profile minimal --default-toolchain stable

# Pinned, not master. Two reasons:
#   - reproducibility: a build that tracks a moving branch is not reproducible
#   - master is currently broken. At a5d7818 the Nim compile fails with
#     "illegal effect: NestedPoll" in rest_api/endpoint/relay/handlers.nim.
# This revision is what the Android bindings pin, so it is known to build.
ARG LD_REF=7a3a064b52742434b3e40260e98e94abf006442b
WORKDIR /src
RUN git clone https://github.com/logos-messaging/logos-delivery.git . \
    && git checkout "$LD_REF" \
    && git rev-parse HEAD > /src/.ld-rev

# Two patches to the pinned tree, both reported and verified in issue #11.
#
# Carried here rather than by pinning a different revision because this rev is
# the one the Android bindings use, and building the library from a different
# source than the phone runs is a worse problem than two sed lines. Both are
# fixed upstream on master; when the pin moves forward, delete this.
#
# Verified by outcome, not by whether the patch matched. Upstream master has
# already fixed both, and this same file builds master in the build-from-source
# job — so "the sed changed something" is the wrong test: it would fail on the
# tree that needs no patching. What must hold either way is that the bad
# construct is absent by the time make runs.
RUN set -eux; \
    # 1. The taskpools pin is no longer needed: it existed to stop nimble
    #    re-resolving past the lockfile, and nimble no longer resolves at all
    #    (the deps come from nimble.lock). Left out deliberately rather than
    #    kept as folklore.
    # 2. chronos 4.4.0 refuses `waitFor` inside an async handler — NestedPoll —
    #    and this call site is already inside one that awaits eight lines up.
    #    Upstream master uses the await form.
    #    Matched on the construct, not on one spelling of its arguments. The
    #    report quoted `let _ = waitFor node.publish(some(...))`; the pinned
    #    tree has it inside `if not (...)`, and master has `Opt.some(...)`
    #    after an API change. A pattern tied to the argument list matched one
    #    of the three and silently skipped the others — which is how this
    #    reached CI. `waitFor node.publish` is the part that is actually wrong.
    sed -i 's|waitFor node\.publish|await node.publish|g' \
        logos_delivery/waku/rest_api/endpoint/relay/handlers.nim; \
    ! grep -rq 'waitFor node.publish' logos_delivery/waku/rest_api/endpoint/relay/

# The Makefile bootstraps its own pinned nim/nimble via install-nim/install-nimble.
# Build serially: with -j, a real error surfaces as a 14000-line bogus
# "Couldn't find a solution for the packages" solver dump.
#
# bash with pipefail, because a pipe otherwise reports the exit status of the
# thing on the right and a Nim compile error becomes invisible: make fails, the
# pipe succeeds, the build carries on, and the next COPY reports a missing file
# with no sign of the real cause.
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# The whole log, kept, and the FIRST errors shown rather than the last.
#
# This was `make liblogosdelivery 2>&1 | tail -40`, which is the exact wrong
# forty lines. The note above says the solver dump is bogus and enormous; a tail
# keeps only the bogus part and discards the cause, so every arm64 failure since
# has read as "nimble cannot resolve the packages" whether or not that is what
# went wrong. It stopped this being diagnosable at all.
#
# Errors from the top, because the first one is the cause and everything after
# it is consequence — the opposite of what a tail gives you. The last 200 lines
# come too, for a failure with no line matching at all.
# The dependency resolution is gone, so nimble is not used at all here.
#
# At this revision nimble cannot resolve its own graph: it fails in `solveLockFileDeps` on a
# pristine tree with a freshly downloaded package list, and it fails AGAIN when running the
# compile task (`nimble liblogosdelivery …`), so staging deps and touching the setup stamp does
# not help. nimble.lock is itself a complete resolution -- 46 packages, each with a url, a
# vcsRevision and a sha1 -- so the script materialises from the lock and calls the compiler
# directly with the flags the .nimble's own buildLibrary proc uses.
#
# librln is a cargo build, not a nimble one, so it stays.
# The script has to be IN the image: the builder stage is a fresh debian:bookworm with the
# upstream repo cloned into /src, so nothing from this repository is present unless it is
# COPYed. Without this line the build dies with
#   sh: 0: cannot open /src/docker/build-lib-nimblefree.sh: No such file
# It was missing from the first handover because the originating builds ran the script on the
# host rather than through this Dockerfile — which is also why their artifact needed a newer
# glibc than the CI base provides.
#
# Invoked with bash, not sh: the script uses `set -o pipefail`, and Debian's /bin/sh is dash,
# which rejects it ("set: Illegal option -o pipefail"). The image has bash — this file already
# sets SHELL to bash — so the `sh` call was stepping outside that for no reason.
COPY docker/build-lib-nimblefree.sh /src/docker/build-lib-nimblefree.sh

# Bootstrap Nim from the UPSTREAM Makefile — /src is the logos-delivery clone, not this repo,
# so `make liblogosdelivery` and its install-nim prereq are upstream's targets, not shrooms'.
# (shrooms' own Makefile contains no reference to nim at all; a comment here used to imply
# otherwise, which is how a first attempt at this fix invoked a target that does not exist.)
#
# The nimble-free script calls `nim c` directly, so the compiler must exist. Previously
# `make liblogosdelivery` pulled install-nim in as a prerequisite; replacing that step removed
# the bootstrap with it, and in a fresh container the build dies with
#   build-lib-nimblefree.sh: line 236: nim: command not found   (exit 127)
# On the originating host it worked only because nim was already installed system-wide.
# nimble itself is deliberately still not bootstrapped: resolution is gone, so only the
# compiler is needed.
#
# All of the above is the OLD build system, the one the pinned LD_REF has. Upstream replaced it
# on 2026-10-01 (d3941be1, "adopt nimble 0.26.0"): install-nim is gone, so on master this line
# died with "No rule to make target 'install-nim'" and the build-from-source job broke.
#
# Which system a tree has is detected, not assumed from LD_REF, because the job builds whatever
# master is that night. The test is the install-nim target itself — the one thing the old path
# needs. A tree without it gets upstream's own build, not the nimble-free script: the new nimble
# resolves nimble.lock itself (`nimble setup`, which upstream's CI runs), and the script's
# hand-copied compile flags have already drifted from master's buildLibrary — it lacks the
# ffiGenBindings flags that write library/generated/, and the leopard C library build-deps
# now makes. Copying them again would just drift again.
#
# The pinned path runs exactly the commands it ran before.
RUN cd /src \
    && if grep -q '^install-nim:' Makefile; then \
           make install-nim \
           && make librln \
           && bash /src/docker/build-lib-nimblefree.sh /src; \
       else \
           make liblogosdelivery NIMFLAGS="-d:disableMarchNative"; \
       fi

# tinycbor, on the new build system only: master's generated header includes <tinycbor/cbor.h>
# and its request/reply encoding links it, so a consumer cannot even compile without it. Upstream
# builds the archive from the copy nim-ffi vendors; it is staged out below. A separate step so
# the slow one above stays cached when only this changes.
RUN if ! grep -q '^install-nim:' Makefile; then make tinycbor; fi

# The public header includes "generated/logosdelivery.h", which upstream says
# plainly is "a build artifact, not checked in" — written by the build we just
# ran. Only the public header used to be copied out, so every include of it
# died at:
#
#   liblogosdelivery.h:15: fatal error: generated/logosdelivery.h: No such file
#
# The prebuilt release tarball ships a self-contained header and does not need
# this, which is why only the from-source path was broken — and why it looked
# like the upstream breakage it was filed under rather than a missing COPY here.
#
# Staged into one directory rather than copied straight out of the tree: mkdir
# -p means a revision that does not generate the header exports an empty
# directory instead of failing the COPY, so this file keeps working across a
# moving upstream.
RUN mkdir -p /out/generated \
    && cp build/liblogosdelivery.so /out/ \
    && cp library/liblogosdelivery.h /out/ \
    && cp .ld-rev /out/ \
    && { cp -a library/generated/. /out/generated/ 2>/dev/null || true; } \
    && if [ -f build/libtinycbor.a ]; then \
           cbor=$(ls -dt nimbledeps/pkgs2/ffi-*/ffi/codegen/templates/cpp/vendor/tinycbor | head -1) \
           && mkdir -p /out/tinycbor \
           && cp build/libtinycbor.a /out/ \
           && cp "$cbor"/*.h /out/tinycbor/; \
       fi

# Collect the artifacts.
FROM scratch AS lib
COPY --from=builder /out/ /
