# Build logos-vpn against an older glibc so the binary runs on older systems.
#
# A binary linked against glibc 2.36 runs on newer systems; one built on the
# 2.42 host does not run on a Debian 12 VPS. Building here is what makes the
# artifact portable downward.
#
# Needs liblogosdelivery and its header. Supply them by mounting or by staging
# them into the build context at lib/:
#
#   docker build -f docker/build-vpn.Dockerfile --target dist -o dist .
ARG GLIBC_BASE=debian:bookworm

FROM ${GLIBC_BASE} AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION=1.24.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
        | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH

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
RUN go build -trimpath \
        -ldflags "-X main.version=${VERSION}" \
        -o /out/logos-vpn ./cmd/logos-vpn \
    && /out/logos-vpn --help 2>&1 | head -1 || true

# Distribution layout: bin/logos-vpn plus lib/, matching the rpath above.
FROM scratch AS dist
COPY --from=builder /out/logos-vpn /bin/
COPY lib/ /lib/
