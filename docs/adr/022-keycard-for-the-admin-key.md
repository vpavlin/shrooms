# 022. A Keycard for the admin key

**Status:** accepted, built, and **proven against a physical card on
2026-08-24** — a digest signed on the card, verified by the same function a peer
uses on a credential.

`cred.Signer` exists and the admin tooling signs through it, so the file-backed
key and a card are interchangeable above that line. The question this ADR
originally got wrong — whether the card could sign ed25519 — has been settled
against hardware and the applet source: it cannot, so option 1 below was taken.
Every node now verifies both key types (`internal/cred/secp256k1.go`), and
signing happens through a detached signer (`--sign-with`, or a digest read off
the terminal) so no card driver is linked into shrooms at all.

**That defect is now explained, and it was never the card.** This ADR recorded
that signatures from `keycard-cli` did not verify against the public key the
same tool exported, and left it for the Keycard developers. It did not need
them. `keycard-cli` is built on `keycard-go`, and `keycard-go` corrupts the `s`
of every signature it parses: `calculateV` does `rs := append(r, s...)` where
`r` is a slice into the response buffer, so the append writes `s` over the two
DER header bytes between `r` and `s` while the `s` slice still points at the old
offset. What the caller receives is `s` shifted two bytes left. The public key,
`r` and `v` all survive, which is why the symptom looked like a key that did not
match its own signatures.

Found 2026-08-24, the first time a physical card was used, and reproducible with
no hardware at all: sign anything locally, wrap it in a legacy signature
template, hand it to `ParseSignature`, and compare `s` in and out.

`cred.RepairCardSignature` recovers the two lost bytes and is documented there.
So the seam works and the card works. The repair was written against signatures
made locally and then met a real one the same day: `CardSelfTest` signs on the
card and verifies with the same function a peer applies to a credential, and it
passes. That is the whole path — pairing, PIN, secure channel, on-card signing,
both conversions, and the repair — exercised at once.

The workaround should be removed when the library is fixed. Upstream has not
been told.

## Context

[ADR-018](018-credentials-instead-of-a-shared-key.md) split authority from
participation: the admin key admits and removes devices, and is needed nowhere
else. A node runs without it. That makes it the one secret in this system whose
usage pattern suits a smartcard — a handful of signatures a year, each one a
deliberate act by a person who is present.

Two things already anticipate this, and they are the parts that are painful to
retrofit.

**Signatures cover a digest, not a body.** `cred.Credential.Digest` is
`SHA-256("shrooms/cred/v1" ‖ body)`, and the same for revocations. A card signs
a fixed-size input and chooses its algorithm per call
(`P2SignEdDSAEd25519`, `P2SignECDSA`, …), so a 32-byte digest works whatever the
card supports and the body may be any length.

**The authority is a set of public keys.** `admin_keys` in a config are public
values; the mesh id is their hash. Where the private halves live is invisible to
every peer, so moving one onto a card changes nothing on the wire and needs no
migration.

Keycard is also in the Logos ecosystem.

## Notes towards a decision

### What the card holds

The **admin key**, and nothing else. Not the network key: every node needs it
continuously to derive topics, the payload key and pairwise PSKs, so a card in a
drawer cannot serve it. Not device identities: they are per device, generated on
first run, and worth nothing to anybody else.

This is a smaller claim than "the mesh is on a card", and it is the true one.

### What the library actually does

This ADR was written assuming `keycard-go` could sign ed25519, which is what
the credential format uses. Checked against v0.3.3, the latest published
version, that is false and the correction matters more than the rest of these
notes:

- **`CommandSet.Sign` produces a secp256k1 ECDSA signature.** `types.Signature`
  carries r, s and a recovery byte and is parsed with go-ethereum's
  `crypto.Ecrecover`. There is no ed25519 anywhere in the library — the applet
  it targets is the Ethereum-facing one.
- **It depends on go-ethereum**, plus btcec and decred's secp256k1. This project
  has three direct dependencies and a deliberate policy of doing wire protocols
  with the standard library.

The consequence is the part that is not a detail. Credentials are verified by
**every node**, not by the admin's machine, so an authority holding a secp256k1
key means every device — including Android, including a container on a VPS —
needs a secp256k1 verifier. Go's standard library has none: `crypto/ecdsa` does
the NIST curves and not this one.

So the honest position is that this is blocked on a decision rather than on
effort:

1. **Accept a second admin key type.** `admin_keys` gains secp256k1 alongside
   ed25519, distinguishable by length (32 versus a 33-byte compressed point),
   `VerifyBy` dispatches on it, and every node gains a small secp256k1
   dependency — decred's is the light one, and is pure Go, so gomobile is fine
   with it. Existing meshes are untouched: a mesh's authority is fixed at mint,
   so this only ever applies to a mesh created with a card.
2. **Find EdDSA on the card.** *Closed.* The hope was that the applet supported
   more than this Go library exposes — the library is not the applet. Checked
   with a card in hand and against the applet source at tag 3.1.0: there is no
   ed25519 in it at all. The library was not hiding anything.
3. **Do not do it.** The admin key is already offline and used a few times a
   year; a card improves it, and not at the price of a new signature scheme on
   every node.

**(1) was taken**, once (2) was closed — and it turned out to be the smaller
change of the two on offer. A mesh minted with a card is a new mesh anyway, the
address prefix already derives from whatever the authority is, and the verifier
is one pure-Go dependency that gomobile accepts. The dependency shrooms did
*not* take is the card library: signing is detached, so go-ethereum stays out of
the build and the admin's card can be driven by whatever tool the admin
already trusts.

### The seam to build

**Built.** One interface, where `issueFor` and `cmdAdminRevoke` used to call
`ed25519.Sign` with an in-memory key:

```go
type Signer interface {
    Public() ed25519.PublicKey
    SignDigest(d [32]byte) ([]byte, error)
}
```

`cred.Admin` implements it, and `cred.IssueWith` and `cred.SignRevocationWith`
take it, so the second implementation is the only thing missing. Everything
above it — issuing, revoking, renewal, the invite exchange — is unchanged,
because all of it already works in terms of a digest.

`SignDigest` returns an error even in the in-memory case, where it cannot fail.
A card fails in ways a file does not — unplugged mid-operation, wrong PIN,
pairing lost — and if the file implementation had no error path, the card's
would be the one nothing else exercises.

### Amendment, 2026-08-26: a card, a reader, and a mesh minted from it

Built and proven end to end from a laptop with a USB reader, no phone involved:

    shrooms keycard init      # PIN, PUK, pairing password, key + mnemonic
    shrooms keycard pair      # one of five slots
    shrooms admin init --keycard

which produced a mesh whose `admin_keys` is the card's own key, and then a
credential for a device signed by that card. `IssueFor` verifies what it signed
against the authority before returning, so a credential coming back is the proof
that the loop closes. `Authority.CardOnly()` is true for a real mesh for the
first time — the condition [ADR-033](033-the-card-is-the-admin-not-the-uid.md)
was built around and could not, until now, be reached at all.

**One admin key, not the usual two.** The pair exists because losing the file
ends a mesh; a card's key is already reconstructible from the mnemonic it was
initialised with. A second key would be another thing to lose, and worse than
redundant: it could not itself be a card key, and `CardOnly` is every key or
none, so one file key would disable the widening above.

**This ADR said shrooms would never INIT a card or generate a key on one**, and
that is no longer true. The reasoning was about a phone settings screen and an
accidental tap on something irreversible, which still holds — the Android app
does neither. On a command line, against a card somebody has physically pushed
into a reader, behind a typed confirmation, the cost of *not* having it is a
setup that stops halfway and says "now go and find another program". There was
no other program on the machine.

`shrooms keycard reset` exists for the same reason and is the sharper case: it
destroys a key. It was written because Vaclav's card reached five taken pairing
slots with no device holding one, which is otherwise terminal — `UNPAIR` travels
inside a channel only a pairing can open. `FACTORY RESET` is unauthenticated by
design, which is the card's own answer to that trap, and worth knowing about a
card in a drawer: possession is enough to destroy what is on it, though not to
use it.

### The derivation path is the mesh's, not the wallet's

Added 2026-08-25. The path was `m/44'/60'/0'/0` — standard Ethereum, and what
loam-keycard uses, so one card presented the same key to both. Deliberate, and
the wrong trade.

`admin_keys` is in every member's config, so on the wallet path everybody you
share a mesh with can read your Loam/Ethereum identity's public key. The linkage
runs one way only — rendezvous topics derive from the network key rather than
the mesh id, so nobody finds your mesh from your wallet — but mesh to identity is
the direction that matters once a mesh is shared with somebody outside the
household, which is the case this project was launched for.

It is now `m/64265'/0'/0'`: the purpose index is the first two bytes of
SHA-256("shrooms"), clear of every registered BIP purpose so no wallet restoring
the mnemonic will scan it, hardened at every level, with the second level
reserved for one authority per mesh (ADR-015) since two meshes sharing an admin
key would be linkable through admin_keys alone.

**Unchangeable after the fact.** The mesh id is the hash of the admin key set
and the overlay prefix derives from the id, so a different path is a different
mesh. Nothing had been minted from a card when this changed, which is the only
reason it was free.

### The card is not the only copy of the key

Also 2026-08-25, and it corrects something this ADR has claimed from the start.
Initialising a Keycard produces a BIP-39 mnemonic and asks you to write it down.
That mnemonic reconstructs the same key on another card, in a software wallet,
or in ten lines of Python.

So *"a compressed secp256k1 point, whose private half has never existed outside
the card"* — the sentence [ADR-033](033-the-card-is-the-admin-not-the-uid.md)
leans on — is **not true**. The private half existed outside the card the moment
the words were displayed.

What remains true is worth having and should be stated instead: **the card will
not export the private half, so a host cannot steal it.** Malware on a laptop
cannot take the key; it can only ask for signatures while the card is held
against the reader and the PIN has been entered. That is the property the card
buys. It is not exclusivity.

Two consequences. The mnemonic is the mesh's root of trust, permanently and
without revocation — and it looks like any other wallet seed, so the risk is it
gets filed next to crypto backups by somebody who has not registered that it is
also a VPN's master key. And recovery is already solved: a card mesh needs no
second admin key, because the mnemonic restores the first one.

### What has to be got right

- **The public key must be exported at mint time**, since `admin_keys` is what
  every node checks against and the card will not hand over the private half.
  `admin init --keycard` therefore reads the public key from the card and
  writes it into the config exactly as it writes a generated one.
- **Pairing and PIN are state on the host.** A Keycard pairs with a client and
  that pairing is a secret worth as much as physical possession. It belongs
  wherever the admin key file lives now, with the same permissions.
- **Recovery matters more, not less.** The second key minted at `init` is
  currently a paper backup against a lost file; with a card it is the answer to
  a lost or wiped card. The recovery key should not also live on the same card,
  which is the obvious mistake.
- **A card is not always present.** Renewal (ADR-018) needs a person already —
  the file-backed key prompts for its passphrase, so the sweep has never been
  unattended — and a card changes the prompt rather than introducing one. What
  it does change is the *count*: a sweep signs one credential per expiring
  device. See below.

### One PIN, many signatures — and the phone as the reader

A renewal sweep signs once per expiring device, and an invite exchange signs
again. If each signature cost a PIN entry, a card would make the thing it was
meant to protect annoying enough to route around, which is the usual way
hardware keys fail.

It does not, and the reason is in the applet rather than in any client. PIN
state is cleared in exactly one place — `selectApplet`, which runs on SELECT,
so on power-up:

```java
private void selectApplet(APDU apdu) {
  if (pin != null) {
    altPIN.reset();
    mainPIN.reset();
```

Nothing else resets it: not a signature, not a derivation. **Verify the PIN once
and every subsequent `SIGN` in that card session is free**, for as long as the
card stays powered and the client holds the same secure channel. A sweep over
five devices is one PIN.

That is what makes **the phone the natural reader**. Keycard is NFC, the phone
already has the radio, and tapping a card to a phone is a gesture people
perform without instruction. Because the field powers the card, holding it
against the back *is* the session: unlock once, sign everything, take the card
away and the card powers down and locks itself. There is no timeout to choose
and nothing to remember to lock — the physical act and the security boundary
are the same act, which is the property this rarely has.

Two things follow for the implementation, and one for the design:

- **The signer must outlive the digest.** `--sign-with` runs its command once
  per digest, and `keycard-cli` unpairs at the end of every invocation, so five
  devices is five sessions and five PINs today. Batching lives in a signer
  process that stays alive across the sweep — reading digests and writing
  signatures — not in a change to the credential format, which already signs a
  digest at a time. On the phone this falls out for free: the app owns the NFC
  channel.
- **The phone holds the pairing, and that is fine.** Pairing is host state — a
  32-byte key and a slot index, one of five the card will ever grant — and
  without it a host cannot issue any command past SELECT. Putting it on a phone
  sounds worse than it is: it is a shortcut for somebody who *already has the
  card*, and on its own it is a key to a lock that is not present. The card
  lives in a safe and comes out for an invite or a monthly sweep, so the phone
  alone yields nothing and the card alone still faces the PIN, three tries and
  a wipe. That separation is the whole point of buying a card — what matters
  stops living on the devices that move.

  Two operational notes rather than risks: the card and the phone should not be
  stored together, which a safe arranges by itself; and `unpair` is a card
  command, so retiring a lost phone's slot means presenting the card to another
  paired host. Five slots is not many, and a phone lost without cleanup costs
  one silently.
- **Pinless signing exists and is refused.** Applet 3.1 has `SIGN_P1_PINLESS`
  and a designated path that signs with no PIN at all — the gate is
  `pin.isValidated() || usePinless || isPinless()`. Applet 4.0 deletes it: its
  `sign` accepts only `SIGN_P1_DERIVE` and requires `pin.isValidated()`
  unconditionally. So it is a feature that works on one dev card and not on a
  card bought next year, and what it buys is that possession of the card alone
  admits devices to the mesh — which is precisely what the PIN is there to
  prevent. The session behaviour gives the same ergonomics without either
  problem.

### What it does not fix

Membership still rests on the network key for everything except admission
(ADR-020). A card protects who may *admit* devices; it does not protect the
control plane's confidentiality, and it cannot, because that key is in use on
every node all the time.

## Sequencing

After the two things that are load-bearing: a two-mesh integration test, and
[ADR-017](017-invite-tokens.md)'s second round so an invite can carry a per-mesh
identity. Keycard is additive — it changes where a key sits, not what the system
guarantees — and it needs hardware in hand to get right, which the others do
not.
