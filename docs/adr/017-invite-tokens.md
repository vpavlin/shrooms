# 017. Invite tokens

**Status:** proposed — the enrolment half of [ADR-018](018-credentials-instead-of-a-shared-key.md)

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

The request carries the joiner's device and X25519 public keys, a nonce and a
timestamp, sealed under `payload_key`. The response carries the network key
sealed to the joiner's X25519 key *and* under `payload_key`, so the bus sees
only ciphertext and only the intended device can open it.

128 bits is far beyond guessing, and there is nothing to guess against: a wrong
token addresses a topic nobody is listening on.

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
