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

Vaclav notes Keycard integration already works for Scala. That matters for the
shape rather than the code: if Basecamp has, or could have, a **Keycard module**
that exposes card access to other modules, then shrooms should use that rather
than opening a reader itself — two modules fighting over one card reader is a
bad afternoon, and PC/SC does not enjoy being opened twice.

Looked, and could not find it. `vpavlin/logos-basecamp-modules` is an empty
catalog — its `.gitmodules` still has only the instructions for adding one.
`vpavlin/loam-keycard` is the one this codebase already nods to, and it is
TypeScript over raw NFC, which is neither the language nor the transport a
desktop module needs. The Scala fork is presumably private or under an
organisation a public search does not reach.

So the question to settle before writing any PC/SC code, and it needs the URL:
**does the Scala integration expose a reusable module, or is it internal to that
app?** If the
former, `shrooms_core` gains a dependency and a `CardTransport` that forwards to
it. If the latter, shrooms carries its own reader and the two applications must
not be used with the same card at the same time — worth saying out loud in the
UI if that is where this lands.

## Suggested order

1. **Move `mobile/keycard.go` to `internal/keycard`.** Mechanical, useful on its
   own, and everything else depends on it.
2. **Settle the tier**, in an ADR: may the group tier hold and relay an invite,
   given that the signature is off-daemon?
3. **Settle where the card lives**: the Scala module, or a reader of our own.
4. **Then the transport and the UI**, which is the smallest part of this.

## One thing this does not change

The desktop already has two ways to sign with something the daemon cannot reach:
`shrooms admin issue --sign-with <command>` and `--external-signer`, which print
a digest and read a signature back ([ADR-022](adr/022-keycard-for-the-admin-key.md)).
A Basecamp invite flow is a nicer front on the same idea, not a new capability —
and if the tier question turns out to be contentious, printing the command
remains the honest fallback, which is what `shrooms_core` already tells a view
to do.
