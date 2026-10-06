# Build shrooms against the oldest glibc that can actually link, so the binary
# runs on as many systems as possible.
#
# That floor is set by liblogosdelivery, not by us: it references
# __isoc23_strtoull@GLIBC_2.38, so a Debian 12 base (2.36) fails at link time
# no matter what our own code needs. Ubuntu 24.04 is 2.39 and is the oldest
# base that works, which puts the result on Ubuntu 24.04+, Debian 13+ and
# anything newer. Building here is still what stops the artifact depending on
# whatever glibc the developer machine happens to have (2.42 today).
#
# Needs liblogosdelivery and its header. Supply them by mounting or by staging
# them into the build context at lib/:
#
#   docker build -f docker/build-vpn.Dockerfile --target dist -o dist .
#
# For another architecture, cross-compiled rather than emulated — the builder
# runs on this machine's platform with Ubuntu's cross toolchain, whose glibc is
# the same 2.39 — and the library staged must be that architecture's:
#
#   docker buildx build --platform linux/arm64 -f docker/build-vpn.Dockerfile --target dist -o dist-arm64 .
ARG GLIBC_BASE=ubuntu:24.04

FROM --platform=$BUILDPLATFORM ${GLIBC_BASE} AS builder
ARG TARGETARCH
ARG BUILDARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl git \
        $( [ "$TARGETARCH" = arm64 ] && [ "$BUILDARCH" != arm64 ] && echo gcc-aarch64-linux-gnu libc6-dev-arm64-cross ) \
    && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION=1.24.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${BUILDARCH}.tar.gz" \
        | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH
ENV GOOS=linux GOARCH=${TARGETARCH}

# liblogosdelivery.so + liblogosdelivery.h, staged by scripts/build-portable.sh.
COPY lib/ /opt/logos/lib/

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/opt/logos/lib"
# $ORIGIN/../lib so the binary finds the library relative to its install
# location rather than an absolute path baked in at build time.
ENV CGO_LDFLAGS="-L/opt/logos/lib -llogosdelivery -Wl,-rpath,\$ORIGIN/../lib"

ARG VERSION=dev
# The agent too: pure Go, no library, the same for every glibc. It ships in the
# package so a machine without a container runtime can have one
# (install-agent.sh --binary).
RUN CGO_ENABLED=0 go build -trimpath -o /out/shrooms-agent ./cmd/shrooms-agent
# Not "|| true" on the build itself. It used to be, which meant a failed
# compile produced a successful layer with no binary in it, and the error
# surfaced three stages later as "/out/shrooms: not found".
RUN if [ "$TARGETARCH" = arm64 ] && [ "$BUILDARCH" != arm64 ]; then export CC=aarch64-linux-gnu-gcc; fi; \
    go build -trimpath \
        -ldflags "-X main.version=${VERSION}" \
        -o /out/shrooms ./cmd/shrooms
# Only where it can run: a cross-compiled binary is checked on its target.
RUN [ "$TARGETARCH" != "$BUILDARCH" ] || /out/shrooms version || true

# Distribution layout: bin/shrooms plus lib/, matching the rpath above.
#
# The library comes from the builder stage rather than the context: it was
# already copied in there, and a second COPY from the context needs a lib/ at
# the repo root that only exists if someone staged one — which is how this
# broke silently at the rename and stayed broken.
FROM scratch AS dist
COPY --from=builder /out/shrooms /out/shrooms-agent /bin/
COPY --from=builder /opt/logos/lib/ /lib/
