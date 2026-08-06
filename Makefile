# logos-vpn
#
# Needs liblogosdelivery (shared lib) and a matching liblogosdelivery.h.
# Defaults point at the copy installed by Logos Basecamp plus the header
# vendored in logos-delivery-module — these two match.
#
# Override if you build liblogosdelivery yourself:
#   make LD_LIB=/path/to/build LD_INC=/path/to/library wakuspike

LD_LIB ?= $(HOME)/.local/share/Logos/LogosBasecamp/modules/delivery_module
LD_INC ?= $(HOME)/devel/github.com/logos-co/logos-workspace/repos/logos-modules/logos-delivery-module/vendor/logos-delivery/liblogosdelivery

export CGO_CFLAGS  += -I$(LD_INC)
export CGO_LDFLAGS += -L$(LD_LIB) -llogosdelivery -Wl,-rpath,$(LD_LIB)

GO ?= go

.PHONY: all check-lib wakuspike s1 s3 probe m0 test clean

all: wakuspike s3topics m0demo

check-lib:
	@test -f "$(LD_INC)/liblogosdelivery.h" \
		|| { echo "missing $(LD_INC)/liblogosdelivery.h — set LD_INC"; exit 1; }
	@test -f "$(LD_LIB)/liblogosdelivery.so" \
		|| { echo "missing $(LD_LIB)/liblogosdelivery.so — set LD_LIB"; exit 1; }
	@echo "lib: $(LD_LIB)/liblogosdelivery.so"
	@echo "hdr: $(LD_INC)/liblogosdelivery.h"

## Spike S1: cgo -> liblogosdelivery round trip
wakuspike: check-lib
	$(GO) build -o bin/wakuspike ./cmd/wakuspike

## Print what the library accepts, then exit — no network round trip
probe: wakuspike
	./bin/wakuspike -probe -v

## Run S1 against logos.dev (cluster 2)
s1: wakuspike
	./bin/wakuspike -v

## Spike S3: rotating rendezvous topics must stay on one shard
s3topics: check-lib
	$(GO) build -o bin/s3topics ./cmd/s3topics

s3: s3topics
	./scripts/check-s3.sh

## Milestone M0: two userspace WireGuard peers sharing a socket with the
## control protocol. Needs no root — uses a netstack TUN.
m0demo:
	$(GO) build -o bin/m0demo ./cmd/m0demo

m0: m0demo
	./bin/m0demo

test: check-lib
	$(GO) test ./...

clean:
	rm -rf bin
