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

An earlier draft of this note recommended that invites carry a relay address,
and called it "the single biggest reduction in how hard this is to start using".
That was wrong, and Vaclav caught it: **a newly invited device discovers relays
by itself.**

It does. `selectRelay` walks the roster for any peer that announced
`Relay: true`, is online, and has a probed path:

```go
for _, p := range m.roster.Peers() {
    if !p.Relay || !p.Online(now) {
        continue
    }
    path, ok := m.prober.Best(p.ID(), now)
    ...
}
```

Every one of those a new device gets on its own. The cost is one announce
interval — 45s — plus a probe, which it is already spending on peer discovery
anyway. So for a mesh whose relay is one of its own members, carrying the
address in the invite saves under a minute of a wait that is happening
regardless. That is not a feature worth a wire field.

**Where it does matter is precisely the blind-relay case**, and only there. A
blind relay is not a member: it holds the derived relay key and not the network
key, so it cannot encrypt an announce, and nothing it says would verify if it
could. It can never appear in a roster, so it can never be discovered. It has to
be configured — and the invite is the one channel that already carries
configuration to a new device, sealed to that device.

So this is not an onboarding improvement that stands alone. It is a dependency
of blind relays, and it should be built when they are, if they are.

There is one residual argument for it that is worth recording and not
overselling: discovery runs over the rendezvous plane, so when rendezvous
stalls — which happens — a node cannot learn about a relay it has never seen. A
pinned relay keeps working. That is a robustness point rather than an
onboarding one, and it is small.

## What "disappearing" would actually take

In order of cost, and each is useful alone:

1. **Stop shipping `mvpn` in the clear** — see
   [obfuscation.md](obfuscation.md). We are currently more identifiable than
   plain WireGuard.
2. **Blind relays.** Strangers can run them; the derived relay key already
   makes this possible without handing over the network key. This is the step
   that removes the setup burden — not invite-carried addresses, which only
   matter as a way to reach a relay that cannot announce itself.
3. **Relay-always as a mode.** Give up direct paths, and a path observer sees you
   talking to one address rather than to your peers. Costs latency and the
   performance premise of ADR-001, which is why it must be a mode and not a
   default.
4. **Two layered hops.** Neither relay knows both ends. Needs relay-to-relay
   forwarding, which does not exist.
5. **Obfuscation on top**, so the flow to the relay does not look like WireGuard.

Note what happens at step 3: if traffic always relays, hole punching stops
mattering. Much of the hardest code in this project exists to establish direct
paths, and the private mode would not use it. That is not an argument against —
it is an argument that this is a genuinely different mode, with different code
paths and a different performance story, rather than a setting.

## The fork in the road

Steps 3–5 change what this project is. Today it is Tailscale-shaped: direct
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
   Steps 1–2 are worth doing on their own merits and cost almost nothing. Steps
   3–5 are a second product inside this one. It is possible to want only the
   first group.

2. ~~Do invites carry a relay?~~ **Answered: only alongside blind relays.**
   A member relay is discovered without help in about a minute. Carrying an
   address only matters for a relay that cannot announce itself, which is a
   blind one — so this is part of that feature rather than a standalone win.
