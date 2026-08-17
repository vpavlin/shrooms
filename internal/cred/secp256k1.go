package cred

import (
	"crypto/ed25519"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// A second admin key type, for an authority whose private half lives on a card
// (ADR-022).
//
// The card decides this, not us. A Keycard signs secp256k1 ECDSA and nothing
// else — measured on a 3.1 applet rather than inferred: it exports an
// uncompressed EC point and returns r, s and a recovery byte. So a mesh minted
// with a card has a secp256k1 authority, and every node that verifies its
// credentials needs this.
//
// The cost is deliberate and bounded. It is one pure-Go dependency, about
// 1.3 MiB of binary — 11% of the daemon, 2.5% of the APK — and it lands on
// every device including Android, permanently, for any mesh created this way.
// Existing meshes are untouched: an authority is fixed at mint.
//
// Nothing dispatches on a flag, a version or a config field. The two key types
// are different lengths and cannot be confused: 32 bytes is an ed25519 public
// key, 33 is a compressed secp256k1 point whose first byte is 0x02 or 0x03. A
// format that needed a discriminator would need it to be authenticated, and
// this one does not.

// secp256k1PubKeySize is a compressed point: one parity byte and the X
// coordinate. Uncompressed (65 bytes, 0x04-prefixed) is deliberately not
// accepted — the card exports that form, and `admin init --keycard` compresses
// it before writing, so that there is exactly one representation of a key in a
// config and therefore exactly one mesh ID.
const secp256k1PubKeySize = 33

// signatureSize is r ‖ s. The card also returns a recovery byte, which matters
// to Ethereum and not to us: we verify against a known public key rather than
// recovering one, so it is dropped at signing time.
const secp256k1SigSize = 64

func knownKeySize(n int) bool {
	return n == ed25519.PublicKeySize || n == secp256k1PubKeySize
}

// verifyKey checks one signature against one admin key, whichever type it is.
func verifyKey(pub ed25519.PublicKey, digest, sig []byte) bool {
	if len(pub) == ed25519.PublicKeySize {
		return ed25519.Verify(pub, digest, sig)
	}
	return verifySecp256k1(pub, digest, sig)
}

// verifySecp256k1 checks an ECDSA signature over the digest.
//
// The digest is used as-is rather than hashed again. Every signature in this
// package covers `SHA-256(domain ‖ body)` already, which is what let the card
// be considered at all: a smartcard signs a fixed-size input and nothing else.
func verifySecp256k1(pub, digest, sig []byte) bool {
	// Exactly 64, not at least. `<` read sig[:64] and ignored anything after
	// it, so a signature with junk appended verified as readily as the real
	// one — two encodings of the same signature, which the comment on
	// SetByteSlice below argues against three lines later.
	if len(pub) != secp256k1PubKeySize || len(sig) != secp256k1SigSize || len(digest) != 32 {
		return false
	}
	p, err := secp256k1.ParsePubKey(pub)
	if err != nil {
		return false
	}
	var r, s secp256k1.ModNScalar
	// SetByteSlice reports overflow, which is a malformed signature rather than
	// a failed one — refused either way, but not silently reduced mod N, since
	// that would make two distinct encodings verify identically.
	if r.SetByteSlice(sig[:32]) || s.SetByteSlice(sig[32:64]) {
		return false
	}
	// Deliberately NOT requiring low-S, though the malleability is real:
	// (r, n-s) verifies wherever (r, s) does, so a credential has two valid
	// encodings. Bitcoin and Ethereum both refuse the high half for exactly
	// that reason.
	//
	// It is refused here for a better one: keycard-go does not canonicalise s,
	// so a Keycard emits the high half about half the time. Rejecting it would
	// turn signing with the card — the entire point of accepting secp256k1 at
	// all (ADR-022) — into a coin flip, failing in a way indistinguishable
	// from the wrong key being used.
	//
	// Safe to allow because nothing here authorises on the bytes of a
	// signature: a credential is identified by device and serial, and the
	// replay guard counts sequence numbers, so a second encoding of the same
	// statement says the same thing. If anything ever keys off a signature —
	// deduplicating by hash, say — this must change, and the signer must
	// canonicalise rather than the verifier refuse.
	return ecdsa.NewSignature(&r, &s).Verify(digest, p)
}

// VerifyDigest checks one signature against one admin key, whichever type it
// is. Exported for the detached signer, which has to check what it was handed
// before it uses it: a signature pasted from another program, another machine
// or another card is exactly the case where a silent mismatch would be written
// into a credential and only fail on somebody else's device days later.
func VerifyDigest(pub ed25519.PublicKey, digest [32]byte, sig []byte) bool {
	return verifyKey(pub, digest[:], sig)
}
