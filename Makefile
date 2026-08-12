# shrooms
#
# Building needs liblogosdelivery (shared library + matching header). There is
# no canonical place for it, so pick one:
#
#   make deps-release         download a prebuilt copy (works anywhere) <- use this
#   make deps-basecamp        reuse the copy Logos Basecamp installed
#   make deps                 build from source in a container (blocked upstream)
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

.PHONY: all deps deps-basecamp check-lib shrooms wakuspike s3topics m0demo \
        s1 s3 probe m0 m1 m2 m2-edm m3 m3-remote dist image push-image deps-release install uninstall build-all vet-cgo test test-unit android-deps android-core aar apk fdroid basecamp-check basecamp-lgx fmt clean

all: shrooms

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
		|| { echo "missing $(LD_INC)/liblogosdelivery.h — run 'make deps-release'"; exit 1; }
	@test -f "$(LD_LIB)/liblogosdelivery.so" \
		|| { echo "missing $(LD_LIB)/liblogosdelivery.so — run 'make deps-release'"; exit 1; }

## --- binaries ---

shrooms: check-lib
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/shrooms ./cmd/shrooms

wakuspike: check-lib
	$(GO) build -o bin/wakuspike ./cmd/wakuspike

s3topics: check-lib
	$(GO) build -o bin/s3topics ./cmd/s3topics

m0demo:
	$(GO) build -o bin/m0demo ./cmd/m0demo

## Portable distribution, built entirely in containers.
dist:
	./scripts/build-portable.sh

## --- container image ---
##
## The image is how this gets deployed: liblogosdelivery needs glibc 2.38 and
## exists only where Logos Basecamp is installed, so a machine without it can
## neither build nor run a bare binary. Publish once from a machine that has the
## library; every other machine pulls.

IMAGE ?= ghcr.io/vpavlin/shrooms
TAG   ?= latest

image: shrooms check-lib
	@rm -rf docker/build/ctx && mkdir -p docker/build/ctx/lib
	@cp bin/shrooms docker/build/ctx/
	@cp docker/gateway.sh docker/entrypoint-nat.sh docker/build/ctx/
	@cp $(LD_LIB)/*.so $(LD_LIB)/*.so.* docker/build/ctx/lib/ 2>/dev/null || true
	docker build -t $(IMAGE):$(TAG) -f docker/Dockerfile docker/build/ctx
	@echo "built $(IMAGE):$(TAG)"

push-image: image
	docker push $(IMAGE):$(TAG)
	@echo "pushed $(IMAGE):$(TAG) — other machines need only 'docker pull'"

## Fetch a prebuilt liblogosdelivery from this repo's releases, so a machine
## without Logos Basecamp can still build.
deps-release:
	./scripts/fetch-lib.sh

## --- install ---

PREFIX  ?= /usr/local
LIBDIR  ?= $(PREFIX)/lib/shrooms

## Install the binary, its libraries and the systemd unit.
##
## The binary is relinked with an rpath pointing at the installed library
## directory, so it does not depend on this checkout still existing.
install: check-lib
	install -d $(DESTDIR)$(LIBDIR) $(DESTDIR)$(PREFIX)/bin
	install -m 0644 $(LD_LIB)/*.so $(DESTDIR)$(LIBDIR)/
	@cp $(LD_LIB)/*.so.* $(DESTDIR)$(LIBDIR)/ 2>/dev/null || true
	CGO_LDFLAGS="-L$(abspath $(LD_LIB)) -llogosdelivery -Wl,-rpath,$(LIBDIR)" 		$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" 		-o $(DESTDIR)$(PREFIX)/bin/shrooms ./cmd/shrooms
	install -d $(DESTDIR)/etc/systemd/system
	install -m 0644 packaging/shrooms.service $(DESTDIR)/etc/systemd/system/
	install -d $(DESTDIR)$(PREFIX)/share/bash-completion/completions
	install -m 0644 packaging/shrooms.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/shrooms
	@echo
	@echo "installed:"
	@echo "  $(PREFIX)/bin/shrooms"
	@echo "  $(LIBDIR)/"
	@echo "  /etc/systemd/system/shrooms.service"
	@echo "  $(PREFIX)/share/bash-completion/completions/shrooms"
	@echo
	@echo "next:"
	@echo "  sudo shrooms init --relay --name $$(hostname)   # or: join <KEY>"
	@echo "  sudo systemctl daemon-reload"
	@echo "  sudo systemctl enable --now shrooms"
	@echo
	@echo "completion applies to new shells; for this one:"
	@echo "  source $(PREFIX)/share/bash-completion/completions/shrooms"

uninstall:
	systemctl disable --now shrooms 2>/dev/null || true
	rm -f $(DESTDIR)$(PREFIX)/bin/shrooms $(DESTDIR)/etc/systemd/system/shrooms.service \
	      $(DESTDIR)$(PREFIX)/share/bash-completion/completions/shrooms
	rm -rf $(DESTDIR)$(LIBDIR)
	@echo "removed the binary, libraries and unit."
	@echo "config and identity are left alone:"
	@echo "  /etc/shrooms  /var/lib/shrooms"

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

## M3: two NATed nodes carry traffic through a relay. Does not depend on
## punching, so it is unaffected by the MASQUERADE mapping problem.
m3:
	RELAY=1 ./scripts/m2-containers.sh

## M3 over the real internet: run a NATed node on a remote relay host and
## measure the tunnel from here. Containers prove the mechanism; only a real
## path proves the system. Needs a deployed relay: make m3-remote HOST=user@vps
## Depends on shrooms: the script ships this binary to the remote host and
## also queries the local daemon with it. Running a stale bin/ against fresh
## sources has produced two confusing failures already.
m3-remote: shrooms
	@[ -n "$(HOST)" ] || { echo "usage: make m3-remote HOST=user@vps"; exit 1; }
	./scripts/m3-remote.sh $(HOST) $(M3_ARGS)

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
		./internal/disco/... ./internal/relay/... ./internal/state/... \
		./internal/hosts/... ./internal/dns/... ./internal/service/... \
		./internal/cred/... ./internal/invite/... ./internal/portmap/...

## --- android ---

NDK      ?= $(HOME)/Android/Sdk/ndk/android-ndk-r27c
ANDROID_API ?= 24
ANDROID_CC  = $(NDK)/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android$(ANDROID_API)-clang
ANDROID_LIB = $(abspath android/libs)

## Fetch the arm64 liblogosdelivery. arm64 only: there is no x86_64 build, so
## an emulator has no node.
android-deps:
	./scripts/fetch-android-lib.sh

## Cross-compile the core for android/arm64. This is the check that matters —
## if the cgo core does not link here, no amount of Kotlin helps.
android-core: android-deps
	@test -x "$(ANDROID_CC)" || { echo "no NDK clang at $(ANDROID_CC); set NDK="; exit 1; }
	GOOS=android GOARCH=arm64 CGO_ENABLED=1 		CC="$(ANDROID_CC)" 		CGO_CFLAGS="-I$(ANDROID_LIB)" 		CGO_LDFLAGS="-L$(ANDROID_LIB)/arm64-v8a -llogosdelivery" 		$(GO) build ./internal/...
	@# The binding is a separate module (see mobile/go.mod), so it is built
	@# from inside it rather than by a ./... pattern from here.
	cd mobile && GOOS=android GOARCH=arm64 CGO_ENABLED=1 		CC="$(ANDROID_CC)" 		CGO_CFLAGS="-I$(ANDROID_LIB)" 		CGO_LDFLAGS="-L$(ANDROID_LIB)/arm64-v8a -llogosdelivery" 		$(GO) build ./...
	@echo "core and mobile binding build for android/arm64"

## Load the Basecamp view offscreen and assert it reads a status snapshot.
## There is no display here, but the QML runtime does not need one — and "the
## QML looks right" is not a test.
basecamp-check:
	./basecamp/test/check.sh

## Build the Basecamp module as a portable LGX, the installable package format.
##
## `lgx-portable`, not `lgx`: the plain one can reference paths in the nix store
## of the machine that built it, which is fine for a dev loop and useless as a
## download. Needs nix with flakes; CI builds this on every push.
basecamp-lgx:
	nix build ./basecamp#lgx-portable --print-build-logs
	@find -L result -name '*.lgx' -exec ls -lL {} \;

## Build the .aar for the Android app. Container-based: gomobile needs a JDK
## and Go >= 1.25, which the core deliberately does not.
aar: android-deps
	./scripts/build-aar.sh

## Build the Android app to an installable APK.
apk: aar
	./scripts/build-apk.sh

## Sign the release APK with the F-Droid repo key and publish it to the LAN
## repo. The key stays on that host; this builds unsigned and signs there.
fdroid:
	./scripts/publish-fdroid.sh

## Build every package. test-unit skips the cgo-bound ones, so without this an
## API change can break a command without any check noticing — which is exactly
## what happened to cmd/m0demo when the control-plane signature changed.
build-all: check-lib
	$(GO) build ./...
	@# The mobile module is nested, so ./... does not reach it — which is how a
	@# change to a struct it reads got as far as an F-Droid publish before
	@# anything complained.
	cd mobile && $(GO) build ./...

## Vet the packages that link liblogosdelivery. The no-cgo vet in CI cannot
## reach these, so without it internal/mesh is never vetted at all.
##
## internal/waku is excluded: it passes a cgo.Handle as the void* userData the
## library's callback signature demands, which vet flags as unsafe.Pointer
## misuse. That is the common idiom for FFI userdata and is safe here (a Handle
## is a map key, not an address), but silencing it properly means reshaping the
## bridge to pass uintptr_t. Left alone rather than blanket-disabling vet.
vet-cgo: check-lib
	$(GO) vet ./internal/mesh/... ./cmd/...

## -race, because the failure this caught was invisible without it: a counter
## incremented on the dispatch path while a read lock was held, which CI found
## and a plain `make test` had been passing over for a day. The whole suite
## takes about twenty seconds with it, which is not a reason to run a weaker
## check than the one that gates a push.
test: check-lib
	$(GO) test -race ./...

fmt:
	gofmt -w ./cmd ./internal

clean:
	rm -rf bin dist docker/build docker/run
