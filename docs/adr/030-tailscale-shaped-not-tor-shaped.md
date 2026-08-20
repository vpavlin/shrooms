# 030. Tailscale-shaped, not Tor-shaped

**Status:** accepted. Scopes four open design notes; nothing to build, several
things not to.

## Context

A run of design notes asked, in effect, how far this project should go towards
being a privacy tool rather than a private network:

- [blind-relays.md](../blind-relays.md) — can strangers forward for a mesh?
- [obfuscation.md](../obfuscation.md) — can the tunnel stop looking like
  WireGuard?
- [disappearing.md](../disappearing.md) — can the data plane be as private as
  the control plane?
- [where-next.md](../where-next.md) — what would serve someone behind a national
  firewall?

Each is individually reasonable. Taken together they describe a different
product, and the last one named the fork: direct tunnels between your own
machines, fast, relays as fallback — or always-on layered relaying, slower,
private against a local observer, dependent on volunteers carrying traffic.

## Decision

**Stay in the first shape.** Shrooms is an overlay network between machines you
control. It is not a circumvention tool and should not be presented as one.

Specifically, and so this is not reopened one note at a time:

- **No exit nodes.** Traffic to the open internet does not route through peers.
- **No always-relay mode**, no layered two-hop relaying, no onion routing.
- **No mixnet**, which [ADR-011](011-no-mixnet.md) already decided and nothing
  since has changed.
- **No tunnel obfuscation** in the sense of looking like TLS or QUIC.

## Why

**The performance premise is the product.** [ADR-001](001-wireguard-not-libp2p-streams.md)
chose WireGuard for speed and every relay hop spends some of it. A mode that
gives up direct paths is a second product wearing the first one's name, and the
projects shipping both consistently find that most people use the fast one.

**The honest reason, though, is what the other audience deserves.** Somebody
relying on this to get past a national firewall is taking a risk on our
engineering. What we could offer them today is a prototype with no external
audit, a threat model written for a different user, and a bootstrap path that a
censor can block — and the failure mode is not "the VPN is slow", it is a person
exposed. Being unable to serve that user well is a reason to say so plainly
rather than to ship something adjacent and let them draw their own conclusions.

**The gap was never obfuscation anyway.** There is no exit node, so the traffic
that user needs to carry cannot be carried at all. Obfuscating a tunnel that
does not go where somebody needs is polishing the wrong thing, and it would have
been easy to build the polish and miss that.

## What stays in scope

This scopes the ambition, not every idea in those notes. Two survive on ordinary
usability grounds and one on hygiene:

**Blind relays** ([blind-relays.md](../blind-relays.md)) stay live. "I have no
publicly reachable machine" is an everyday problem for anyone whose devices are
all behind NAT — Tailscale runs DERP for exactly this — and it is not a
censorship feature. If built, the invite carries the relay, because a blind
relay cannot announce itself and therefore cannot be discovered.

**Blinded relay registration** stays worth doing if blind relays are: a relay
operator holding opaque per-relay tags instead of real tunnel keys is a
straightforward improvement over one who can recognise a device across meshes.

**Dropping the `mvpn` prefix** ([obfuscation.md](../obfuscation.md)) stays worth
doing, and not as obfuscation. Every control packet currently ships four literal
ASCII bytes naming the software, in the clear, on the tunnel port — so we are
more identifiable than plain WireGuard, which is a fingerprint nobody chose and
no threat model asked for. Deriving the prefix from the network key removes it.
That is hygiene, and it would be right even if no censor existed anywhere.

## What would change our mind

An exit node is the hinge. If it were ever in scope, the rest of that group
becomes coherent rather than premature — and the order in
[where-next.md](../where-next.md) holds: exit first, then bootstrap resilience,
then obfuscation, then finding people willing to carry the risk.

Also worth revisiting if Shrooms becomes transport for other Logos applications
([where-next.md](../where-next.md), decision 2, still open). That direction is
compatible with this one: it uses the socket interface
[ADR-025](025-control-from-a-desktop-app.md) already built, and needs none of
the privacy machinery declined here.
