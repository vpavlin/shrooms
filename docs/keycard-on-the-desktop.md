# Inviting from Basecamp with a Keycard

> **Update, 2026-08-26.** The first half of this is built: the card protocol
> moved out of `mobile/` into `internal/keycard`, a PC/SC transport sits behind
> `-tags pcsc`, and `shrooms keycard` plus `admin init --keycard` drive a card
> from a reader. What remains open is the part this document calls a decision
> rather than a build — whether the group tier may hold an invite — which is
> still unanswered and still wants an ADR.

Vaclav, 2026-08-24: *"I would also like to support Basecamp invites via a keycard
module — we got keycard integration working for Scala."*

Worth doing, and the pieces are further along than they look. Three things are
in the way and only one of them is a decision.

## Why Basecamp cannot invite today

Two separate reasons, and they are usually confused with each other.

**The daemon has no key to sign with.** `shrooms_core` says so where somebody
would go looking: an invite is two halves ([ADR-017](adr/017-invite-tokens.md)) —
the daemon holds the exchange open, and the admin key signs the credential that
comes out of it. The desktop's admin key is a passphrase-protected file in a
user's session, and the daemon has never held it. That separation is what makes
handing the control socket to a group a bounded grant rather than a way to admit
anybody ([ADR-025](adr/025-control-from-a-desktop-app.md)).

**And Basecamp cannot even hold the invite.** `/invite/hold` and `/invite/reply`
are both `requireRoot` (`cmd/shrooms/daemon.go`). Basecamp reaches the socket in
the *group* tier, so it is refused before signing ever comes up.

A card changes the first and not the second, which is why the second needs
deciding rather than building.

## What is already portable, and what is not

**The seam is one method.** `CardTransport` is:

    Transmit(apdu []byte) ([]byte, error)

Everything above it — pairing, the secure channel, deriving at
`mobile.KeycardPath`, signing a digest — is ordinary Go in `mobile/keycard.go`, and
none of it knows what carries the bytes. The phone's implementation is NFC. A
desktop's would be PC/SC. That is the whole difference.

**The conversions are already shared and already tested.**
`internal/cred/card.go` turns a card's 65-byte uncompressed point into the
33-byte compressed form an authority is written with, and its minimal-length
`r`/`s` into a fixed 64-byte signature — with the reasoning that this is
"exactly the kind of small that fails silently" and vectors rather than
hardware to prove it. None of that needs doing twice.

**But the protocol is in the wrong module.** `mobile/keycard.go` lives inside
`mobile/`, which is a separate Go module with its own `go.mod`, so nothing on
the desktop side can import it. 247 lines, one external dependency
(`keycard-tech/keycard-go`), no Android in it. Moving it to `internal/keycard`
is mechanical and is the first step regardless of everything else here.

## The decision: may the group tier hold an invite?

`/invite/hold` and `/invite/reply` are root today. The question is whether they
can be group.

**The case for yes.** With a card, neither call admits anybody:

- `/invite/hold` subscribes to a topic named by a token and waits. It hands back
  a joining device's public keys. It grants nothing.
- `/invite/reply` publishes a credential *the caller supplies*. A credential
  with no valid admin signature is refused by every peer that receives it, so
  relaying one is not the same as issuing one.

The thing that admits a device is the signature, and the signature comes off a
card the daemon has never seen and cannot reach. Widening these two does not
move that line — it moves who may *carry* the message, which is the same grant
the group tier already has for everything else it publishes.

**What it does cost, stated plainly.** A group-tier caller could hold invites
and see the public keys of devices trying to join, and could occupy a token's
topic. Both are noise rather than access, and both are already available to
anyone who can read the socket's status.

**What would have to stay root.** Nothing else moves. `/revoke`, `/grant` and
`/members` are a different question and are not part of this.

This wants an ADR, because ADR-025 drew the line here deliberately and "a card
changes the argument" is exactly the sort of reasoning that should be written
down rather than inferred from a diff.

## Where the card code should live

The module is [`xAlisher/keycard-basecamp`](https://github.com/xAlisher/keycard-basecamp),
"Keycard smartcard authentication module for Logos Basecamp", forked at
`vpavlin/keycard-basecamp`. It is a C++ Logos module reached the ordinary way —
`logos.callModule("keycard", ...)` — which is exactly the shape hoped for above:
shrooms would not open a reader itself, and two modules would not fight over
one.

**And it already signs.** `KEYCARD_API.md` documents only `requestAuth` and
`checkAuthStatus`, which is what led an earlier version of this note to conclude
that a patch was needed. The documentation is behind the code. `keycard-core`
exposes a full signing flow, mirroring the auth one:

    requestSign({domain, payloadHash, caller, scheme, bip32_path?})
    checkSignStatus(signId)  ->  {status, signature}
    getPendingSigns() / approveSign({signId, pin}) / rejectSign(signId)

Every property this needs is already there:

- **`scheme` accepts `"ecdsa"`**, which is what this codebase verifies. The
  Schnorr work in `KEYCARD_SIGNING_MODES.md` is for LEZ and is not on our path.
- **It takes a `payloadHash`**, not a payload — a digest is exactly what
  `Credential.Digest()` and `Rotation.Digest()` produce, and exactly what
  [ADR-022](adr/022-keycard-for-the-admin-key.md) built the `Signer` seam around.
- **`bip32_path` is settable**, so shrooms' own path can be asked for by name.
  It must be: the authority is derived at `m/64265'/0'/0'`, not at the wallet
  path, and a signer defaulting to `m/44'/60'/0'/0` would sign with the wrong
  key and produce credentials nothing verifies.
- **It returns a signature and never a key.** `approveSign` calls
  `signWithPath(hash, path, false, P2)` on the card and hands back hex; the read
  is one-shot and the buffer is wiped after. The private half never leaves the
  card, which is the property this whole design depends on.
- The 65 bytes are R‖S‖V, and `internal/cred/card.go` already drops the recovery
  byte — "which matters to Ethereum and not to us" — leaving the 64-byte r‖s
  this codebase verifies.

**So there is no card work to do, and nothing to ask anyone for.** Shrooms does
not need its own reader, does not need the Keycard protocol on the desktop at
all, and does not need `mobile/keycard.go` moved out of the mobile module for
this — signing is delegated, so the protocol stays where it is used.

What is left is wiring and one decision: `shrooms_core` calls `requestSign` and
polls `checkSignStatus`, and the invite tier has to allow Basecamp to hold the
exchange in the first place.

**Do not use `requestAuth` for this.** It derives a key and returns the raw 32
bytes to the caller, which is the right shape for a module doing bulk encryption
and the wrong one for an authority key. `requestSign` exists precisely because
of that distinction, and the module's own
`KEYCARD_SIGNING_MODES.md` argues the point at length.

**One constraint to carry into the UI**, from the same document: a card is
loaded in one mode or the other, standard BIP32 or LEE, and only one at a time.
A card set up for LEZ cannot produce the keys used here, and a user who wants
both needs two cards. That is a property of the applet rather than a choice
anybody made, and it is exactly the sort of thing that presents as "my card
stopped working".

## Suggested order

1. **Settle the tier**, in an ADR: may the group tier hold and relay an invite,
   given that the signature is off-daemon? This is the only blocker left, and
   the only thing here that is a decision rather than work.
2. **Wire `shrooms_core` to `requestSign`/`checkSignStatus`**, dropping the
   recovery byte and handing the 64-byte r‖s to the existing credential path.
3. **The UI**, which is the smallest part.

Nothing needs moving out of `mobile/`, and nothing needs adding to
keycard-basecamp.

## One thing this does not change

The desktop already has two ways to sign with something the daemon cannot reach:
`shrooms admin issue --sign-with <command>` and `--external-signer`, which print
a digest and read a signature back ([ADR-022](adr/022-keycard-for-the-admin-key.md)).
A Basecamp invite flow is a nicer front on the same idea, not a new capability —
and if the tier question turns out to be contentious, printing the command
remains the honest fallback, which is what `shrooms_core` already tells a view
to do.
