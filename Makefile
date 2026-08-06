# logos-vpn
#
# Building needs liblogosdelivery (shared library + matching header). There is
# no canonical place for it, so pick one:
#
#   make deps                 build from source in a pinned container (slow, portable)
#   make deps-basecamp        reuse the copy Logos Basecamp installed (fast, local)
#   make LD_DIR=/path/to/lib  point at your own build
#
# `make dist` produces a portable distribution built entirely in containers.

LD_DIR ?= docker/build/lib
LD_LIB ?= $(LD_DIR)
LD_INC ?= $(LD_DIR)

BASECAMP_LIB ?= $(HOME)/.local/share/Logos/LogosBasecamp/modules/delivery_module

# Absolute paths: cgo runs the compiler in each package's directory, so a
# relative -I or -L resolves relative to internal/waku rather than the repo root.
export CGO_CFLAGS  += -I$(abspath $(LD_INC))
export CGO_LDFLAGS += -L$(abspath $(LD_LIB)) -llogosdelivery -Wl,-rpath,$(abspath $(LD_LIB))

GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all deps deps-basecamp check-lib logos-vpn wakuspike s3topics m0demo \
        s1 s3 probe m0 m1 m2 m2-edm dist test test-unit fmt clean

all: logos-vpn

## --- dependencies ---

## Build liblogosdelivery from source in a pinned container.
deps:
	docker build -f docker/build-lib.Dockerfile --target lib -o $(LD_DIR) .

## Reuse the library Logos Basecamp installed. Copies every shipped .so:
## liblogosdelivery dlopens libpq at runtime and that failure is fatal.
deps-basecamp:
	@test -f "$(BASECAMP_LIB)/liblogosdelivery.so" \
		|| { echo "no liblogosdelivery.so in $(BASECAMP_LIB) — set BASECAMP_LIB"; exit 1; }
	@mkdir -p $(LD_DIR)
	@cp $(BASECAMP_LIB)/*.so $(BASECAMP_LIB)/*.so.* $(LD_DIR)/ 2>/dev/null || true
	@if [ -n "$(HDR)" ]; then cp "$(HDR)" $(LD_DIR)/; fi
	@test -f "$(LD_DIR)/liblogosdelivery.h" \
		|| echo "note: no liblogosdelivery.h staged — set HDR=/path/to/liblogosdelivery.h"
	@echo "staged $$(ls $(LD_DIR) | wc -l) files in $(LD_DIR)"

check-lib:
	@test -f "$(LD_INC)/liblogosdelivery.h" \
		|| { echo "missing $(LD_INC)/liblogosdelivery.h — run 'make deps' or 'make deps-basecamp'"; exit 1; }
	@test -f "$(LD_LIB)/liblogosdelivery.so" \
		|| { echo "missing $(LD_LIB)/liblogosdelivery.so — run 'make deps' or 'make deps-basecamp'"; exit 1; }

## --- binaries ---

logos-vpn: check-lib
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/logos-vpn ./cmd/logos-vpn

wakuspike: check-lib
	$(GO) build -o bin/wakuspike ./cmd/wakuspike

s3topics: check-lib
	$(GO) build -o bin/s3topics ./cmd/s3topics

m0demo:
	$(GO) build -o bin/m0demo ./cmd/m0demo

## Portable distribution, built entirely in containers.
dist:
	./scripts/build-portable.sh

## --- spikes and milestones ---

## S1: cgo -> liblogosdelivery round trip over logos.dev
s1: wakuspike
	./bin/wakuspike -v

## Print what the library accepts, then exit
probe: wakuspike
	./bin/wakuspike -probe -v

## S3: rotating rendezvous topics must stay on one shard
s3: s3topics
	./scripts/check-s3.sh

## M0: two WireGuard peers sharing a socket with the control protocol.
## Needs no root — uses a netstack TUN.
m0: m0demo
	./bin/m0demo

## M1: two containerised nodes discover each other over logos.dev and tunnel.
## Needs docker and /dev/net/tun.
m1:
	./scripts/m1-containers.sh

## M2: two nodes behind separate NATs punch through to each other.
m2:
	./scripts/m2-containers.sh

## M2 under endpoint-dependent NAT, where punching is expected to fail and
## the relay (M3) is required.
m2-edm:
	NAT_MODE=edm ./scripts/m2-containers.sh

## --- checks ---

## Unit tests that do not need liblogosdelivery. This is what CI runs on every
## push; the cgo-bound packages are covered by the build and the m1 harness.
## CGO_LDFLAGS is cleared: these packages do not link liblogosdelivery, and
## inheriting -llogosdelivery would fail the link when the library is absent —
## which is exactly the situation in CI before the build job runs.
test-unit:
	CGO_CFLAGS= CGO_LDFLAGS= $(GO) test -race ./internal/identity/... \
		./internal/topic/... ./internal/control/... ./internal/wg/... \
		./internal/disco/... ./internal/relay/...

test: check-lib
	$(GO) test ./...

fmt:
	gofmt -w ./cmd ./internal

clean:
	rm -rf bin dist docker/build docker/run
