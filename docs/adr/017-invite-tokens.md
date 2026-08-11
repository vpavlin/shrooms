# 017. Invite tokens

**Status:** accepted, built — the enrolment half of [ADR-018](018-credentials-instead-of-a-shared-key.md)

Built as described, with one change and one addition. `internal/invite` is the
crypto and wire format; `shrooms invite` and `shrooms join --invite` are the two
ends. Verified end to end over logos.test: token to enrolled device in about
twenty seconds, and the same token afterwards times out with nothing written.

## Context

The network key is the only credential there is (ADR-008). It derives the
rendezvous topics, the payload key, the pairwise WireGuard PSKs and the address
prefix, so every member must hold it — and holding it is what membership *is*.

That was a deliberate v1 simplification. Its consequences are now concrete
rather than theoretical:

- **Adding a device means transmitting the credential.** Read aloud, pasted into
  a chat, or shown as a QR code on a screen. It has been on a screen.
- **It is on a phone**, which can be lost.
- **There is no revocation.** Removing one device means rotating the key, which
  ejects every device — laptop, VPS, phone, test containers — and re-enrolling
  each by hand.
- **Every member can mint members.** Any machine holding the key can enrol more,
  so one compromise is total.

`shrooms prepare` narrows who *handles* the key during setup. It does not
change any of the above.

## Decision

**A device joins with a one-time, short-lived invite rather than the mesh key.**
The key is delivered to it over an authenticated channel, encrypted to that
device alone.

```
existing member                              joining device
──────────────                               ─────────────
shrooms invite
  → prints a token + QR, valid 15 minutes
  → subscribes to a topic derived from it
                        ── token, by QR or typed ──▶
                                             shrooms join --invite <token>
                        ◀── request: device pubkey, sealed ──
verifies, marks the invite used
  → replies with the mesh key, sealed to
    the joiner's key alone
                        ── mesh key ──▶
                                             writes config, joins normally
```

### Single-use needs no consensus

The property that looks hardest is the one the design gets for free.

An invite is answered **only by the member that issued it**, because only that
member is listening on the topic the token derives. "Used once" is therefore a
local decision by one machine, not an agreement among peers — no distributed
state, no race, nothing to reconcile on an eventually-consistent bus. The same
is true of expiry.

This is why the inviter must be online during the join. That is a real
constraint and it is also a feature: enrolment happens when a human is present
and paying attention, rather than whenever a token is replayed.

### Derivation

```
secret        128 random bits, base32 — 26 characters, typable
topic_key   = HKDF(secret, "invite/v1/topic")     where to listen
payload_key = HKDF(secret, "invite/v1/payload")   what to encrypt with
```

The request carries the joiner's device and WireGuard public keys, a **fresh**
X25519 key and a timestamp, sealed under `payload_key`. The response carries the
network key sealed to that X25519 key *and* under `payload_key`, so the bus sees
only ciphertext and only the device that asked can open it.

The ephemeral key is generated for the exchange rather than being the device's
tunnel key. The tunnel key is a Curve25519 key and would have worked; a fresh
one costs nothing and means there is no cross-protocol key reuse to argue about.

Both directions are padded to 1024 bytes, so a request, a response, and a
response carrying a credential are the same size on the bus. The topic is built
with the rendezvous application and version fields, so it lands on the shard the
mesh's traffic already uses (ADR-006) rather than on one of its own.

128 bits is far beyond guessing, and there is nothing to guess against: a wrong
token addresses a topic nobody is listening on.

### Where each end runs

**The inviting end is the daemon, not the CLI.** `shrooms invite` first ran a
Logos Delivery node of its own, which worked and cost three seconds of dialling,
a page of library logging, and a second Core node joining the fleet to send two
messages. The daemon is already connected, so it holds the topic and publishes;
the CLI mints the token and signs.

That split falls out of the same reasoning the admin key exists for. The daemon
has the network key and the connection and never sees the admin key; the CLI has
the admin key and never sees the network key. Neither half can admit a device
alone, and the daemon will only ever publish a well-formed response on a topic
derived from a token it was handed — so a caller who can reach the control
socket has not gained a general-purpose way to publish.

The cost is that `shrooms invite` requires a running daemon. That is not much of
a constraint, since it can only invite people to a mesh it is already on.

**The joining end cannot do the same**, because a device that has not joined
anything has no daemon and no config for one. It runs a node for the length of
the exchange and throws it away. Two consequences worth writing down: the join
takes a few seconds to connect before it can ask, and the rendezvous library
logs at INFO to **stdout** — not stderr, and its `logLevel` config key is
accepted and ignored, measured identical at FATAL, ERROR and the default. So
`join --invite` points fd 1 at `/dev/null` for the length of the exchange and
keeps the real descriptor for its own three lines. `-v` gets the logs back.

### One round is not enough for per-mesh identities

Found building the mobile side. The request carries the joining device's keys,
and the inviter issues a credential naming them — but the device cannot know
*which* mesh it is joining until the response arrives, so it cannot send a
per-mesh identity ([ADR-015](015-multiple-meshes-one-daemon.md)). It sends its
base identity, and the credential names that.

The consequence: a mesh joined by invite uses the device's base identity, so a
device on two such meshes presents the same device key to both, and anyone in
both can tell it is the same device. That is precisely the linkability ADR-015
set out to close.

Fixing it needs a second round — learn the mesh, derive the identity for it,
then ask for the credential — which is a wire-format change to this ADR and is
not built. Written down because the alternative is a surprise later, and
because the workaround (joining an additional mesh with its network key, where
the mesh is known before the identity is chosen) already avoids it.

## What this does not fix

**It does not remove the shared key**, which is the thing actually worth
removing. A joined device holds the same network key it would have been sent by
hand, so a leak from any device is still total and revocation still means
rotating for everyone. Invites reduce how far the credential travels; they do
not change what it is.

[ADR-018](018-credentials-instead-of-a-shared-key.md) is the design that does,
and this is its enrolment half — the exchange below is exactly where a
credential would be issued instead of a key. Worth building first because it is
useful either way and much smaller, but it should not be mistaken for the fix.

**Anyone with the token can join.** The token *is* the authorisation, so an
attacker who photographs the QR within the window can enrol. Expiry and
single-use bound that to minutes and one device; today the equivalent exposure
is permanent.

**A member can still mint members.** Any node can issue invites. Restricting
that requires the admin key M5 introduces.

## Alternatives

**Ship the key by QR and stop there.** Already possible, and what
`key show --qr` does. The credential is still permanent, still transmitted, and
a photograph of the screen is enough forever.

**Go straight to M5 credentials.** The right destination, and much larger: an
admin key, signed per-device credentials, revocation distribution, and a
migration for existing meshes. Invites are a step on that path rather than a
detour — the enrolment exchange is where a credential would be issued.

**Password-authenticated key exchange (SPAKE2 or similar).** Would let a short
human-typable code resist an active attacker rather than only a passive one.
Worth revisiting for M5; heavier than warranted while the thing being protected
is a shared key that every member already has.

## Consequences

- The mesh key stops being read aloud, pasted, or left on screens. It moves
  once, encrypted, to one device.
- Enrolment gains a liveness requirement: the inviter must be running. Worth
  saying in the interface, since "nothing happened" is otherwise the failure.
- `shrooms join <KEY>` stays, because bootstrapping the first machines and
  recovering a mesh both need it.
- The invite exchange is the natural place for M5 to issue a credential, so the
  wire format should leave room for one rather than being minimal now.
- **It carries the credential, and that is what makes it worth building.** The
  response is the network key, the mesh's `admin_keys` and a credential signed
  for the joining device's keys — so `invite` needs the admin key, and enrolment
  is one command on each machine instead of six and a copied blob.
- The response is sent once. Waku may lose it, and the failure is visible and
  cheap: the joining device says nobody answered, and you run `invite` again.
  Answering repeatedly would quietly turn a single-use token into a reusable
  one, which is the property the whole design rests on.
