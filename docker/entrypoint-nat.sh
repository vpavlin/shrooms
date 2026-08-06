#!/usr/bin/env bash
# Entrypoint for a node that lives behind a NAT gateway.
#
# Docker attaches the container to an internal-only network, so there is no
# default route out. Point the default route at the gateway container, then run
# the daemon normally.
set -euo pipefail

GW=${NAT_GW:?NAT_GW must be set to the gateway address}

echo "routing through NAT gateway $GW"
ip route del default 2>/dev/null || true
ip route add default via "$GW"

# Docker's embedded DNS lives on the bridge and is unreachable once the default
# route moves, so resolve via the gateway's upstream instead.
if ! grep -q '^nameserver 1.1.1.1' /etc/resolv.conf 2>/dev/null; then
    printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf
fi

echo "route:"; ip route show | sed 's/^/  /'

exec /usr/bin/logos-vpn "${@:-daemon}" -v
