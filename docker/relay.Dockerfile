# A blind relay (docs/blind-relays.md).
#
# Nothing like the node image above it, and that is the point: a relay links no
# native library, holds no mesh identity, and keeps no state, so it builds from
# source in one stage and ships on scratch. A few megabytes, no volume, no
# capabilities, running as nobody.
#
# That is what makes it deployable on ephemeral compute — Akash, a free tier, a
# spare container somewhere — where a persistent volume is the usual obstacle
# and there is nothing here to persist. A redeployed relay costs one refresh
# interval of downtime, because the forwarding table is soft state that its
# clients rebuild without being asked.
# --platform=$BUILDPLATFORM pins the build stage to the machine doing the
# building, and GOARCH below picks the target. Without it, buildx runs this
# stage under QEMU for every architecture — minutes become tens of minutes, and
# for no reason: a Go compiler cross-compiles natively, and there is no cgo here
# to need a cross toolchain.
#
# This is the one image in the project that can be built for any architecture on
# any machine. The node image cannot, because liblogosdelivery has to be built
# or fetched per architecture and building it from source has been broken
# upstream since September. None of that applies here.
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine AS build
WORKDIR /src

# Dependencies first, so a source change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# CGO off is not an optimisation here, it is the requirement that lets this run
# on scratch: internal/relay depends on nothing native, and linking statically
# is what removes the need for a base image at all.
#
# Trimpath and no build id so the same source produces the same binary, which is
# worth having for something strangers are invited to run.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} GOFLAGS=-trimpath \
    go build -ldflags="-s -w -buildid=" -o /shrooms-relay ./cmd/shrooms-relay

FROM scratch
COPY --from=build /shrooms-relay /shrooms-relay

# Links the package to this repository on ghcr.
#
# Not decoration: GitHub grants a workflow's GITHUB_TOKEN write access to
# packages associated with the repository, and association comes from this
# label. Without it a package pushed by hand stays unlinked, and CI gets
# "denied: permission_denied: write_package" — which is exactly what happened
# on the first run of the relay-image job, against a package created from a
# laptop in August.
#
# It fixes the next package rather than that one: an existing package still
# needs the repository added under its settings, once.
LABEL org.opencontainers.image.source=https://github.com/vpavlin/shrooms
LABEL org.opencontainers.image.description="Blind relay for shrooms (docs/blind-relays.md)"
LABEL org.opencontainers.image.licenses=MIT

# No certificates, no /etc/passwd, nothing else: the relay speaks raw UDP to
# addresses it is given and resolves no names, so an empty filesystem is not an
# austerity measure — there is genuinely nothing it needs.
#
# 65534 is nobody. A numeric id because there is no passwd file to look one up
# in, and unprivileged because binding 51820 needs no privilege.
USER 65534:65534

EXPOSE 51820/udp
ENTRYPOINT ["/shrooms-relay"]
