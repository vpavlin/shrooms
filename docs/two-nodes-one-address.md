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

The router gave it one. Guessed at reflection first and that was wrong; pi5's
own log settles it:

```
port mapped by the router  mesh=default external=178.213.45.235:51821 proto=natpmp
```

pi5 listens on 51820, asked NAT-PMP to map 51820, and was handed **51821** —
which the laptop already held from its own mapping. So both machines hold what
each believes is a valid promise from the router for one address and port, and
only one can receive on it.

A mapping is a promise the router makes, and this is what it looks like when the
router breaks one. Nothing either node did was wrong, and neither can tell.

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

### And the same for mapped addresses

Corroboration cannot help with the case above, because a mapped address never
goes through reflection — the router asserts it directly. So the second half is
simpler: **do not announce an address a peer is already announcing.**

Two nodes cannot both be behind one address and port. If a peer claims one of
ours, at most one of us is right and neither knows which, so saying it anyway is
worse than staying quiet. The roster already carries every peer's announced
endpoints; the check costs a map.

Only public addresses are compared. Two machines on one LAN legitimately share a
subnet, and treating that as a conflict would strip the LAN address from every
node in the building — the addresses most likely to work.

### What it does not fix

A node that has no corroborated address now announces fewer candidates, which
is honest but not the same as being reachable. Neither node gets a working public address out of this — they get an honest
silence instead of a misleading claim, which is better but is not reachability.
Both will fall back to a relay until the router hands out distinct ports, or one
of them is configured with `advertise` for a port that is genuinely forwarded.

Worth noting what the laptop actually reaches pi5 at: `178.213.45.235:51820`,
which pi5 never announced. WireGuard learned it by roaming — packets arrived
from there, so it replies there. That address is real and neither node knows it,
which suggests a third improvement: an address a peer's traffic *actually
arrives from* is better evidence than either a mapping or a single observation.

## Meanwhile

The behaviour is safe. Traffic flows, the relay carries it when the direct path
cannot be verified, and nothing is misdelivered — the signature check is what
makes the failure a fallback rather than a hijack.

The cost is throughput and latency on a path that should be direct, and a
`status` output that looks wrong to anybody who knows pi5 is a relay.
