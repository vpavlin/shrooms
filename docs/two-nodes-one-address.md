# Two nodes announcing one address

**Status:** diagnosed, and the announcing half is fixed. A node no longer
advertises a reflexive address its peers disagree about.

A phone on mobile data shows pi5 as reached *through a relay*, and switches
between that and a direct path every few minutes. pi5 is itself a relay and
publicly reachable, so being relayed to looks obviously wrong.

It is not wrong. It is the correct response to something else being wrong.

## What is happening

Both machines sit behind one home NAT, and both announce the same external
address:

```
laptop announces:  178.213.45.235:51821   192.168.0.151:51821  …
pi5 announces:     178.213.45.235:51821   192.168.0.209:51820  …
```

Only one device can be behind that address and port at a time. So when the phone
probes it expecting pi5 and the laptop answers, the reply is signed by the
laptop's device key, the prober rejects it — correctly, that is exactly what the
signature is for — and there is no verified direct path. The peer falls back to
a relay, which is what a peer with no verified path is supposed to do.

When the mapping genuinely is pi5's, the probe succeeds and the path goes
direct. Hence the switching.

## Why pi5 claims an address that is not reliably its own

Not from its own port mapping: pi5 listens on 51820 and the laptop on 51821. It
learned `:51821` by **reflection** — a peer observed pi5's traffic arriving from
that address, and reflection is how a node behind NAT discovers its public
address at all ([ADR-009](adr/009-probe-before-use.md)).

The trap is that on a symmetric NAT the mapping is *per destination*. The
address a peer observed is real for that peer's flow and worthless to anybody
else, but it is announced to everybody. The router reusing a port another host
already maps is ordinary behaviour for outbound connections and makes the
collision look deliberate.

## The fix

**Ask whether the peers agree.** The prober now records *which* peer reported
each reflexive address, not merely when. That turns a guess into a measurement:

- Several peers reporting the **same** address means the NAT maps this socket
  the same way for every destination. The address is genuinely ours, and it is
  announced ahead of anything less corroborated.
- Peers reporting **different** addresses means the NAT maps per destination.
  None of those addresses works for anybody but its observer, so none is
  announced. That is the case here, and suppressing them is what stops the
  flapping.
- A **single** observer is not a disagreement. A two-node mesh has only one
  vantage point, so a lone observation is kept — unverified, but no worse than
  having no candidate at all, and without it a pair of NATed nodes could never
  find each other directly.

This is the STUN test for NAT type, done with the peers already present instead
of a server. No new messages and no wire change: the pong already carries the
observation, and the only thing missing was remembering who sent it.

### What it does not fix

A node that has no corroborated address now announces fewer candidates, which
is honest but not the same as being reachable. pi5 announces no mapping of its
own — it listens on 51820 and nothing advertises `178.213.45.235:51820`, though
the laptop reaches it there. Whatever forwards that port, pi5 does not know
about it, so it cannot tell anybody. Asking the router (ADR-024) is what would
close that, and pi5's build has the code; why it has no mapping is unanswered.

## Meanwhile

The behaviour is safe. Traffic flows, the relay carries it when the direct path
cannot be verified, and nothing is misdelivered — the signature check is what
makes the failure a fallback rather than a hijack.

The cost is throughput and latency on a path that should be direct, and a
`status` output that looks wrong to anybody who knows pi5 is a relay.
