#!/usr/bin/env bash
# NAT gateway for the M2 topology.
#
# Masquerades traffic from the internal LAN out to the public segment, giving
# nodes behind it exactly what a home router gives you: outbound works, inbound
# does not, and the external port is chosen by the NAT.
#
# NAT_MODE controls the mapping behaviour, which is what decides whether hole
# punching can work at all (RFC 4787):
#
#   eim  (default) endpoint-independent mapping — one external port per internal
#                  socket regardless of destination. The punchable case, and
#                  what <2% of home routers deviate from.
#   edm            endpoint-dependent ("symmetric") — a different external port
#                  per destination, so a port learned from one peer is useless
#                  to another. ~40% of cellular CGNATs behave this way, and it
#                  is where punching fails and the relay has to take over.
set -euo pipefail

MODE=${NAT_MODE:-eim}
WAN=${WAN_IF:-}
LAN=${LAN_IF:-}

echo "gateway starting: mode=$MODE"

# Verified, not assumed. podman mounts /proc/sys read-only, so this call is
# ignored with a warning on stderr and the gateway then forwards nothing —
# while still printing "gateway ready". The containers behind it lose all
# connectivity, which presents as a mesh failure a long way from the cause.
sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null)" != "1" ]; then
    echo "FATAL: could not enable net.ipv4.ip_forward" >&2
    echo "  /proc/sys is read-only in this container. Start it with:" >&2
    echo "    --sysctl net.ipv4.ip_forward=1" >&2
    echo "  (podman needs this; docker usually permits the write directly)" >&2
    exit 1
fi

# Identify interfaces by role rather than trusting device order, which docker
# does not guarantee.
#
# WAN is whichever interface carries the default route: docker gives one to a
# bridge with internet access and none to an `internal` network, which is
# exactly the split we want. LAN is the other one.
#
# This used to match on hardcoded subnets (10.90/10.91/10.92), which meant any
# topology other than the M2 one silently got the wrong interface — the gateway
# would come up, masquerade the wrong direction, and present as a network
# failure rather than a configuration error.
if [ -z "$WAN" ]; then
    WAN=$(ip -4 route show default | awk '{print $5}' | head -1)
fi
[ -n "$WAN" ] || { echo "no default route: cannot identify the WAN interface"; exit 1; }

if [ -z "$LAN" ]; then
    for ifc in $(ls /sys/class/net | grep -v lo); do
        [ "$ifc" = "$WAN" ] && continue
        LAN=$ifc
        break
    done
fi
[ -n "$LAN" ] || { echo "no second interface: cannot identify the LAN interface"; exit 1; }
echo "resolved: wan=$WAN lan=$LAN"

WAN_IP=$(ip -4 -o addr show dev "$WAN" | awk '{print $4}' | cut -d/ -f1)
WG_PORT=${WG_PORT:-51820}

if [ "$MODE" = "eim" ]; then
    # Endpoint-independent mapping: pin the external port so every destination
    # sees the same ip:port. This needs an explicit SNAT.
    #
    # Plain MASQUERADE is NOT endpoint-independent — measured here, it allocates
    # a different external port per destination, i.e. it behaves as symmetric
    # NAT. A traversal harness built on bare MASQUERADE therefore tests the hard
    # case while claiming to test the easy one.
    iptables -t nat -A POSTROUTING -o "$WAN" -p udp --sport "$WG_PORT" \
        -j SNAT --to-source "$WAN_IP:$WG_PORT"
    iptables -t nat -A POSTROUTING -o "$WAN" -j MASQUERADE
else
    # Endpoint-dependent: a different external port per flow, so a port learned
    # by one peer is useless to another. ~40% of cellular CGNATs behave this way
    # and it is where punching fails and the relay must take over.
    iptables -t nat -A POSTROUTING -o "$WAN" -j MASQUERADE --random-fully
fi

# Drop unsolicited inbound, keeping established flows. Without this the
# gateway forwards freely and there is no NAT to traverse.
iptables -A FORWARD -i "$WAN" -o "$LAN" -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A FORWARD -i "$WAN" -o "$LAN" -j DROP
iptables -A FORWARD -i "$LAN" -o "$WAN" -j ACCEPT

echo "gateway ready"
ip -4 -o addr show | sed 's/^/  /'
iptables -t nat -L POSTROUTING -n --line-numbers | sed 's/^/  /'

# Stay alive; the nodes route through this container.
sleep infinity
