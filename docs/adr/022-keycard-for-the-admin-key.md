# 022. A Keycard for the admin key

**Status:** proposed. The seam is built; the card is blocked on a question this
ADR got wrong.

`cred.Signer` exists and the admin tooling signs through it, so the file-backed
key and a card are already interchangeable above that line. What is not built is
the card, because of the finding in "What the library actually does" below: it
signs secp256k1, not ed25519, and that lands on every node rather than only on
the admin's machine.

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
2. **Find EdDSA on the card.** The applet may support more than this Go library
   exposes — the library is not the applet — and if it does, the change stays
   entirely on the admin's machine, which is what made this attractive in the
   first place. Worth ten minutes with a card in hand before choosing option 1.
3. **Do not do it.** The admin key is already offline and used a few times a
   year; a card improves it, and not at the price of a new signature scheme on
   every node.

The recommendation is to try (2) with hardware before committing to (1), and to
treat (1) as a real option rather than a workaround — a mesh minted with a card
is a new mesh anyway, and the address prefix already derives from whatever the
authority is.

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
- **A card is not always present.** Renewal (ADR-018) is meant to be
  hands-off, and a card is by definition not. Either renewal keeps a separate
  online key in the authority set — which the fixed set already allows for —
  or renewal requires a person, which is a real cost to state rather than
  discover.

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
