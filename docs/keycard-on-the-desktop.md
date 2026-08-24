# Inviting from Basecamp with a Keycard

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
`m/44'/60'/0'/0`, signing a digest — is ordinary Go in `mobile/keycard.go`, and
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

**But its current API is the wrong shape for this, and its own documentation
says so.** Today it is an auth and key-derivation service: `requestAuth(domain,
caller)` derives a domain-scoped secp256k1 key on-card and **returns the raw
32-byte private key to the calling module**.

Shrooms must not use that. The whole point of
[ADR-022](adr/022-keycard-for-the-admin-key.md) is that the mesh's authority key
never leaves the card — a card signs a digest and the key stays put. A call that
hands back the private key would put the authority for a mesh into
`shrooms_core`'s memory, which is worse than the passphrase-protected file it
was meant to improve on.

`KEYCARD_SIGNING_MODES.md` in that repo already makes this argument, quoting
guylouis: *"keycard-basecamp needs to be more than an auth/key-derivation
service — it also needs to sign request for signature coming from LEZ wallet (or
other modules)"*, and noting that handing raw keys to consumers "distributes the
surface across every consumer module". Status there is **"Research complete,
implementation not yet started."**

**The good news is that what shrooms needs is the already-supported path, not
the unmerged one.** That document is mostly about BIP340 Schnorr for LEZ, which
needs an applet branch mikkoph distributes by hand. Shrooms needs plain
**secp256k1 ECDSA over a 32-byte digest** — `SIGN P2=0x00`, the current
behaviour — and the vendored `keycard-qt` SDK already exposes it:

> `signWithPath()` returns 65 bytes: R(32) ‖ S(32) ‖ V(1)

`internal/cred/card.go` already turns exactly that into what this codebase
verifies. So no new cryptography, no new applet, no waiting for Schnorr.

**So this is a patch to write, not a request to make.** The module's whole
exposed surface is `requestAuth`, `checkAuthStatus` and `getCardPresence` for
consumers, plus a UI-only set — `deriveKey(domain)` is the closest thing to
signing and it derives. Nothing in it signs. But the card signs, the vendored
SDK exposes `signWithPath()`, and there is already a fork with changes in it.

A `signDigest(path, digest)` alongside `requestAuth` — returning a signature,
not a key — is a thin passthrough to a call that is already there. Add it to the
fork, use it, offer it upstream when convenient. It is the thing
keycard-basecamp's own roadmap says it should have, so upstream is a
conversation about timing rather than about whether.

What cannot be skipped is the method itself: `requestAuth` hands back a private
key, and shrooms cannot accept one. Something has to sign on the card, and
today nothing in the module's API does.

**One constraint to carry into the UI**, from the same document: a card is
loaded in one mode or the other, standard BIP32 or LEE, and only one at a time.
A card set up for LEZ cannot produce the keys used here, and a user who wants
both needs two cards. That is a property of the applet rather than a choice
anybody made, and it is exactly the sort of thing that presents as "my card
stopped working".

## Suggested order

1. **Move `mobile/keycard.go` to `internal/keycard`.** Mechanical, useful on its
   own, and everything else depends on it.
2. **Settle the tier**, in an ADR: may the group tier hold and relay an invite,
   given that the signature is off-daemon?
3. **Add `signDigest` to the keycard-basecamp fork** — a passthrough to
   `signWithPath()`, which is already vendored. Its absence, not the transport
   and not the card, is what blocks this.
4. **Then the transport and the UI**, which is the smallest part of this.

## One thing this does not change

The desktop already has two ways to sign with something the daemon cannot reach:
`shrooms admin issue --sign-with <command>` and `--external-signer`, which print
a digest and read a signature back ([ADR-022](adr/022-keycard-for-the-admin-key.md)).
A Basecamp invite flow is a nicer front on the same idea, not a new capability —
and if the tier question turns out to be contentious, printing the command
remains the honest fallback, which is what `shrooms_core` already tells a view
to do.
