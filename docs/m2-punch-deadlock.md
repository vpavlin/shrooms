# Why M2's eim punch fails: a conntrack tuple both sides need

**Status:** diagnosed, not fixed. Two decisions below.
**Found:** 2026-08-18, while validating Phase 4. **Not caused by it** — the same
failure reproduces on `4c3f5b9`, before disco signing or the relay change.

## The symptom

`make m2` (NAT_MODE=eim, the punchable case) fails:

```
FAIL: discovered but never punched through under eim
node-b  ...
  no candidate has answered a probe yet
  announced: [10.90.0.30:51820 10.92.0.100:51820]
```

Both nodes discover each other in ~60s, both probe each other every 3s, and
neither ever gets an answer. Reaching node-pub works from both.

## What is actually happening

Measured with counters in each gateway's `raw`, `nat`, `filter` and `mangle`
tables, not inferred:

| observation point | result |
|---|---|
| node-a → node-b, forwarded out of gw-a | yes |
| arrives at gw-b's wire (`raw PREROUTING`) | yes, 4 packets |
| forwarded by gw-b to node-b | **no — lands on gw-b's INPUT** |
| node-b → node-a, forwarded out of gw-b | yes, 26 packets |
| arrives at gw-a's wire | **no — zero, ever** |

So one direction reaches the far gateway and stops there; the other never
leaves. The asymmetry is the clue.

**The mechanism.** node-a's probe arrives at gw-b addressed to `10.90.0.30:51820`,
which is gw-b's own WAN address. gw-b has no matching conntrack entry, so it is
not reverse-NAT'd; it goes to INPUT, where nothing is listening. But conntrack
tracks it anyway, creating an entry:

```
orig:  10.90.0.20:51820 -> 10.90.0.30:51820
```

Now node-b tries to punch outward. gw-b's SNAT rule wants to map it to
`10.90.0.30:51820 -> 10.90.0.20:51820`, whose **reply tuple is exactly the entry
above**. Conntrack will not allocate a clashing tuple, and the packet is dropped
inside gw-b — which is why it never reaches the wire.

It is permanent, not a race that resolves. node-a keeps probing every 3 seconds,
each packet refreshing the entry that blocks node-b's escape, so the side whose
inbound arrives first can never punch out for as long as the other side keeps
trying. The harder the peer tries, the more firmly it is locked out.

**Proof, rather than a story.** Adding one rule to gw-b so the inbound packet
never takes the tuple:

```
iptables -t raw -I PREROUTING 1 -i eth1 -p udp -s 10.90.0.20 --dport 51820 -j NOTRACK
```

node-b's punch packets started arriving at gw-a immediately — a counter that had
read 0 for the whole run began climbing within seconds.

## Is this real, or an artefact of the harness?

Real. The gateways are plain Linux boxes doing SNAT, which is what a home router
is. Any NAT that tracks unsolicited inbound UDP — and Linux does by default —
can enter this state.

What normally saves punching in the field is timing: both sides send first, so
each has its own outbound entry before the other's packet arrives, and no tuple
is contested. This topology loses that race and then cannot recover, because
nothing ever stops probing long enough for the entry to expire (30s for
unreplied UDP, refreshed every 3s).

It also explains something already seen in the field and shrugged off: a pair
that sits on the relay while a third machine reaches both directly.

## Decision 1: does the harness model a router that tracks, or one that does not?

- **Keep it as it is.** M2's eim case stays red, honestly reporting a case where
  punching cannot work. The milestone's claim — "endpoint-independent mapping is
  the punchable case" — is then wrong as written, because mapping is not the only
  thing that decides.
- **Add the NOTRACK rule to `gateway.sh`**, modelling a router that drops
  unsolicited inbound before conntrack sees it. M2 would then test what it says
  it tests, and this failure mode would need its own scenario to keep it covered.

Recommendation: the second, plus a third NAT mode (`eim-sticky`) that keeps this
behaviour deliberately, so it is tested rather than lost.

## Decision 2: should the product do anything?

It already has the right answer — the relay — and `RELAY=1` is unaffected. But a
node cannot currently tell this state from "the peer is offline": its own router
is dropping its packets, so it sees silence and concludes nothing.

The known technique is to vary the source port after repeated failure, which
sidesteps the contested tuple: the new port has no entry, so the SNAT succeeds.
That is a real change to the disco socket and worth its own ADR if wanted.
Cheaper and almost as useful: notice the pattern — a peer that announces fresh
endpoints, that we probe, that never answers, while another peer confirms we are
reachable — and log it as a probable NAT deadlock rather than a dead peer.

## Separately: NAT_MODE never reaches the gateways

`scripts/m2-containers.sh` accepts `NAT_MODE=edm`, but `docker/compose-nat.yml`
passes no environment to `gw-a`/`gw-b`, so `gateway.sh` always falls back to its
own default of `eim`. **The symmetric-NAT scenario has never actually run.**
Whatever is decided above, that is a plain bug: a test that reports on a case it
does not set up.
