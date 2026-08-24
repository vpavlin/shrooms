# Keycard as the mesh authority, from a phone

**Status:** built. The assessment below is kept because its reasoning still
holds and the format analysis is still the reason this works at all; "The
screen, as built" near the end describes what actually shipped, and stage 1
passed against a physical card on 2026-08-24.

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

**And the tap does not have to happen while anybody waits.** "Hold the response
until I find the card" is not a compromise to engineer — it is what the design
already says. From ADR-025's notes on the socket: *"minting an invite hands over
the network key and admits nobody: a device is a member only once the admin key
has signed a credential."* Getting in and being admitted are already two events.

So the flow is:

1. The joiner redeems. The phone answers immediately with the mesh — no card
   needed, it is information the inviter already holds.
2. They join and sit **unenrolled**. `status` already prints this honestly:
   `!! no credential — peers will refuse this device`.
3. Whenever the admin next has the card, they tap.
4. The credential goes out as a **`Grant`** — "carries an admin-signed credential
   towards the device it names", already built because credentials expire and
   renewals need exactly this path.

Nothing new is required for the delay: the invite window stops being a deadline
for the admin, because the credential no longer travels on the invite channel.
The joiner may be waiting minutes or days, and can see why.

The alternative — a short-lived delegated signing key so the phone answers
unattended — is now unnecessary for this problem. It would buy convenience by
putting signing material on the phone, which is the property the card exists to
remove.

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

**C. keycard-go in the mobile module**, with a Kotlin transport injected.
The card protocol runs in Go; Kotlin supplies only the wire to the card.

The seam is one method:

```go
type Transmitter interface {
    Transmit([]byte) ([]byte, error)   // keycard-go
}
```

which is exactly Android's `IsoDep.transceive(ByteArray): ByteArray`. The Kotlin
side is about five lines — enable reader mode, hand over the tag — and pairing,
the secure channel, PIN and signing all stay in a maintained Go library.

**Measured, not estimated.** A program that reaches Select, Pair, VerifyPIN and
Sign links 3.6 MiB more than an empty one. Against the 49.7 MiB APK that is 7%.
Against the 12.5 MiB daemon it would be 29% — except the daemon never has to
link it: `mobile/` is a **separate Go module** and nothing in the daemon imports
it. So the cost lands only where the feature is used, and a server that will
never meet a card pays nothing. That removes the objection this option carried
in the first draft, which assumed the code would land on every platform.

The `go-ethereum` line in keycard-go's go.mod looks alarming and is not: the
linker keeps only what is reachable, and Shrooms already depends on the same
secp256k1 library keycard-go uses.

Recommendation: **C**.

The first draft said A, on the grounds that ADR-022's value was keeping card
code out of this project. That reasoning was about the *desktop*, where an
external signer costs nothing because processes are cheap and `keycard-cli`
already exists. On Android the equivalent is a second app the user must install
and keep in step, and the mesh's independence is worth more than 3.6 MiB in an
APK that is already 50. C keeps Shrooms self-contained, avoids reimplementing
the fiddly part, and confines the weight to the phone.

## Why this is worth doing

It closes the last thing that keeps a laptop in the loop. Today the authority
lives on a machine that must be present to admit anybody. With a card and a
phone, the mesh's authority is a physical object in a pocket, the phone is only
a reader, and losing the phone loses nothing. That is a stronger claim than the
one the project makes now, and it is the natural end of ADR-018's argument that
the authority should not live on the always-on node.

## The screen, as built

Settings → Keycard shows **one** thing: whether this phone is set up with a
card, and which key it signs with if it is. Everything else is behind the action
that applies.

It did not start there. The first version was the protocol laid out flat — five
buttons and two text fields, all always visible and always enabled: check, pair,
forget other devices, read key, test a signature. Every one of them was a real
operation and the screen never said which one you were on. The verdict after an
evening with a real card was *"I have no clue what we are doing and what the
flow should be"*, which was a fair description.

**Setting up is one guided sequence**, and the order is the point:

1. **Look at the card.** `SELECT` only — no slot, no PIN attempt, no password,
   nothing on the card changed. This answers three of the four ways setup can
   fail before anything has been spent.
2. **The verdict.** A card with no secure channel, no initialisation or no key
   is a dead end, and says which, in a sentence rather than a status word.
   Shrooms will not `INIT` a card or generate a key on one: both are
   irreversible and decide what the card *is*.
3. **A pairing password, only if this card has one.** Applet 4.0 and later
   authenticates with a certificate, so the field is not shown there at all. The
   factory default is filled in when it is shown, because it is not a secret in
   any useful sense and getting it wrong costs a slot.
4. **The PIN, as six dots and a keypad**, because a Keycard PIN is exactly six
   digits. The last digit is the action — no separate approve button, because
   by then there was nothing left to decide.
5. **The tap**, with the card asked for and the screen saying so.

`CardStatus` answers with JSON rather than a sentence for exactly this: the
screen has decisions to make from it. Whether to ask for a password, whether
there is any point asking for a PIN, whether freeing the other slots would help
— all of that used to be in prose and unreachable, so the screen showed every
button always.

### Three decisions worth naming

**A refused PIN does not restart the ceremony.** `CardEnrol` stores the pairing
the moment it succeeds and checks the PIN afterwards, so a mistyped PIN leaves
the phone paired. Going round again would spend a second of five slots to fix a
typo, so the retry offers the pad and reads the key with the pairing already
held.

**The PIN is not cached.** loam-keycard caches it, and the flow that comes of it
— enrol once, then tap-per-sign — is nicer. It also means anybody holding an
unlocked phone and the card can sign without knowing the PIN, which is most of
what the PIN is for. The operations that need one here are occasional (setting
up, admitting a device, a self-test), so the pad every time costs little. **Open
for Vaclav**: a short in-memory window, cleared on background, would be a
defensible middle if admitting several devices at once becomes normal.

**"Forget this card" does not free the slot on the card.** It deletes this
phone's pairing locally; the card still counts this phone among its five and
setting up again takes another slot. Freeing one needs the card present and a
pairing already held, which is what "Free the other pairing slots" is for. The
dialog says so, because the opposite assumption is the natural one.

### Three things asked before, not discovered by failing

**Whether this phone has NFC, and whether it is on.** Both used to surface as an
exception thrown from the middle of a tap — after somebody had opened the
ceremony, typed a PIN and held a card against a phone that was never going to
read it. The panel says so up front, and offers Android's NFC settings.

**Whether this phone is set up with a card, on the invite screen.** Without one
the flow used to run its whole length — mint a token, show a QR, have somebody
scan it, take their request — and fail at the tap, by which point the other
person has to be asked to do it all again.

**Where the screen is.** It was the fourth section of a scrolling settings
screen, under two settings most people never change, which produced *"Where do I
find this keycard screen? I see 'invite a device', but nothing on keycard
explicitly"*. It is now above the blind relay.

### What is on disk

| file | mode | what it is |
|---|---|---|
| `keycard-pairing` | 0600 | the pairing, base64. Not a secret that admits anybody — it opens an encrypted channel and the PIN is still needed to sign — but one of the two things an attacker would need. |
| `keycard-key` | 0600 | the authority's public half, so the screen can say what this phone signs with without asking for a card and a PIN. Public; losing it costs one tap. |

## Two questions

1. ~~Intent or Kotlin driver~~ — **settled: C**, keycard-go inside the mobile
   module with a five-line Kotlin transport. Self-contained, no reimplementation,
   3.6 MiB on the APK and nothing on the daemon.
2. **Unattended or co-present?** The default is co-present: the card is tapped
   when the invitee redeems, so the key never leaves it. A delegated short-lived
   key would let the phone answer unattended, at the cost of the guarantee the
   card is there for. Co-present looks right for invites, which are a moment
   between two people anyway.
