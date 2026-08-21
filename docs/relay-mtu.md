# A relayed packet does not fit

**Status:** measured, not fixed. Large transfers over a relay stall.

A 100MB download from a container to a phone, both behind NAT and talking
through a blind relay, stopped after 1.5KB. Nothing reported an error: the
tunnel was up, the relay was forwarding, and the transfer simply stopped.

## What is happening

A relayed packet carries more than a direct one:

| | bytes |
|---|---|
| tunnel payload | 1280 |
| WireGuard transport header and tag | 32 |
| control header, so relay frames share the WireGuard socket | 5 |
| relay forward header — type, destination, source, MAC | 81 |
| UDP | 8 |
| IPv4 underlay | 20 |
| **on the wire** | **1426** |

The path to a phone on mobile data commonly carries about 1250. So the
handshake fits, the HTTP request fits, small responses fit — and the first
full-size data packet does not.

Measured by pinging the far end at increasing sizes through the relay:

```
tunnel packet 1098B  → arrives
tunnel packet 1128B  → does not
```

## Why lowering the MTU does not fix it

The overlay is IPv6, and **1280 is the IPv6 minimum**. The kernel refuses to put
an IPv6 address on an interface below it:

```
ip address add fd58:… dev shrooms0
RTNETLINK answers: Invalid argument
```

So the smallest legal overlay packet is already 1426 bytes on the wire. There is
no MTU that satisfies both the floor and the path. This is not a constant that
was chosen badly; it is a constant that cannot be chosen.

## What would fix it

**Path MTU discovery, per peer.** When a packet will not fit the path a peer is
currently reached by, drop it and emit an ICMPv6 Packet Too Big back to the
local sender. The sending stack caches a smaller path MTU for that destination
and TCP adjusts within a round trip; UDP applications that care are told the
same way.

That is the right shape rather than merely a workaround:

- It is **per destination**, so a peer on the LAN keeps full size while a peer
  through a relay does not. Which peers are relayed changes at runtime, and an
  interface MTU cannot express that.
- It needs no negotiation and no wire change — ICMPv6 Packet Too Big is what
  every stack already listens for.
- It degrades honestly. Today the failure is a transfer that stops with no error
  anywhere, which is the worst kind.

**Reducing the relay header would help and is not sufficient.** The forward
header spends 64 of its 81 bytes on two full 32-byte handles. Short session ids
agreed at registration would save around 56, bringing a relayed packet to about
1370 on the wire — still above the ~1250 measured here. Worth doing eventually
for the bandwidth, not as a fix for this.

## Until then

Small transfers work. Interactive traffic works. Large TCP transfers over a
relay stall, and the symptom is silence rather than an error — so it is worth
knowing that a stalled download over a relayed peer is this and not the relay
being slow or overloaded.

Direct peers are unaffected: 1280 is comfortably under any ordinary path.
