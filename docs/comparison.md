# How Shrooms compares

Asked on a livestream: *"this is not new and there is no magic — just existing
protocols layered over each other."*

That is correct and worth conceding first. WireGuard carries the traffic. Path
selection is ICE's idea. Reflexive addresses are STUN's. Credentials are
ed25519, relays are the TURN pattern, gossip is libp2p's. There is no new
cryptography and no new protocol here, and a claim otherwise would be trivial to
falsify.

The claim is about a component that is **absent**, not one that is new.

## The table

| | Shrooms | Tailscale | Nebula | ZeroTier | Yggdrasil | WireGuard alone |
|---|---|---|---|---|---|---|
| **Coordination service** | none | Tailscale's (or self-host Headscale) | lighthouses **you** run | root servers ZeroTier runs; moons self-hostable | none | none |
| **Who can see your topology** | nobody | Tailscale, or you if self-hosted | you | ZeroTier, or you with moons | nobody | you |
| **Data plane** | WireGuard | WireGuard | Noise (own protocol) | own L2 overlay | own encrypted IPv6 routing | WireGuard |
| **Peer discovery** | public gossip bus (Waku), rendezvous only | coordination server | lighthouses | roots/controller | link-local + configured peers | manual config |
| **Traffic transits other members** | no | no | no | no | **yes** — it is a routing mesh | no |
| **NAT traversal** | yes | yes, best in class | yes | yes | via peers | no |
| **Relay fallback** | discovered from its own announce | DERP, run by Tailscale | relays you configure | roots/moons | inherent to routing | none |
| **Addressing** | derived from device key | assigned by coordinator | assigned in cert | assigned by controller | derived from public key | manual |
| **Membership** | credential signed by an admin key | SSO/OIDC identity | CA-issued certificate | controller authorises | open network | whoever holds a key |
| **Revocation** | signed, gossiped, re-announced, survives restart | via coordinator | blocklist (documented gap) | via controller | n/a | remove the peer by hand |
| **Admin key location** | a machine that can be **off** | the coordinator | your CA machine | the controller | n/a | n/a |
| **Mobile** | Android (full peer) | iOS + Android | iOS + Android | iOS + Android | Android | via app configs |
| **Maturity** | **prototype** | production, large scale | production | production | mature | production |

## What is actually distinctive

**Nobody runs a service.** Tailscale operates a coordination server; Nebula
needs lighthouses you keep alive; ZeroTier has roots. In every case some host
knows the membership and topology, and forming *new* connections depends on it.
Shrooms uses a public gossip network for rendezvous only, so there is no
account, no control plane, and no host whose disappearance takes the network
with it.

**Rendezvous is not on the data path.** Once tunnels exist, the gossip bus can
vanish and traffic keeps flowing — demonstrable by killing it live. In a
coordinator design, existing tunnels also survive an outage, but the coordinator
still saw the whole graph while it was up. Here nothing ever did.

**The authority is off the always-on machine.** The admin key lives on a laptop
or a smartcard, not on the relay. The relay — the one permanently online box —
has no power over membership at all, which is why it can be a €4 VM that gets
deleted afterwards. Most overlays put authority and uptime on the same host.

**Addresses come from keys.** Shared with Yggdrasil and cjdns, and it removes an
allocator: nothing to assign, nothing to collide, no state to lose.

## Where Shrooms loses

Worth stating plainly, since the point of this page is credibility.

- **It is a prototype.** Tailscale, Nebula and ZeroTier are production systems
  with real operational history. This is not.
- **No iOS.** The extension memory limit rules out the current design (ADR-022).
  The others all ship iOS clients.
- **NAT traversal is good, not great.** Tailscale has spent years on this and it
  shows. See `m2-punch-deadlock.md` for a case Shrooms does not yet handle.
- **The gossip bus is a real dependency.** Not a coordinator — nobody runs it
  for us and it holds no authority — but if it is unreachable, *discovery*
  stalls and new peers cannot find each other. Established tunnels are
  unaffected. Calling this "no dependencies" would be dishonest; the accurate
  claim is "no dependency that anybody operates on your behalf".
- **Metadata.** Content topics and timestamps are cleartext on that bus, and
  directly-connected relay peers see your IP. See SECURITY.md.
- **No audit.** The cryptography is standard and the compositions are ordinary,
  but nobody independent has looked.

## The honest positioning

Every part is old. The combination is not common: a WireGuard data plane, no
coordinator, revocable credential membership, key-derived addresses, and a phone
that is a full peer rather than a viewer.

And the work was never in inventing a protocol. It was in the constraints —
the iOS memory limit, a wire format flag day, a conntrack deadlock between two
NATs — because those are the pressures that make a project quietly grow a
coordination server. Not growing one is the contribution.
