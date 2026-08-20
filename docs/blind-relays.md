# Blind relays: a stranger forwards for you without joining

**Status:** design note, nothing built. One decision at the end.

The idea: somebody runs a public node, you point your mesh at it, and it
forwards packets between your devices without being a member of your mesh —
without seeing who is on it, or being able to read anything.

Worth writing down because most of it already exists, and the one part that does
not is a security property added on 2026-08-18 that this would give back.

## What a relay does today, in plain terms

Two of your devices cannot always reach each other directly. A phone on mobile
data usually cannot be dialled at all: the carrier gives it an address shared
with thousands of other people and rewrites the port per destination. When both
ends are like that, no amount of cleverness connects them.

A relay is a machine that *can* be dialled. Both devices send to it and it
passes the packets along — a post office rather than a participant.

**It cannot read anything it forwards.** The packets are WireGuard, encrypted
between the two devices, and the relay has neither private key. This is not a
promise about relays being well-behaved; it is arithmetic. A hostile relay can
drop your packets or count them. It cannot open them.

Today a relay is one of your own machines and a full member of the mesh.

## What it would take to let a stranger do it

Three pieces. Two exist.

**1. Pointing at it without discovery — exists.** Relays are normally found by
listening for them announcing themselves on the mesh, which a stranger cannot
do. But `relay_addr` in the config already overrides discovery:

```go
// An explicit relay_addr overrides discovery.
if m.relayPin.IsValid() {
    return relayChoice{ok: true, addr: m.relayPin}
}
```

**2. Letting it check frames without letting it read your mesh — exists, and is
the pleasant surprise.** A relay verifies the frames it is sent using a key
derived from the network key:

```go
r := hkdf.New(sha256.New, nk[:], nil, []byte("mesh/v1/relay"))
```

HKDF is one-way. The relay key can be computed from the network key and the
network key **cannot** be computed back from it. So a relay can be handed the
derived key alone — enough to check that frames come from your mesh, not enough
to decrypt a single announce, learn who is on the mesh, or derive the tunnel
keys.

That is not a change. It is true of the code as written; nothing has ever
required a relay to hold the network key, only the daemon that hands it one
happens to have both today.

**3. Checking that a device is not claiming somebody else's key — does not
exist for a stranger, and this is the whole problem.**

## The check that breaks

Two days ago the relay gained a rule: when a device registers "reach me at this
address for tunnel key X", the relay verifies that the device actually owns X.
Without it, any mesh member could tell the relay that *your* tunnel key is at
*their* address — silently swallowing your relayed traffic and learning who was
trying to reach you.

The relay answers that by consulting its roster:

```go
// The roster is the source, and it is a good one: every entry comes from an
// announce the device signed, which names both of its keys — so the pairing is
// asserted by the only party entitled to assert it.
```

A blind relay has no roster. It is not a member; it has never seen an announce.
So it cannot answer the question at all, and the hijack that was just closed
comes back for anybody using one.

**This is the trade the idea has to resolve**, and it is worth being clear that
it is a real regression rather than a detail.

## A way out: first claim wins

The register frame already carries the device's public key and its signature
over the whole frame — that was added at the same time as the check above. A
blind relay can verify that signature on its own: it is self-contained
arithmetic, no roster required. What it cannot do is know whether that device
*should* have the tunnel key it names.

So let the first claim stand, and never let it move:

> The first device key to register tunnel key X owns X here. A registration for
> X signed by any other device key is refused, for as long as the entry lives.

This is "trust on first use" — the same rule SSH uses when it shows you a
fingerprint the first time and shouts if it ever changes.

What it gives: once your phone has registered, nobody can take that entry from
it, because they cannot forge its signature. The hijack against a device already
using the relay is closed.

What it does not give: if an attacker registers your tunnel key *before you
ever do*, they hold it. They would need to know your tunnel key, which is
visible to anyone on your mesh — so this defends you against strangers and
against a member who was too slow, not against a hostile member who was quick.

That is weaker than the roster check and stronger than nothing. Whether it is
enough depends on what a blind relay is for: if it is a public utility used by
people who are not on each other's meshes, the attacker who matters is a
stranger, and a stranger does not know your tunnel key.

## What the relay operator learns

Worth stating plainly, because "it cannot read your traffic" is true and
incomplete.

They **cannot** see: message contents, your network key, who is on your mesh, the
names of your devices, or anything from the control plane.

They **can** see: the tunnel public keys of the devices using them, the IP
addresses those devices connect from, which pairs of keys exchange packets, how
much, and when. That is a traffic-analysis surface. A relay operator learns that
key A and key B talk daily at 09:00 from a German and a Czech address — not who
they are, unless they can connect a key or an address to a person.

Compared to what you have now, this is a real change: today the only machines
seeing that are your own.

## What it would cost the operator

Three things to bound, none of them new but all of them sharper when the users
are strangers:

- **The registration table.** Capped at 512 entries with one address holding one
  key, so a single socket cannot flood it. A public relay serving many meshes
  wants that number configurable, and probably per-mesh.
- **Bandwidth.** Relayed traffic is somebody else's video call. There is no
  accounting and no limit today.
- **Being an open reflector.** A relay that forwards for anyone who asks is a
  tool for bouncing traffic at third parties. The MAC on every frame is what
  prevents this — which is another reason a blind relay must still be given a
  key, rather than being open to all comers.

## What this is not

It is not a Tailscale DERP or a Tor relay. Those carry traffic for people who
have no other route and are operated as infrastructure. This is narrower: a
machine you were *told about* by somebody you already trust enough to accept a
config line from, doing one job, with a key that only works for one mesh.

Which is also the answer to "does this make Shrooms less decentralised?" — no,
as long as nobody has to use a *particular* relay. It becomes a coordinator the
moment there is one everybody depends on.

## The decision

**Is a blind relay for a stranger you trust a little, or for the public?**

- *A stranger you trust a little* — a friend with a VPS, someone in a group
  chat. They get the derived relay key, first-claim-wins is plenty, and this is
  a small feature: a config line, a key to hand over, and the TOFU rule.
- *The public* — anyone can point at it. Then it needs rate limiting, abuse
  handling, probably per-mesh quotas and some way to publish a list, and the
  traffic-analysis surface is being offered to people you know nothing about.

The first is worth doing and mostly written. The second is a different project
and would be the first thing in Shrooms that looks like infrastructure somebody
operates.
