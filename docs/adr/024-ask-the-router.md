# 024. Ask the router for a way in

**Status:** accepted; `internal/portmap` built, wiring in progress

## Context

A node behind NAT learns its own public address by *reflection*: it probes a
peer, and the pong echoes back the address that peer observed
(`disco.Prober.HandlePong` records `m.Observed`). That is enough for the common
case and it costs nothing, because the probes are being sent anyway.

It has one requirement, and the requirement is invisible until it bites: the
peer that answers must be **outside your NAT**. On a mesh where every member is
behind the same router, no node can learn its public address at all. Every
announced candidate is a LAN address, every probe from outside is aimed at
`192.168.x.y`, and the mesh works perfectly from the sofa and not at all from
the street.

That is not a corner case. It is what a second mesh looks like on the day it is
created — a NAS and a laptop in one house — and the first time anyone leaves the
house with a phone, nothing connects. The failure reads as "the VPN is broken",
not as "no member of this mesh has ever been told its own address".

The existing answer is `advertise`, a config line naming a public
`host:port`. It works, and it requires knowing your public IP, knowing your
listen port, and having already set up a forwarding rule in a router
administration page. Three pieces of knowledge to make a home node reachable, in
a project whose whole claim is that you should not need to run infrastructure.

Meanwhile the router knows the answer, and there are three standard ways to ask
it.

## Decision

**Ask the router at startup: PCP, then NAT-PMP, then UPnP-IGD. Treat whatever
comes back as one more candidate.**

### Why asking beats every other way of finding out

The alternatives all get you an address; only this one gets you a *way in*.

| | address | inbound mapping | third party |
|---|---|---|---|
| Reflection from a peer | yes, if one is outside | no | no |
| PCP / NAT-PMP / UPnP | yes | **yes** | no |
| STUN | yes | no | **yes** |
| Observed address from the delivery layer | IP only | no | no |

A public address without a mapping is only useful when the NAT already happens
to forward — which is exactly the situation `advertise` covers, and it is the
situation people have to construct by hand. Port mapping constructs it.

STUN would work and is deliberately not used: it means depending on somebody
else's server for a thing the router can answer, in a project that went to some
trouble to have no coordination server.

### Why a wrong answer is cheap

This is the property that makes the whole thing safe to attempt: **every
candidate is probed before it is used** (ADR-009). An address obtained from a
lying or confused router is a candidate that fails to probe and is discarded,
exactly like a stale LAN address. Nothing sets an endpoint on the strength of a
router's word.

So there is no need to be careful about *whether* to believe the mapping. The
code can ask, announce what it is told, and let the prober decide — which also
means the fallback ordering below costs nothing when an earlier protocol
answers wrongly rather than not at all.

### Order, and why

**PCP first** (RFC 6887). Newer, and the one that behaves correctly when the
router itself is behind another NAT: it reports the outermost external address
rather than the address of the first hop.

**NAT-PMP second** (RFC 6886). Same port, same shape, much older, still what a
large number of consumer routers actually implement. Roughly forty lines to
support once the socket is open.

**UPnP-IGD last** (SSDP discovery, then SOAP over HTTP). Ugly — an XML dialect
and a device-description fetch — and the most widely enabled of the three on
domestic hardware. Last because it is the largest amount of code for a marginal
increase in coverage, so it can be added after the two cheap ones prove the
idea.

**All of them are best-effort.** A router that answers none of them leaves the
node exactly where it is today, with `advertise` available for the person who
wants to configure it by hand. Nothing regresses.

### What it does not do

**Carrier-grade NAT cannot be mapped.** A phone on mobile data sits behind a NAT
belonging to the carrier; PCP requests reach the handset's own gateway at best,
and there is nothing to open. This is not a shortcoming of the mechanism — a
device with no inbound path has no inbound path — and it is why relays exist. A
mesh needs one reachable member, not one per node.

**A mapping is a lease, not a fact.** All three protocols hand out a lifetime,
and a router that reboots forgets everything. So the mapping is renewed on a
timer at half its lifetime, the same soft-state discipline the relay
registration already uses, and a lapse degrades to what happens today rather
than to a broken node.

**It opens a port on your router.** Worth stating plainly: this is a device on
the LAN asking to be reachable from the internet, which is precisely what the
protocols are for and precisely what makes some people disable them. The port
carries WireGuard, which answers nothing without a valid handshake, and the
behaviour is off by nothing — a node that would rather not ask can turn it off.

## Consequences

- A home node becomes an anchor for its mesh without anyone opening a router
  page: it learns its address, opens its port, and announces the result.
- `advertise` goes back to being what its config comment says it is — a thing
  for unusual setups — rather than something you must discover you need.
- Reflection stays. It is free, it works whenever a public peer exists, and the
  two together cover more than either alone: the router answers when there is no
  outside peer, the outside peer answers when the router will not talk.
- One more thing that can fail silently, so it is logged at startup with which
  protocol answered, and the resulting address appears in `shrooms status` as an
  ordinary candidate rather than as a special case.
- No new dependency. All three are wire protocols we can speak with the standard
  library, and the first two are small enough that adding them is smaller than
  the argument about whether to.
