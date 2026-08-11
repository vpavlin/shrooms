# 022. A Keycard for the admin key

**Status:** proposed — notes, not a plan. Nothing here is built.

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

Keycard is also in the Logos ecosystem, and `keycard-go` exposes the EdDSA
signing the credential format assumes.

## Notes towards a decision

### What the card holds

The **admin key**, and nothing else. Not the network key: every node needs it
continuously to derive topics, the payload key and pairwise PSKs, so a card in a
drawer cannot serve it. Not device identities: they are per device, generated on
first run, and worth nothing to anybody else.

This is a smaller claim than "the mesh is on a card", and it is the true one.

### The seam to build

One interface, where `issueFor` and `cmdAdminRevoke` currently call
`ed25519.Sign` with an in-memory key:

```go
type Signer interface {
    Public() ed25519.PublicKey
    SignDigest(d [32]byte) ([]byte, error)
}
```

Two implementations: the passphrase-encrypted file that exists today, and a
card. Everything above it — issuing, revoking, the invite exchange — is
unchanged, because all of it already works in terms of a digest.

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
