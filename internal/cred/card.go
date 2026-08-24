package cred

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Turning what a smartcard hands back into what this package verifies.
//
// A Keycard speaks the shapes Ethereum wanted: an uncompressed 65-byte point
// and a signature whose r and s are minimal-length big-endian integers. This
// package wants a 33-byte compressed point and a fixed 64-byte r‖s. Neither
// side is wrong and the conversion is small — but it is exactly the kind of
// small that fails silently, because a signature with a short r is still 63
// bytes of plausible-looking hex and verifies against nothing.
//
// So it lives here, next to the verifier that will reject it, and it is tested
// against vectors rather than against a card. A card is needed to find out
// whether the *session* works; it is not needed to find out whether 31 bytes
// were left-padded correctly, and a test that needs hardware is a test nobody
// runs.

// CompressPoint turns a card's uncompressed public key into the compressed form
// an authority is written with.
//
// Parsed rather than sliced. The compressed form is a parity byte and X, so
// "compression" could be four lines of byte shuffling that never notices it was
// handed 65 bytes of noise. Going through the curve means a point that is not
// on it is refused here, at the moment somebody is minting an authority from
// it, rather than at the moment the first credential fails to verify.
//
// Already-compressed input is accepted and returned unchanged, so a caller that
// does not know which form it holds can ask for the one it wants.
func CompressPoint(pub []byte) ([]byte, error) {
	switch len(pub) {
	case secp256k1PubKeySize, 65:
	default:
		return nil, fmt.Errorf("public key is %d bytes, want %d compressed or 65 uncompressed",
			len(pub), secp256k1PubKeySize)
	}
	p, err := secp256k1.ParsePubKey(pub)
	if err != nil {
		return nil, fmt.Errorf("not a point on secp256k1: %w", err)
	}
	return p.SerializeCompressed(), nil
}

// CompactSig turns a card's r and s into the fixed 64-byte form.
//
// The card returns each as a minimal-length big-endian integer, so a value that
// happens to start with a zero byte comes back 31 bytes long — about one
// signature in 256, which is frequently enough to reach production and rarely
// enough to look like a flake when it does. Each half is left-padded to 32.
//
// Leading zeros beyond 32 bytes are stripped rather than refused: they carry no
// value and some encoders emit one. Anything longer than that after stripping
// is a different number than the caller thinks it has, and is an error.
//
// s is deliberately NOT normalised to low-S here. See verifyKey: this package
// accepts either, because keycard-go does not canonicalise s and requiring it
// would make signing a coin flip. A caller that has already normalised — the
// mobile stack does — passes straight through.
func CompactSig(r, s []byte) ([]byte, error) {
	rp, err := pad32(r)
	if err != nil {
		return nil, fmt.Errorf("r: %w", err)
	}
	sp, err := pad32(s)
	if err != nil {
		return nil, fmt.Errorf("s: %w", err)
	}
	out := make([]byte, 0, secp256k1SigSize)
	out = append(out, rp...)
	out = append(out, sp...)
	return out, nil
}

// pad32 left-pads a big-endian integer to 32 bytes.
func pad32(b []byte) ([]byte, error) {
	for len(b) > 32 && b[0] == 0 {
		b = b[1:]
	}
	if len(b) > 32 {
		return nil, fmt.Errorf("%d bytes, want at most 32", len(b))
	}
	if len(b) == 0 {
		return nil, errors.New("empty")
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out, nil
}

// SignerOf works out which key actually produced a pair of signatures.
//
// A Keycard has been observed reporting one public key in its signature
// response while signing with another — 04681f8e… reported, while the signature
// recovers to 02e6242f… or 03970770…, and neither is the reported key. Whatever
// the reason, the key that will verify a credential is the one that SIGNED, not
// the one the card mentioned, and this finds it without trusting either.
//
// ECDSA recovery yields two candidate keys per signature and both verify it, so
// one signature cannot say which is real. Two signatures over different digests
// can: the signing key is a candidate for both, and any other candidate is a
// coincidence of one. Two is enough in practice and the check below insists on
// exactly one survivor rather than taking the first.
//
// Nothing here trusts a reported key, so a card that reports the wrong one, or
// none at all, is handled the same way.
func SignerOf(digestA [32]byte, sigA []byte, digestB [32]byte, sigB []byte) ([]byte, error) {
	candA, err := recoverCandidates(digestA, sigA)
	if err != nil {
		return nil, err
	}
	candB, err := recoverCandidates(digestB, sigB)
	if err != nil {
		return nil, err
	}
	var common [][]byte
	for _, a := range candA {
		for _, b := range candB {
			if bytes.Equal(a, b) {
				common = append(common, a)
			}
		}
	}
	switch len(common) {
	case 1:
		return common[0], nil
	case 0:
		return nil, errors.New("no key signed both digests — the card is not signing " +
			"with one key, or a signature was mangled on the way back")
	default:
		return nil, fmt.Errorf("%d keys could have signed both digests, which should "+
			"not happen; sign again", len(common))
	}
}

// recoverCandidates returns every public key that could have produced a
// signature over a digest. There are at most two.
func recoverCandidates(digest [32]byte, sig []byte) ([][]byte, error) {
	if len(sig) != secp256k1SigSize {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(sig), secp256k1SigSize)
	}
	var out [][]byte
	for recid := 0; recid < 2; recid++ {
		c := make([]byte, 65)
		c[0] = byte(27 + recid)
		copy(c[1:], sig)
		pub, _, err := ecdsa.RecoverCompact(c, digest[:])
		if err != nil {
			continue
		}
		out = append(out, pub.SerializeCompressed())
	}
	if len(out) == 0 {
		return nil, errors.New("no key could have produced that signature")
	}
	return out, nil
}
