package cred

import (
	"errors"
	"fmt"

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
