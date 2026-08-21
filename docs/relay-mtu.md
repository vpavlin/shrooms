# A relayed packet does not fit

**Status:** fixed by clamping TCP segments, and the constant it relies on is a
measured guess rather than a derived value. See *Fixed* below.

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

## This is not about blind relays

The overhead is the relay protocol, not who runs the relay. A relay that is a
member of your mesh adds exactly the same 86 bytes, so a phone relayed through
your own VPS hits the same wall. Blind relays are simply where it surfaced,
because they prompted the first deliberate bulk transfer over a relay.

It was worse before this was found. At the previous tunnel MTU of 1420 a relayed
packet was 1566 bytes on the wire — over the 1500 an ordinary Ethernet path
carries — so **large relayed transfers failed on every path**, including through
a relay of your own on a good network. Nobody noticed because bulk transfer over
a relay is not a thing people do on purpose.

| tunnel MTU | relayed, on the wire | 1500 path | a phone's ~1250 path |
|---|---|---|---|
| 1420 (before) | 1566 | no | no |
| 1280 (now) | 1426 | yes | no |
| with path MTU discovery | adapts | yes | yes |

So dropping to 1280 fixed the ordinary case and left the one this was found on.

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

## Fixed, and how it was measured

TCP segments are now clamped for peers reached through a relay, in both
directions — see `internal/wg/mss.go`. A 100MB transfer between two containers
on isolated networks, forced through a relay on Akash, completes:

```
OK 104.9 MB in 2m27s (0.71 MB/s)
```

Three things had to be right, and the first two attempts each got one wrong.

**Path MTU discovery cannot express it.** RFC 8201 forbids a node reducing its
path MTU below 1280, so a host told "packet too big, use 1134" raises it back
and starts adding Fragment headers. A TCP segment size has no such floor, which
is why clamping works where the honest signal does not.

**The clamp has to be reached.** The first version installed itself by asserting
the tun was a particular type; the daemon's tun is a v4 translator wrapping a
real device, so the assertion failed, the clamp was installed on nothing, and
every unit test still passed. It now wraps whatever `NewDevice` is given.

**The constant was wrong, and only measurement found it.** 1280 looked
inarguable — it is the IPv6 minimum, so nothing may carry less. Probing a relay
on Akash put the real limit at about 1265 bytes on the wire: hosted networks
stack their own encapsulation underneath, and an overlay eating twenty bytes
below the IPv6 floor is apparently ordinary. `SafeUnderlay` is now 1200, which
is a guess with margin rather than a derived value.

**Which is why the constant is not the answer.** A client already exchanges
registration frames with its relay and could pad one to find the largest that
survives — real path MTU discovery on the one hop that needs it. Until then this
is deliberately pessimistic, and pays for it in throughput.

## Until then

Small transfers work. Interactive traffic works. Large TCP transfers over a
relay stall, and the symptom is silence rather than an error — so it is worth
knowing that a stalled download over a relayed peer is this and not the relay
being slow or overloaded.

Direct peers are unaffected: 1280 is comfortably under any ordinary path.
