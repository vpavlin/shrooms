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

## Could a mixnet hide who is using the relay?

Asked directly, so answered directly: no, and mostly for a reason that has
nothing to do with mixnets being good or bad.

**The relay path is not libp2p.** Relay frames are raw UDP on the same socket
WireGuard uses — that is what makes NAT traversal work at all. libp2p-mix
carries libp2p messages, so there is nothing here for it to take hold of.
Putting the data plane on libp2p to make it mixable is the rewrite ADR-001
exists to refuse.

[ADR-011](adr/011-no-mixnet.md) settles the rest and its findings still hold:
`PathLength` is a compile-time constant, only two of three hops delay, and cover
traffic is implemented but never wired up — the weak half of the Loopix trade,
paying latency without buying unlinkability because there is nothing to mix
with.

One thing worth adding, because it is specific to this question rather than to
ADR-011's: **the relay needs a destination to route to.** Even a perfect mixnet
would hide where you are, not which device you are. The relay would still be
accumulating a list of tunnel keys and who talks to whom.

That last part is fixable without a mixnet, and cheaply.

### Blinded registration

The relay uses the tunnel key purely as a lookup handle — it does no
cryptography with it. So it does not have to be the real key. Both ends could
register and address under

    tag = HKDF(relay_key, wg_pub)

Both already hold the relay key and each other's tunnel keys, so both derive the
same tag; the relay matches tags and never sees a real key.

What it buys: the operator sees opaque per-relay tags. They cannot recognise a
device on a second relay, cannot match one against a key observed anywhere else,
and learn nothing about the mesh from the identifier itself. Two relay operators
comparing notes see unrelated values.

What it does not buy: the IP address, the pairing, the volume and the timing are
all still visible. Hiding those is what a mixnet would be for, and per the above
it is not on offer.

Cost: another relay wire change, and therefore another flag day. Worth bundling
with the trust-on-first-use rule above rather than paying twice — the two land
in the same frame and are the same size.

Note it composes with TOFU rather than fighting it: the relay is binding a
device key to a tag rather than to a tunnel key, and it can check that signature
either way.

## One relay, many meshes: two keys doing two jobs

An obstacle this note missed, found while scoping it, and the way round is
better than what was here before.

**Today a relay serves exactly one mesh.** Each mesh runs on its own port
(`ListenPort + i`), and every frame is authenticated with a key derived from
*that mesh's* network key — `Decode(k Key, pkt)` cannot verify a frame without
knowing which key to use. So a volunteer forwarding for five friends' meshes
needs five ports and five keys, and the port count leaks how many meshes they
carry.

The earlier suggestion here — hand out the derived relay key — works but
inherits that structure. The cleaner split is to notice that the one key is
doing two unrelated jobs:

**"May I use this relay?"** belongs to the relay, not to a mesh. The operator
issues a **relay token**; anyone holding it may register and forward. It is what
stops the relay being an open reflector for bouncing traffic at third parties,
and it says nothing about who anybody is. One token, one port, any number of
meshes.

**"Which device is this?"** belongs to the mesh, and never needs to reach the
relay at all. Devices register under

    tag = HKDF(mesh_relay_key, wg_pub)

as described above. Only members of that mesh can compute its tags, because only
they hold the key it derives from — so a token holder from another mesh cannot
address, or claim, a device they have somehow learned the tunnel key of.

Two keys, two questions, and the relay's forwarding table becomes flat:
`tag → address`. It never learns that meshes exist, let alone how many it
serves or which devices belong together.

The operator gets something out of this too: a token they can rotate or withdraw
without touching anybody's mesh, and which cannot be used to read anything.

Configuration on the client side is then two lines rather than one — an address
and a token — and both come from the same place, which is whoever offered you
the relay.

## Does the operator hand out tokens by hand?

"A token to hand over" was vague, and the honest answer is that the manual step
is probably avoidable. It is worth separating two reasons a relay might want a
secret, because only one of them is about safety.

### Safety needs no token: prove you receive where you claim

The reason an open forwarder is dangerous is reflection — someone points it at a
third party and makes it send traffic there. That attack needs the relay to
forward to an address the *attacker* chose.

Registration can close it without any shared secret:

1. A device registers "tag X, reach me at this address".
2. The relay sends a random nonce **to that address**.
3. The device echoes it back.
4. Only then is the mapping installed.

An attacker registering somebody else's address never receives the nonce and
cannot echo it. This is the return-routability check ICE and QUIC already use,
and it costs one round trip on registration and nothing afterwards.

Note also that relay forwarding is one packet in, one packet out. There is no
amplification, which is what makes reflectors worth building in the first place.

So **a blind relay can be open to anyone, with zero manual work, and still not be
usable as a reflector.** What is left to abuse is the operator's bandwidth, and
that is what limits are for rather than what a token is for.

### A token is for policy, not safety

An operator who wants to choose *who* uses their relay still can, and then the
unit is a **mesh**, not a device: one token, issued once, for everybody on that
mesh. That is the same amount of work as agreeing to help a friend, not per
device and not ongoing.

Getting it to the devices is the case where invites carrying relay configuration
finally earns its place (see [disappearing.md](disappearing.md), where it was
withdrawn as an onboarding feature): a new device gets the address and the token
sealed inside its invite, and existing devices get it the way they get any other
mesh-wide setting.

Recommendation: build the return-routability check, make tokens optional, and
default to open-with-limits. An operator who wants a guest list can turn one on.

## Limits an operator needs

All three are straightforward, because `Server.Handle` is a single choke point
that already sees every frame and already keeps counters
(`registered`, `forwarded`, `dropped`).

**Number of registrations** — exists as `MaxRegistrations = 512`, currently a
constant. Wants to be configurable, and wants a second cap per token (or per
source address when open) so one user cannot take the whole table.

**Total bandwidth** — a byte counter and a ceiling. The relay sees every
forwarded packet, so this is a counter increment on a path that already exists.

**Per-registration bandwidth** — a token bucket per entry. Same mechanism, finer
grain.

One caveat on how throttling behaves. Dropping packets on a WireGuard tunnel
presents as packet loss, and TCP inside the tunnel backs off — which is roughly
the right behaviour and needs no cooperation. But a *hard* cutoff mid-session
looks like the network dying, with no way for the user to know why. A gradual
throttle is kinder than a cliff, and the relay should be able to say it is
throttling somewhere the user can see.

## Could relaying be paid for?

Vaclav's question, and the instinct is sound: [ADR-012](adr/012-relay-hosting.md)
already blames Tor's relay shortage on having no reciprocity to build on, and
this is the shape of a fix.

The hard part is not the payment. It is **proof of service**: how does the payer
know the relay forwarded, and how does the relay prove it did? Bandwidth is not
self-attesting, both sides can lie, and a relay can manufacture traffic to bill
itself. This is the problem that has occupied every decentralised-bandwidth
project and none has solved it cleanly.

**The way out is to not solve it.** If a token is a bearer capability with an
expiry and a quota, then the token *is* the thing being sold. How somebody
obtained it — free from a friend, cash, a bank transfer, a Logos token — is
outside the protocol entirely. Prepaid capacity needs no metering, no
settlement, no chain, and no trust in either direction beyond the window the
token covers.

So the architectural advice is: **do not build payments. Build the thing
payments would buy**, and make it expirable and transferable. If incentives
later want a settlement layer, it sells tokens; nothing in the relay changes.

Two cautions worth recording:

- **Metered micropayments are almost certainly not worth it.** Relaying a
  gigabyte is worth a fraction of a cent, which does not survive the overhead of
  accounting for it.
- **Taking payment changes the operator's position.** In many jurisdictions
  being paid to carry traffic is what separates a person doing a favour from a
  provider with obligations. That is a real consideration for whoever
  volunteers, and it belongs in whatever documentation invites them to.

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
  chat. They issue a relay token, first-claim-wins is plenty, and this is a
  small feature: two config lines, a token to hand over, and the TOFU rule.
- *The public* — anyone can point at it. Then it needs rate limiting, abuse
  handling, probably per-mesh quotas and some way to publish a list, and the
  traffic-analysis surface is being offered to people you know nothing about.

The first is worth doing and mostly written. The second is a different project
and would be the first thing in Shrooms that looks like infrastructure somebody
operates.
