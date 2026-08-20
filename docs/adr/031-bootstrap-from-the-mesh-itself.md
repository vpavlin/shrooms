# 031. Bootstrap from the mesh itself

**Status:** proposed. Nothing built; the failure it fixes happened on
2026-08-20 and is described below.

## Context

A node needs the rendezvous plane before it can find anything, and it reaches
that plane through bootstrap addresses it did not choose.
[ADR-004](004-public-fleet-not-own-cluster.md) picked the public fleet partly
because there was "nothing to run":

> The six entry nodes are `/dns4/` multiaddrs hardcoded in …

On 2026-08-20 five of those six refused TCP on port 30303. The sixth is in Hong
Kong and answers slowly from Europe. Measured, not inferred:

```
node-01.do-ams3                  refused
node-02.do-ams3                  refused
node-01.gc-us-central1-a         refused
node-02.gc-us-central1-a         refused
node-01.ac-cn-hongkong-c         OPEN
node-02.ac-cn-hongkong-c         refused
```

The laptop logged `successfulConns=0 attempted=6` and went to zero peers on
three meshes. Every other machine stayed up — not because they were healthier,
but because they were **already connected** and never had to bootstrap again.
The laptop had restarted.

So the failure mode is precise: *an established fleet survives its bootstrap
disappearing, and cannot survive a restart while it is gone.* Every node is one
reboot away from being unable to rejoin, and nothing warns about it because
everything looks fine until it happens.

[ADR-028](028-when-the-fleet-turns-on-rln.md) recorded the same shape on
2026-08-07 without generalising it. This generalises it.

## Decision

**A mesh becomes its own bootstrap.** Three parts:

**1. Core + Relay nodes publish their delivery multiaddr** in their announce,
alongside the relay flag they already publish.

The predicate is right for two independent reasons. `Relay` already means
publicly reachable — `selectRelay` skips relays for relay nodes because "a relay
is publicly reachable by definition" — and `Core` means the node carries gossip,
so it is worth bootstrapping *from*. An Edge node is neither, and is correctly
excluded.

**2. Peers persist what they learn.** This is the part that makes it work rather
than merely sound good. `internal/waku` exposes `Subscribe`, `Send`, `PeerID`
and `PeersInMesh` — **there is no `AddPeer`**. Bootstrap addresses are consumed
when the node is constructed and never after, so an address learned at runtime
cannot help the running node. It has to reach disk and be merged into the entry
list at the next start.

That is exactly the case that failed: the laptop had known these peers for
weeks and had nowhere to write them down.

**3. Invites carry them.** A device that has never connected has nothing
persisted, so it still needs somewhere to start.

They belong in the **token**, not the response. The invite response travels over
the bus, so a device that cannot bootstrap cannot receive one — putting
addresses there would help only devices that did not need them. The token
travels out of band, which is the whole point of it.

The token is already a URI with query parameters:

```
logosvpn://enrol?token=<26 chars>
```

so `&boot=<multiaddr>` is backward compatible: a parser that does not know the
key ignores it and uses the fleet, exactly as today.

## What this makes possible

A mesh whose members bootstrap from each other **does not need the public fleet
at all**. The first device uses it (or an invite from somebody who did), and
after that the fleet is one entry in a list rather than the only one.

That is a larger change than the bug that prompted it. It means:

- A fleet outage stops being able to take a mesh down.
- A censor blocking the Logos bootstrap nodes does not block a mesh whose
  members carry their own — which is the one piece of
  [where-next.md](../where-next.md)'s bootstrap problem that is ours to fix
  rather than the ecosystem's.
- "Nothing to run" from ADR-004 becomes "nothing you *must* run", which is a
  weaker and more honest claim.

## Costs and risks

**Crowd cover.** ADR-004 valued the public fleet partly for the anonymity of
publishing among other applications' traffic. A mesh bootstrapping only from its
own nodes is an island, and its anonymity set is its own devices. SECURITY.md
already concedes this is worth little — "the mesh's anonymity set on its own
private topic is its own devices" — so the loss is smaller than ADR-004 assumed,
but it is real and it should be a choice rather than a default.

**A member can influence where you bootstrap.** Announces are authenticated, so
only members can publish an address, and a hostile one cannot forge announces —
they are signed and sealed under the network key. What they could do is offer a
node that feeds selectively: an eclipse, not a forgery. Mitigations are ordinary
— keep several addresses, prefer recently-seen, and never discard the configured
ones in favour of learned ones.

**Stale addresses.** The libp2p port is assigned at random on each start and
nothing in Shrooms pins it. A Core node that restarts publishes its new address
within 45 seconds, so connected peers stay current; a peer that is *offline*
across that restart wakes with a stale entry. Keeping several addresses covers
most of it. Pinning the port would close it properly and is worth doing
separately — it is a config field and one plumb into the Waku node.

**Invite size.** A multiaddr with a peer ID is about 90 characters. Two or three
fit comfortably in a QR that still scans on a phone; a dozen would not.

## Consequences

- A restart stops being the moment a node can lose its network.
- `entry_nodes` stops being an escape hatch somebody has to know about and
  becomes the mechanism, with the fleet as one default entry.
- A mesh gains the option — not the obligation — to run entirely on
  infrastructure its own members provide.

## What would change our mind

The fleet publishing a stable, maintained bootstrap that does not refuse
connections would make part 3 unnecessary, though parts 1 and 2 remain worth
having: they are what turn a restart from an outage into an event nobody
notices.
