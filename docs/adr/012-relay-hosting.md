# 012. Who runs the relay

**Status:** accepted for tiers 1–2; tier 3 deliberately deferred

## Context

Some peer pairs cannot punch through to each other — under endpoint-dependent
NAT they never will. Those pairs need a relay: a reachable third party that
forwards packets between them.

The obvious answer is "rent a VPS", but that is a recurring cost and a machine
to maintain for what may be a handful of packets a day. Could someone else's
node do it instead?

## Decision

Support three tiers, and implement the first two.

### Tier 1 — a reachable node in your own mesh ✅ implemented

**Any mesh node with `relay = true` and a reachable address forwards for the
others.** Nothing else is required.

This is the answer most people actually need and it is easy to miss: if *any*
one of your machines is reachable — an office box with a port forward, a home
server on a static IP, a NAS with UPnP working — **you do not need a VPS at
all**. That node relays for every pair that cannot connect directly.

The trust question does not arise: it is your own machine, already a full mesh
member holding the network key.

### Tier 2 — a friend's node, same mesh ✅ implemented

Identical mechanism. A friend who is already a mesh member and has a reachable
address can relay for you. They are already trusted with the network key, so
relaying grants them nothing new.

### Tier 3 — a stranger's node, different mesh ⏸ deferred

A public volunteer relay network: people with spare bandwidth run relays for
meshes they are not part of.

## Why tier 3 is *architecturally* easy

**The relay cannot read what it forwards.** WireGuard has already encrypted and
authenticated the payload end to end; the relay sees ciphertext and two public
keys. This is exactly Tailscale's DERP trust model — their relays carry traffic
they cannot decrypt.

So a stranger's relay costs you:

- **Metadata.** They learn which two public keys talk, when, and how much.
- **Availability.** They can drop you, and you would fail over.
- **Nothing else.** Not confidentiality, not integrity, not the ability to
  inject — the relay fills the source key in from whoever registered the address
  a packet came from, so it cannot even attribute a packet to the wrong peer
  without the receiver's WireGuard rejecting it.

## Why it is deferred anyway

Three things need solving, and one of them is not technical.

**Discovery.** Relays would need to advertise themselves somewhere a stranger
can find them. Waku fits this naturally: a well-known *public* topic carrying
signed relay announcements — deliberately not the mesh's rotating rendezvous
topic, which is private by construction. This is the easy part.

**Authentication.** Our frames are MAC'd with a key derived from the network
key, which is what stops the relay being an open reflector. A third-party relay
does not have your network key and so cannot verify your frames. It would need a
different model — genuinely open (and rate-limited), or some proof of
entitlement.

Note the current design already limits abuse more than a naive reflector:
**both** endpoints must have registered, so you cannot use it to send traffic to
an arbitrary victim. That property should survive into any tier-3 design.

**Incentives — the hard one.** The people who *can* relay are exactly the people
who *do not need* relaying. A node with a public address and spare bandwidth has
no NAT problem of its own. So unlike BitTorrent, where everyone both uploads and
downloads and tit-for-tat emerges naturally, a relay network has **no
reciprocity to build on**. This is why Tor has a persistent relay shortage
despite obvious demand, and why Tailscale runs DERP itself rather than asking
users to.

That leaves altruism (works at small scale, does not scale), payment (an entire
economic layer, and the point of this project was to avoid a service), or
federation among people who already know each other — which is tier 2 with extra
steps.

## Consequences

- Most users need no VPS: one reachable device in the mesh is enough.
- A VPS is a convenience for the case where *nothing* you own is reachable, not
  a requirement of the architecture.
- Tier 3 stays open as a design direction. If it happens, the discovery half
  belongs on Waku and the incentive half is the real work.

## What would change our mind

An existing relay network with a working incentive model that we could join
rather than build — or evidence that altruistic relays are sufficient at the
scale this would ever reach.
