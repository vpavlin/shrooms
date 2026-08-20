# Making the data plane as private as the control plane

**Status:** design note, nothing built. Two decisions at the end.

The observation behind this, in Vaclav's words: we use Logos Delivery to get
privacy and independence for the control plane, and then give a lot of it back
on the data plane. That is correct, and worth stating precisely, because the
two halves of it fail differently.

## The premise, sharpened

**Independence is not lost on the data plane.** Tunnels are direct between your
own machines. No third party carries them, and no coordinator knows they exist.
If anything the data plane is *more* independent than the control plane, which
does depend on a public fleet being reachable.

**Privacy is lost, and specifically topology.** A direct tunnel means your IP
sends WireGuard-shaped packets to their IP. Anyone on the path — your ISP, their
ISP, anyone between — learns which of your machines talk to each other, when,
and how much. The control plane hides that behind a shared shard; the data plane
puts it on the wire in the clear.

This is not a new discovery. It is [ADR-011](adr/011-no-mixnet.md)'s central
argument, used there to *refuse* mixing the control plane:

> Mixing the *announcement* that site A is at IP X, while actual traffic to IP X
> is right there, is buying anonymity you immediately hand back.

The question here is the inverse one. ADR-011 asked "is control-plane mixing
worth it given a naked data plane?" and answered no. It never asked whether the
data plane could be covered instead.

## The insight that shapes every option

Hiding from the **network** and hiding from the **relay** are different problems,
and one relay cannot solve both.

| | who sees the topology |
|---|---|
| direct tunnel | anyone on the path |
| one relay | the relay operator |
| two relays, layered | neither operator; a path observer sees only hop 1 |

With a direct tunnel the pair is visible to the network. Route it through a
relay and the network sees only "you talked to the relay" — but the relay now
knows the pair, which is exactly the exposure
[blind-relays.md](blind-relays.md) catalogues.

Two relays, each knowing only one side, is the first arrangement where nobody
holds both halves. That is Tor's shape.

## Onion routing is not a mixnet

Worth separating, because ADR-011's refusal is often read as closing this door
and it does not.

A **mixnet** deliberately delays and batches messages, and needs constant cover
traffic, to break timing correlation. That is what makes it unusable for a VPN,
and ADR-011's measurements — no cover traffic wired up, 50 ms per hop — are why
Waku's mix buys nothing here.

**Onion routing** adds hops and layered encryption but no deliberate delay. Tor
is usable for interactive traffic; a mixnet is not. Two relay hops would cost
roughly what the second hop's round trip costs, not a mixing delay.

So "two blind relays, layered" sits at a different point on the curve than
anything ADR-011 rejected, and does not contradict it.

What it still does **not** buy: an observer who can watch both ends at once can
correlate volume and timing regardless. Only cover traffic fixes that, and cover
traffic is the cost that makes mixnets unusable. This defends against a local
observer — your ISP, the café, the employer — not a global one. That is Tor's
position too, and it is worth claiming honestly rather than overclaiming.

## The cheap half: nobody should have to run a relay

A separate problem, with a much smaller answer.

Today a new person needs a reachable machine, or a friend who has one, and has
to be told its address. That is the single hardest step in adopting this.

**The invite could carry it.** An invite response already hands over everything
else a device needs to function:

```go
type Response struct {
    NetworkKey []byte   `json:"nk"`
    AdminKeys  [][]byte `json:"admin_keys"`
    Credential []byte   `json:"cred,omitempty"`
    Suffix     string   `json:"suffix,omitempty"`
    ...
}
```

It is JSON with `omitempty`, sealed to the invitee — so adding a relay field is
backward compatible, invisible to anyone but the recipient, and needs no wire
break. Whoever invites you already knows a relay that works, because they are
using it.

That is a small change with most of the "easy to start" benefit, and it composes
with blind relays rather than depending on them: the relay handed over can be
one of yours today and somebody else's later.

## What "disappearing" would actually take

In order of cost, and each is useful alone:

1. **Stop shipping `mvpn` in the clear** — see
   [obfuscation.md](obfuscation.md). We are currently more identifiable than
   plain WireGuard.
2. **Invites carry a relay.** Removes the setup burden.
3. **Blind relays.** Strangers can run them; the derived relay key already makes
   this possible without handing over the network key.
4. **Relay-always as a mode.** Give up direct paths, and a path observer sees you
   talking to one address rather than to your peers. Costs latency and the
   performance premise of ADR-001, which is why it must be a mode and not a
   default.
5. **Two layered hops.** Neither relay knows both ends. Needs relay-to-relay
   forwarding, which does not exist.
6. **Obfuscation on top**, so the flow to the relay does not look like WireGuard.

Note what happens at step 4: if traffic always relays, hole punching stops
mattering. Much of the hardest code in this project exists to establish direct
paths, and the private mode would not use it. That is not an argument against —
it is an argument that this is a genuinely different mode, with different code
paths and a different performance story, rather than a setting.

## The fork in the road

Steps 4–6 change what this project is. Today it is Tailscale-shaped: direct
tunnels between your own machines, fast, with relays as a fallback. With
always-on layered relaying it becomes Tor-shaped: slower, private against a
local observer, dependent on volunteers carrying traffic.

Both are defensible. They are not the same product, and the second one has a
harder problem — [ADR-012](adr/012-relay-hosting.md) already notes that Tor has
a persistent relay shortage because there is no reciprocity to build on. A
network where everyone relays for everyone has an incentive story; one where a
few volunteers carry everybody does not.

The honest framing is that most users want mode 1 most of the time, and the
projects that have shipped both find that the fast mode is what people use.

## Two decisions

1. **Is "private mode" a goal, or is the goal to stop leaking gratuitously?**
   Steps 1–3 are worth doing on their own merits and cost almost nothing. Steps
   4–6 are a second product inside this one. It is possible to want only the
   first group.

2. **Do invites carry a relay?** This one is nearly free, is useful whatever the
   answer to the first question, and is the single biggest reduction in how hard
   this is to start using. It is the recommendation of this note regardless of
   everything else in it.
