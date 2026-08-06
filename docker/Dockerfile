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
        iputils-ping \
        libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

# liblogosdelivery.so links librln.so from the same directory.
COPY lib/ /opt/logos/lib/
ENV LD_LIBRARY_PATH=/opt/logos/lib

COPY logos-vpn /usr/bin/logos-vpn

# Config and state live on volumes so a container restart keeps its identity —
# and, importantly, its announce sequence number.
VOLUME ["/etc/logos-vpn", "/var/lib/logos-vpn"]

ENTRYPOINT ["/usr/bin/logos-vpn"]
CMD ["daemon", "-v"]
