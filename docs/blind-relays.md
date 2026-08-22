# Blind relays: a stranger forwards for you without joining

**Status:** built. The relay engine, the routability check, first-claim-wins,
the operator's limits and a standalone `shrooms-relay` binary all exist and are
tested; what is not yet wired is the client side, so nothing points at one of
these yet. See `deploy/akash/` to run one.

**No guarantees, of any kind.** This is a best-effort experiment shared so that
people can poke at it. It has not been audited, a relay may lose your traffic,
and one you do not run yourself may vanish without notice.

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

    tag = HKDF(mesh_relay_key, wg_pub ‖ relay_address)

Both already hold the relay key and each other's tunnel keys, so both derive the
same tag; the relay matches tags and never sees a real key.

What it buys: the operator sees opaque per-relay tags. They cannot recognise a
device on a second relay, cannot match one against a key observed anywhere else,
and learn nothing about the mesh from the identifier itself. Two relay operators
comparing notes see unrelated values.

**The relay address has to be in the derivation, and the signing key has to be
per relay too.** Both were missed the first time, and each on its own defeats
the property entirely. A tag derived from the mesh key alone is the same
everywhere a device goes. And a register frame carries its signing key in
cleartext — the relay needs it to enforce first claim wins — so signing with the
device's mesh identity hands every operator one stable name regardless of how
good the tag is. See `relay.Tag` and `relay.RelayIdentity`.

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

    tag = HKDF(mesh_relay_key, wg_pub ‖ relay_address)

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

### RLN memberships as the capability

Vaclav's suggestion, and it fits better than expected — because a membership
already *is* the thing the previous paragraph says to build.

From [ADR-028](adr/028-when-the-fleet-turns-on-rln.md), read out of
`logos-lez-rln` rather than documentation:

```rust
pub fn calculate_payment_amount(rate_limit: u64, price_per_unit: u128) -> u128 {
    price_per_unit * (rate_limit as u128)
}
```

with `active_duration` and `grace_period_duration` on the membership state. That
is a bearer capability, bought by the unit, that expires — arrived at
independently above as "the thing payments would buy", and already specified,
already being built, and already carrying a payment rail whose wallet lives in
Basecamp.

Three things it does better than a bearer token:

- **It is anonymous.** A proof shows membership, not identity. A shared token is
  the same bytes every time, so a relay can link every session that used it;
  an RLN proof cannot be linked to the last one. That composes with blinded tags
  — the operator would learn neither who you are nor which device.
- **The rate limit enforces itself.** The nullifier *is* Shamir sharing: exceed
  your rate within an epoch and you publish two shares of your own secret. The
  relay does not have to trust, track or adjudicate.
- **Revocation is intrinsic.** Overuse is self-slashing rather than an operator
  decision.

And the objection that dominates ADR-028 is much weaker here. There, the
decisive cost was that on-chain memberships create "a permanent, enumerable
public record that a key publishes on a shard" — a registry the project
deliberately does not have. For relay access the enumerable fact is *somebody
bought relay capacity*. It names no mesh, no device address and no roster. That
is a far smaller disclosure than the control-plane case that ADR-028 declined.

### Where it does not fit, and it matters

**RLN limits messages; a relay costs bytes.** `user_message_limit` is bounded
100–600 *messages* per epoch. A relay forwards millions of packets, and there is
no proving per packet — which is why "proofs on connection" is the right
instinct and the only workable one.

But then the limit bounds **how often you may register, not how much you may
send.** A membership would gate access and meter nothing, while what actually
costs the operator is bandwidth. So the ordinary counters above are still
needed; RLN does the entitlement half and none of the metering half.

That is survivable — counters are easy, entitlement was the hard part — but it
means a membership cannot be priced against consumption without a second
mechanism beside it.

**A proof per registration is a proof per minute.** `RelayRefresh` is half the
registration TTL, so registrations repeat about every 60s. Proving that often on
a phone is heavier than ADR-028's control-plane case, not lighter. It wants a
session: prove once, receive a short-lived symmetric credential, refresh with
that.

**It adds a liveness dependency to the component least able to carry one.**
Verifying a proof needs the current membership root, so a relay would have to
follow the registry, with cached roots and a staleness window when LEZ is
unreachable. A relay today needs nothing but a socket.

**And it only pays for itself in the public case.** For a friend with a VPS this
is enormous overkill; the friend wants a config line, not a zero-knowledge
proof.

### What that implies for building order

Make entitlement an **interface**, not a mechanism: the relay asks "may this
registration proceed?" and the answer is a plain token today, an RLN proof later
if the public case ever justifies the dependency. Nothing else in the relay
changes between those, and the dependency is not taken until it earns itself.

The session concept is worth having regardless, since a proof per minute is
unreasonable whatever produces the proof.

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

## Decided: open, with limits, and no promises

Agreed 2026-08-21. Open by default with quotas, not a guest list — a relay is
useful to a stranger or it is not useful at all, and the return-routability
check means open costs nothing in safety.

**The promise is that there is none**, and that has to be said where somebody
would rely on it rather than buried here. A relay may vanish, throttle, refuse,
or be switched off mid-session. It keeps no data and offers no uptime. This is a
best-effort experiment published so other people can poke at it, and anybody
building on it should assume it will be gone tomorrow.

That is a design constraint, not a disclaimer. It means the relay advertises no
capacity, a client treats losing one as ordinary rather than exceptional, and
nothing in the protocol lets an operator make a commitment they would then be
held to.

### Running one on Akash

Raised because Akash has no traffic limits, which is the right instinct for a
relay. It works, with two caveats found by reading rather than assuming:

**Built, and the state question turned out to be the wrong question.** An
earlier version of this note worried about which two secrets a relay would have
to carry in its deployment environment to survive a redeploy. That applies to a
*mesh-member* relay like the VPS. A blind relay carries nothing at all: it is
not a member, so there is no device identity; it is not a rendezvous node, so
there is no libp2p identity; and its forwarding table is soft state that clients
rebuild within one refresh interval.

So there is no volume, no env secret, and nothing to restore. `internal/relay`
depends on nothing native either, which means the whole thing is a 2.5 MB
scratch image running as an unprivileged user — see `deploy/akash/`.

**The IP lease turns out not to be needed**, which is worth correcting because
an earlier version of this note treated it as the unavoidable cost. Checked
against Akash's own announcement of leases rather than assumed: what a lease
buys is the ability to *choose* a port —

> some services (like a VPN, for example) must use standard ports in the 0-1024
> range, which isn't possible unless you have a dedicated IP

Before leases a service was still exposed, on a port the provider picked; you
simply had no say in which. **A relay does not need a say.** It is reached at
whatever address and port its users are configured with, and that value is
arbitrary either way. So `deploy/akash/relay-noip.yaml` takes no lease, and the
cost drops to the compute — which for something that copies bytes between two
sockets is close to nothing.

What going without costs is a stable address: the assigned port can change when
a deployment is recreated. For a relay handed out alongside a token, that is a
line in a message you were sending anyway.

- **Deployments are paid in ACT, not AKT.** Found by deploying: a create with
  an AKT deposit is refused with "Deposit invalid", and the chain rejects it by
  design — `x/deployment` allows AKT only as a top-up to a deployment that
  already exists. Deposits and pricing are `uact`, minted from AKT with
  `tx bme mint-act`. Both descriptors priced in `uakt` until this was hit.
- **It works without a lease, one is running, and it costs 28 cents a month.**
  Confirmed 2026-08-21 on codestan.fi: a node port, no dedicated address, no
  volume, no state — two devices registered through the routability challenge
  and a packet relayed between them in 85ms. The price is the part that matters
  beyond the engineering: at five dollars a relay is a subscription somebody
  decides to keep, and at twenty-eight cents it is not a decision at all. That
  is what makes "other people run these" a plausible sentence.
- **It is provider-dependent, and two others forwarded nothing.** Both ran the
  container correctly. On one this was measured: the provider's node address
  had TCP node ports wide open and nothing at all on UDP, so Kubernetes creates
  the mapping and that network does not carry it. Try without a lease and be
  ready to move; the probe settles it in one command.
- **`shrooms-relay -probe host:port` is how you check.** It is a client rather
  than an inspection: two throwaway devices, both registered the way a real one
  would, and a packet relayed between them. A pass means the whole path works,
  NAT and forwarded port included, rather than that something is listening.

### A TCP relay transport, reinstated

Dropped here once, on the grounds that its only remaining justification was the
censorship one [ADR-030](adr/030-tailscale-shaped-not-tor-shaped.md) refused.
That was reasoning from a belief since disproved — that UDP worked without a
lease. It does not, and on the very host where UDP is dead, TCP NodePorts are
wide open. So the deployment argument is back, and it is now measured rather
than assumed.

**And there is a better argument, about users rather than hosting.** Some
networks block UDP outright — hotels, corporate guest wifi, a few mobile
carriers. A device on one of those cannot reach the mesh at all today, directly
or through a relay. A TCP relay fixes that. It is not censorship circumvention;
it is an ordinary broken network, and it is the difference between "the mesh
does not work here" and "the mesh is slower here".

**Why it is not a redesign.** `RelayEndpoint` is a `conn.Endpoint` that
WireGuard treats as opaque, and relay frames are already a self-contained byte
protocol. How those bytes reach the relay is a transport swap under a seam that
exists. The reason the data plane needs UDP — hole punching — does not apply to
this leg at all: both ends dial *out* to a publicly reachable host.

**What it costs.** TCP-over-TCP is the real objection. WireGuard carries the
user's TCP inside, so one lost segment on the outer connection stalls everything
behind it while the inner stack retransmits on top. Under loss that degrades
badly, and the failure is confusing — a tunnel that is up and unusable. Against
it: the relay is already the degraded path, each device holds its own
connection, and every commercial VPN ships this as a fallback mode. Plus the
ordinary plumbing of length-prefixed framing, reconnect with backoff, and
keepalives.

**Shape, if built.** A second listener on the relay and a second dialler on the
client, both speaking the frames that already exist; the relay's table and every
rule around it stay as they are. Offered alongside UDP rather than instead of
it, and chosen only once UDP has failed — so nobody pays the head-of-line cost
for a path that was working.

## Making them discoverable

Configuring `relay_addr` by hand on every device is the thing that stops anyone
using this. Two ways out, and they answer different questions.

### Within a mesh: a member says so

The narrow one, and it needs nothing new. A device that knows a blind relay
publishes it in its ordinary announce, exactly as `Boot` already carries a
delivery multiaddr ([ADR-031](adr/031-bootstrap-from-the-mesh-itself.md)).
Announces are sealed under the network key, so only members read it, and the
relay never learns it is being advertised.

Configure one device, and the mesh knows. That also solves the address moving:
a node port changes on redeploy, and reconfiguring one machine beats
reconfiguring five. Announces are JSON, so the field is additive — no flag day.

What it does not solve is a device that has never been told about any relay.

### Publicly: a well-known topic

The broader one, and the one that makes relays a commons rather than a thing you
must already know about. A well-known public content topic carries relay
advertisements; a peer that cannot reach anybody directly subscribes, picks one,
and uses it. No configuration at all.

**The relay must not be what publishes.** A blind relay has no Delivery node —
that is what makes it 2.5 MB, free of cgo, and cheap enough to run on the
smallest tier there is. Giving it a Waku node to publish pings would cost the
properties that made it worth running.

It does not need one. Whoever runs a relay is already a shrooms user with a
Delivery node, so **their node advertises it** and the relay stays a dumb
forwarder that never knows it is listed. The advertisement is a signed record —
address, whether a token is needed, an expiry — and it costs the operator
nothing but a config line.

**Verification is already built.** An advertisement is a claim, and the
routability challenge is the answer: a peer probes before trusting, exactly as
`shrooms-relay -probe` does, and a relay that does not forward is simply not
used. So a poisoned listing costs a round trip, not a connection.

**What it risks, stated plainly:**

- **Anyone can advertise.** Including a relay run to watch who uses it. The
  traffic-analysis surface is already documented above and does not change —
  but a public list makes it easy to offer that surface to strangers at scale,
  which is different from a friend offering it to you. Mitigations are ordinary:
  prefer relays that work, keep several, and never make one sticky.
- **It is a list, and lists are blockable.** A censor can take the topic. That
  is true of the rendezvous plane already, so it adds a target rather than a
  weakness.
- **It will fill with junk.** Expired entries, dead hosts, optimistic
  advertisements. Whatever consumes it has to treat every entry as a candidate
  to be tested rather than a fact.

**The decision to make:** whether relay advertisements ride the *existing*
public rendezvous plane, or a topic of their own. Sharing means no new
infrastructure and the anonymity of other traffic; a separate topic is easier to
reason about and easier to block. Worth choosing before building.

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
