# Keycard as the mesh authority, from a phone

**Status:** assessment, not a decision. Two questions at the end are Vaclav's.

The goal: issue invites and revocations from a phone, with the admin key on a
Keycard rather than on any device. The phone is the machine always within reach,
and the security comes from the card, not from the phone being trusted.

## What already exists, on both sides

Shrooms was built for this and says so. `cred.Signer` is the seam:

```go
type Signer interface {
    Public() ed25519.PublicKey
    SignDigest(d [32]byte) ([]byte, error)
}
```

with a comment naming the second implementation before one existed: *"an admin
key held in a file, which is what exists today, and a Keycard, which is why this
is an interface at all."* Everything the package signs is a 32-byte digest,
chosen up front so a card could sign it. `internal/cred/secp256k1.go` already
accepts a secp256k1 authority, and `admin init --keycard` compresses the card's
exported point before writing it.

On the other side, `loam-keycard` already drives a card from a phone for Scala:
a raw NFC driver, enrolment, implicit-PIN unlock, tap-per-sign, and a
`"idle" | "tap"` state bus for the "hold your card" overlay.

**The wire formats already match, exactly.** Neither project knew about the
other's constraints, and they converged:

| Shrooms expects | loam-keycard produces |
|---|---|
| 33-byte compressed pubkey (uncompressed refused) | `compressPub()` → 33-byte |
| 64-byte `r‖s`, recovery byte dropped | `toCompactSig()` → 64-byte `r‖s` |
| a signature over a raw 32-byte digest, no re-hash | signs the raw digest, no re-hash |
| high-S or low-S both accepted, deliberately | normalises to low-S |

The low-S detail is worth noting because it nearly went wrong in both
directions. Shrooms deliberately does **not** require low-S: keycard-go does not
canonicalise `s`, so enforcing it would have made signing a coin flip, and there
is a test pinning that decision. loam-keycard normalises to low-S on the client.
Those are compatible — a normalised signature verifies fine against a verifier
that accepts either — but only by luck of ordering. Neither side should "fix"
its half without checking the other.

So the format work is done. What is left is runtime.

## The three gaps

**1. The phone cannot issue invites at all.** The mobile API has
`JoinWithInvite`, `InviteToken`, `InviteKey`, `InviteMeshName` — all
*redemption*. There is no minting.

An earlier draft of this document called holding the fifteen-minute window the
hard part. That was wrong, and the correction matters because it makes the
feature much cheaper than it looked. **The daemon already hosts invite windows.**
`shrooms invite` is a UI over the control socket: the daemon does the waiting and
reports a redemption. On a phone the Go core *is* that daemon, inside a
foreground service that already holds the rendezvous connection. Hosting is not
an extra task; it is exposing one that already runs.

What is left is minting and plumbing: a mobile call that registers a pending
invite and returns the token, and a way to hand the redemption back up for
signing.

**The real constraint is when the tap has to happen**, and it is an interaction
problem rather than a resource one.

The credential names the invitee's *per-mesh* device key, which the inviter
cannot know in advance — `Deferred` above exists precisely because the invitee
cannot derive that key until it learns which mesh it is joining. So there is
nothing to pre-sign: the card has to be tapped at the moment the other person
redeems.

And on Android, **NFC reader mode is foreground-only**. The window can sit in
the background indefinitely, but the tap needs the app open and on screen. So
the flow is: window waits in the background, redemption arrives, the app pulls
the user back with a high-priority notification, they tap, the credential goes
out.

In person that is a good ceremony — they scan, the phone says "hold your card",
you tap, they are in. Remotely it means both people are present at the same
time, which an invite already implies.

The alternative, if that proves annoying, is a short-lived delegated signing key
issued by the card so the phone can answer unattended for the window. It buys
convenience by putting a signing key on the phone, which is the property the
card exists to avoid, so it should be a deliberate decision rather than a
default.

**2. No way to inject a signer into the mobile binding.** The daemon builds its
Signer from a key file. Mobile needs one that calls back out to the phone.

gomobile can pass a Kotlin implementation of a Go interface, with a caveat:
bound interfaces cannot use `[32]byte`. The mobile-facing signer has to be
`SignDigest(digest []byte) ([]byte, error)` and adapt internally to
`cred.Signer`. Small, but it means a second interface rather than exporting the
existing one.

**3. NFC on Android, from Kotlin.** `loam-keycard` is TypeScript for React
Native, and the Keycard protocol it uses (`keycard-sdk`) is JavaScript. The
Shrooms app is Kotlin + Compose over a gomobile binding. The driver is therefore
**not directly reusable**, despite being the piece that already works.

## Options for gap 3

**A. Delegate to the app that already works, over an Android intent.**
Shrooms sends a 32-byte digest to the Loam/Scala app; that app taps the card and
returns the 64-byte signature and 33-byte public key.

This is exactly ADR-022's model. On desktop the card is driven by an external
tool (`keycard-cli sign --hex <digest>`) precisely so that card handling lives
outside Shrooms; on Android the same separation is an intent instead of a
subprocess. One Keycard implementation, already tested, and Shrooms never learns
what a Keycard is.

Costs: the other app must be installed; an intent contract to define and
version; and a hostile app could offer the same intent, so Shrooms must pin the
target package and check its signature rather than broadcasting.

**B. A Kotlin-native driver** using status-im's `keycard-android` SDK.
Self-contained, no second app, and the natural fit for a Kotlin app. Costs: a
second Keycard implementation in the ecosystem, and card handling — pairing,
PIN, tap timing, the failure modes — is where the fiddly work lives. `loam-keycard`'s
README is mostly hard-won detail about exactly that.

**C. keycard-go in-process**, with a Kotlin `IsoDep` transport injected.
Keeps one implementation across desktop and mobile in Go. But Shrooms has no
keycard dependency today and deliberately so, and this adds card protocol code
to the daemon binary on every platform.

Recommendation: **A**, for the same reason ADR-022 chose an external signer on
the desktop — the value is in not having card code in this project at all. B is
the fallback if the intent boundary proves awkward.

## Why this is worth doing

It closes the last thing that keeps a laptop in the loop. Today the authority
lives on a machine that must be present to admit anybody. With a card and a
phone, the mesh's authority is a physical object in a pocket, the phone is only
a reader, and losing the phone loses nothing. That is a stronger claim than the
one the project makes now, and it is the natural end of ADR-018's argument that
the authority should not live on the always-on node.

## Two questions

1. **Intent or Kotlin driver** — A or B above. A reuses working code and keeps
   card handling out of Shrooms; B is self-contained but duplicates the hard
   part.
2. **Unattended or co-present?** The default is co-present: the card is tapped
   when the invitee redeems, so the key never leaves it. A delegated short-lived
   key would let the phone answer unattended, at the cost of the guarantee the
   card is there for. Co-present looks right for invites, which are a moment
   between two people anyway.
