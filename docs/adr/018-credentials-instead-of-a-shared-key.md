# 018. Credentials instead of a shared key

**Status:** accepted, built except for the announce rewrite. The destination
[ADR-017](017-invite-tokens.md) is a step toward, and its enrolment channel.

Built: `internal/cred`, the `admin` commands, expiry, revocation, issuance
inside the invite exchange, and renewal as a sweep (below). Not built,
deliberately: the per-recipient announce wrapping, for the reasons in
[ADR-020](020-membership-is-a-seam.md). Not built: renewal without a person,
which needs a key that is online and is therefore its own decision.

**Renewal is verified by unit tests only.** The `admin renew` sweep and the
`KindGrant` relay have not been run against the live mesh; issuance through the
invite exchange has. Credentials last 30 days, so the first real test of the
sweep arrives whether or not it is scheduled.

## Context

Everything protecting the mesh derives from one secret. The network key gives
the rendezvous topics, the announce payload key, the pairwise WireGuard PSKs and
the address prefix (ADR-008), so every member must hold it — and holding it is
what membership *is*.

That conflation is the flaw, not the sharing. **The credential that grants
membership is the same object that must live on every device.** So:

- A leak from any device compromises the whole mesh, including devices that
  never went anywhere near the leak.
- Removing one device means rotating the key, which ejects every device and
  requires re-enrolling each by hand.
- Every member can enrol members, because every member holds the thing that
  authorises enrolment.
- The blast radius of the least-protected device is the entire mesh. Today that
  is a phone.

ADR-017's invites reduce how far the key *travels*. They do not change any of
the above, because the joined device ends up holding the same key. Worth being
blunt about: an invite scheme is easy to mistake for a credential system.

> **Two keys from birth, and a card can hold them.** The mesh id commits to a
> *set* of admin keys, fixed at mint, because the address prefix derives from it
> — adding one later re-addresses every node. Two earn their place immediately:
> recovery, and the renewal key that lets credentials refresh while the root
> stays offline. Signatures cover a SHA-256 digest rather than the body, so a
> Keycard can hold the root: a card signs a fixed-size input with the algorithm
> chosen per call (`P2SignEdDSAEd25519`), and with BIP-32 the keys are
> derivation paths on one card rather than separate artefacts.
>
> **Scope note (see [ADR-020](020-membership-is-a-seam.md)).** The
> per-recipient announce encryption below is *not* being built. It is the
> largest piece of this design, it scales worst, and it is exactly what MLS
> TreeKEM does properly — and Logos already has an MLS implementation in
> libchat. The shared payload key stays until that is a real option. What is
> being built is credentials, expiry and revocation, which earn their keep
> whatever happens to libchat.

## Decision

**Separate authority from participation.**

| | today | proposed |
|---|---|---|
| mesh identity | secret key | admin **public** key |
| membership | knowing the secret | an admin-signed credential |
| announce privacy | shared payload key | encrypted per recipient |
| WireGuard PSK | derived from the secret | derived pairwise |
| revocation | rotate, re-enrol everything | sign a revocation for one device |
| what lives on a device | the thing that grants membership | only its own credential |

The decisive property: **the admin key never has to be on a participating
device.** It is needed only to enrol and to revoke, so it can live offline — on
one laptop, in a password manager, on a hardware key. Today's network key must
be on every device *and* is what grants membership. Splitting those two roles is
the whole idea; everything below follows from it.

### What derives from what

```
admin keypair            created once; the public half IS the mesh identity
  ↓ signs
credential               {device_pub, mesh_id, issued, expires?, sig}
                         held by one device, proves membership, secret to nobody

mesh_id = SHA256(admin_pub)      public
prefix  = fd || mesh_id[0:5]     public, as today
topics  = f(mesh_id)             public
```

Topics and the prefix becoming public loses nothing: they are already visible to
anyone watching the shard, and they never protected anything. What they gain is
that a new device can compute them from the mesh id alone, before it has any
secret at all.

### Announces without a shared key

An announce is encrypted to a fresh symmetric key, and that key is wrapped to
each current member's X25519 key. Members are known — their credentials carry
their public keys.

Costs about 48 bytes per member. For a personal mesh that is nothing, and the
fixed padding (ADR §control-plane) becomes a function of membership size rather
than a constant, which is a small metadata leak worth naming: an observer learns
roughly how many devices are in the mesh. Padding to buckets blunts it.

This is the part that scales worst, and the reason this design suits a personal
mesh of tens of devices rather than an organisation of thousands. That is the
mesh this project is for.

### Revocation that means something

The admin signs `{revoked: device_pub, serial, not_before}` and publishes it on
the rendezvous topic. Members drop the device from the roster, stop wrapping
announce keys to it, and remove its WireGuard peer.

The revoked device keeps whatever it already had — nothing can reach into it —
but it stops receiving announces, so it cannot follow anyone who moves, and no
member will establish a new tunnel with it. Monotonic serials, as with
announces, so a revocation cannot be rolled back by replay.

### Renewal is a sweep, not a ceremony per device

Expiry is what makes a lost device survivable without every node having to hear
a revocation: an unrenewed device falls off the mesh by itself. The cost of
that guarantee is that somebody has to renew, and a system whose answer to "what
happens on day thirty" is "everything stops and nobody knows why" is worse than
one with no expiry at all.

So `shrooms admin renew` asks a running node who is on the mesh, signs a fresh
credential for every device inside `RenewBefore` of expiry, and hands them back
to that node to deliver. One command, occasionally, from the machine that
already holds the admin key.

**Delivery is a control message — `KindGrant`, the mirror of a revocation
travelling the other way.** Relayed by any member, for the same reason a
revocation is: a credential is public, holds nothing secret, and is worthless to
anyone but the device whose keys it names. A hostile relayer can drop one, which
is indistinguishable from being offline and ends exactly where expiry already
ends. It cannot forge one, because every node verifies the admin signature on
arrival against the authority that admits peers.

A device receiving its own keeps whichever credential lasts longer — a sweep may
reissue while an older one is still in flight — and announces immediately, so
peers stop checking against the credential it has replaced.

**What is deliberately not built is renewal with nobody present.** That needs a
signing key that is online, which is a different security posture from an admin
key used a handful of times a year, and it is the thing a Keycard
([ADR-022](022-keycard-for-the-admin-key.md)) makes awkward. The fixed
authority set already allows for a separate renewal key; adding one is a
decision to take on its own, not a side effect of making renewal work.

## What this costs

**The admin key becomes a single point of failure.** Losing it means no more
enrolment or revocation, and rebuilding the mesh. Mitigated by keeping a second
admin key offline from the start and honouring both — cheap to do now,
impossible to retrofit once devices hold credentials naming only one.

**More moving parts.** A key that must not be lost, credentials with lifetimes,
a revocation list to distribute and persist. The current design's one virtue is
that it is trivial to reason about, and that is genuinely worth something.

**Migration.** Existing meshes hold a shared key and nothing else. The path is
to enrol each device with a credential while the shared key still works, then
retire the key — which needs both schemes live at once, briefly.

## Alternatives

**Keep the shared key; make rotation cheap.** Credentials would let devices
re-enrol automatically after a rotation, so revocation becomes "rotate, everyone
but the revoked device recovers by itself". Much less work, and it still leaves
every device holding the thing that grants membership. Worth considering if this
design proves too heavy, but it treats the symptom.

**Per-pair manual trust, no authority at all.** Every device explicitly approves
every other. Truly no central secret, and the work grows with the square of the
mesh. Reasonable for three machines, unreasonable for ten.

**An existing framework — SPIFFE, X.509, Macaroons.** Correct instincts, all far
heavier than a mesh of personal devices needs, and each drags in a trust model
larger than the problem. The scheme above is roughly "X.509 with one CA and no
path building", stated in a page.

## Consequences

- A compromised device costs that device.
- The mesh key stops existing, so it cannot leak, be photographed, or be pasted.
- ADR-017's invites become the enrolment channel: the exchange that hands over a
  network key today is exactly where a credential is issued instead. Its wire
  format should leave room for one rather than being minimal now.
- `join <KEY>` disappears eventually. Bootstrapping the first device becomes
  `init`, which mints the admin key, and every device after that is invited.
