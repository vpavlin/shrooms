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
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl git \
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
    # 1. nimble re-resolves taskpools past the lockfile pin (0.2.1 instead of
    #    0.1.0), and 0.2.1 dropped taskpools/channels_spsc_single.nim, so the
    #    build dies on a missing import. Naming the commit leaves nothing to
    #    resolve. `nimble setup --localdeps` re-runs on every make, so fixing
    #    the staged tree by hand is undone; the requirement is the durable fix.
    sed -i 's|^\( *\)"taskpools",|\1"https://github.com/status-im/nim-taskpools#9e8ccc754631ac55ac2fd495e167e74e86293edb",|' logos_delivery.nimble; \
    ! grep -qE '^ *"taskpools",' logos_delivery.nimble; \
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
# bash with pipefail, because the pipe below otherwise reports the exit status
# of `tail` and a Nim compile error becomes invisible: make fails, tail
# succeeds, the build carries on, and the next COPY reports a missing file with
# no sign of the real cause.
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN echo "building liblogosdelivery from $(cat /src/.ld-rev)" \
    && make liblogosdelivery 2>&1 | tail -40

# Collect the artifacts.
FROM scratch AS lib
COPY --from=builder /src/build/liblogosdelivery.so /
COPY --from=builder /src/library/liblogosdelivery.h /
COPY --from=builder /src/.ld-rev /
