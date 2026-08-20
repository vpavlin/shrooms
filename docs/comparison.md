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

## The closest thing to this: Nostr VPN

[mmalmi/nostr-vpn](https://github.com/mmalmi/nostr-vpn) deserves its own section
rather than a column, because it is the same idea reached independently — and
its author's stated motivation is ours almost word for word: *"Got annoyed by
Tailscale requiring 3rd party accounts, so created Nostr VPN."*

Read from its README and `docs/protocol.md` on 2026-08-20, not from summaries:

| | Shrooms | Nostr VPN |
|---|---|---|
| coordination service | none | none |
| signalling substrate | Logos Delivery (Waku) | Nostr relays |
| how much rides it | continuous announces, every 45s | enrolment and roster delivery only |
| data plane | wireguard-go, userspace | FIPS, a Rust WireGuard |
| identity | ed25519 device key | Nostr keypair (npub) |
| addressing | derived from the device key, IPv6 /48 | derived: `SHA256(network_id + "\n" + pubkey)` → `10.44.x.y/32` |
| membership | admin-signed credential, expires | admin-signed roster, from a signed join request |
| revocation | signed, gossiped, re-announced each epoch, survives restart | not documented |
| relay fallback | a peer that announces itself as one | "through FIPS neighbors when direct UDP is blocked" |
| platforms | Linux, Android | macOS, Linux, Windows, Android, **iOS**, StartOS/Umbrel |

### Where they are ahead

**iOS**, which we have declared out of scope
([ADR-022](adr/022-keycard-for-the-admin-key.md)). The reason is worth understanding
rather than resenting: our blocker was never WireGuard, it was fitting a libp2p
node inside a Network Extension's memory limit. Nostr signalling is websockets
to a relay, which costs almost nothing, so the constraint that stopped us does
not apply to them. That is an argument about substrate, not effort.

**Platform coverage generally**, and release cadence — eleven releases in seven
days at one point.

### Where the designs genuinely differ

**How much depends on somebody else's infrastructure.** This is the one where a
casual reading would get it backwards. We announce continuously over a public
Waku fleet; they use Nostr relays for *enrolment and roster delivery only*, with
peer discovery delegated to the FIPS layer. Their protocol document is explicit
that it "should not publish or consume its old Nostr relay peer announcements."
So in steady state they lean on third-party infrastructure less than we do, not
more — and the outage on 2026-08-20, when five of six Waku entry nodes refused
connections and a restarted node could not rejoin, is exactly the class of
failure that buys.
[ADR-031](adr/031-bootstrap-from-the-mesh-itself.md) is our answer to it.

**Address space.** Both derive addresses from keys, which removes the allocator.
Theirs lands in `10.44.0.0/16` through two modulo-254 bytes — about 64,500
possible addresses, so collisions become likely somewhere in the low hundreds of
devices by the birthday bound. Ours is a 64-bit interface identifier inside a
derived /48, where a collision is not a thing that happens. For a personal mesh
neither matters; it is a difference in what the design will tolerate later.

**Revocation.** Ours is built and deliberate: signed by the authority, gossiped,
repeated each epoch and when a peer appears, persisted across restarts, and
bounded by an expiry the revocation itself carries
([ADR-018](adr/018-credentials-instead-of-a-shared-key.md)). Theirs may exist;
it is not in the README or the protocol document, and we have not read the
source. "Not documented" is the honest claim, not "absent".

### The honest summary

Two projects, the same objection to Tailscale, the same shape of answer, and
different substrates underneath. They are further along as a product. We have
thought harder about what happens after somebody is admitted — expiry,
revocation, per-device credentials — and they have thought harder about being
installable on the machine somebody actually owns.

Neither of those is a moat, and it would be silly to pretend otherwise.

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
