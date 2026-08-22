# Two nodes announcing one address

**Status:** observed and diagnosed, not fixed.

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

## What would fix it

Three candidates, in increasing order of how much they actually address it:

- **Do not announce a candidate a peer already announces.** The roster is right
  there and the check is cheap. It cannot decide *which* node is wrong, but two
  nodes claiming one address means at most one is right, and announcing it to
  everyone is worse than announcing nothing. Cheap, partial, and would stop the
  flapping.
- **Remember a candidate that answered as the wrong device.** The prober already
  rejects the reply; it does not remember that this address answers as somebody
  else, so it retries on a timer forever. Recording it would stop a node
  repeatedly offering a path that has demonstrably never worked.
- **Distinguish reflexive addresses learned from one peer from those confirmed
  by several.** An address seen by two peers on different networks is a real
  public address; one seen by a single peer may be a per-destination mapping.
  This is the honest fix and the largest.

## Meanwhile

The behaviour is safe. Traffic flows, the relay carries it when the direct path
cannot be verified, and nothing is misdelivered — the signature check is what
makes the failure a fallback rather than a hijack.

The cost is throughput and latency on a path that should be direct, and a
`status` output that looks wrong to anybody who knows pi5 is a relay.
