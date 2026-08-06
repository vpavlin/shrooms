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
WAN=${WAN_IF:-eth0}
LAN=${LAN_IF:-eth1}

echo "gateway starting: mode=$MODE wan=$WAN lan=$LAN"

sysctl -w net.ipv4.ip_forward=1 >/dev/null

# Identify interfaces by subnet rather than trusting device order, which docker
# does not guarantee.
for ifc in $(ls /sys/class/net | grep -v lo); do
    addr=$(ip -4 -o addr show dev "$ifc" | awk '{print $4}' | head -1)
    case "$addr" in
        10.90.*) WAN=$ifc ;;
        10.91.*|10.92.*) LAN=$ifc ;;
    esac
done
echo "resolved: wan=$WAN lan=$LAN"

if [ "$MODE" = "edm" ]; then
    # --random-fully makes the source port unpredictable per flow, approximating
    # endpoint-dependent mapping: a port observed by one peer does not predict
    # the port another peer will see.
    iptables -t nat -A POSTROUTING -o "$WAN" -j MASQUERADE --random-fully
else
    iptables -t nat -A POSTROUTING -o "$WAN" -j MASQUERADE
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
