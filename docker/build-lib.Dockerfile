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

ARG LD_REF=master
WORKDIR /src
RUN git clone https://github.com/logos-messaging/logos-delivery.git . \
    && git checkout "$LD_REF" \
    && git rev-parse HEAD > /src/.ld-rev

# The Makefile bootstraps its own pinned nim/nimble via install-nim/install-nimble.
# Build serially: with -j, a real error surfaces as a 14000-line bogus
# "Couldn't find a solution for the packages" solver dump.
RUN echo "building liblogosdelivery from $(cat /src/.ld-rev)" \
    && make liblogosdelivery 2>&1 | tail -40

# Collect the artifacts.
FROM scratch AS lib
COPY --from=builder /src/build/liblogosdelivery.so /
COPY --from=builder /src/library/liblogosdelivery.h /
COPY --from=builder /src/.ld-rev /
