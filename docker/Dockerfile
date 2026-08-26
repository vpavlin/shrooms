# Test harness for running mesh nodes in isolated network namespaces.
#
# The base matches the development host (Ubuntu 25.10 / glibc 2.42) because we
# copy in a host-built binary and the prebuilt liblogosdelivery.so. If you want
# a portable image, build the Go binary in a builder stage instead — that needs
# the liblogosdelivery header as well as the library.
FROM ubuntu:25.10

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        iproute2 \
        iptables \
        iputils-ping \
        libstdc++6 \
        procps \
        # For a binary built with -tags pcsc, which reaches a smartcard reader
        # through pcscd. The daemon never touches a card; the admin commands do,
        # and running them inside a node is how a real deployment would. Costs
        # about 60 KB and nothing at all when no socket is mounted.
        libpcsclite1 \
    && rm -rf /var/lib/apt/lists/*

# liblogosdelivery.so links librln.so from the same directory.
COPY lib/ /opt/logos/lib/
ENV LD_LIBRARY_PATH=/opt/logos/lib

COPY shrooms /usr/bin/shrooms

# NAT-topology helpers, used by compose-nat.yml (M2).
COPY gateway.sh entrypoint-nat.sh /usr/bin/

# Config and state live on volumes so a container restart keeps its identity —
# and, importantly, its announce sequence number.
VOLUME ["/etc/shrooms", "/var/lib/shrooms"]

# The library resolves storeMessageDbUrl — "sqlite://store.sqlite3" by default —
# against the working directory, which was "/" and is not somewhere anything
# should be writing. Pointed at the state volume instead: writable, persistent,
# and already ours.
WORKDIR /var/lib/shrooms

ENTRYPOINT ["/usr/bin/shrooms"]
CMD ["daemon", "-v"]
