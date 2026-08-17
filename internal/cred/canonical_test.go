package cred

import (
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// signRS produces the wire form this package verifies: r‖s, 64 bytes, no DER.
func signRS(t *testing.T, priv *secp256k1.PrivateKey, digest []byte) []byte {
	t.Helper()
	sig := ecdsa.Sign(priv, digest)
	// ecdsa.Sign returns a Signature; take r and s out in fixed-width form,
	// which is what a card emits and what verifySecp256k1 expects.
	r, s := sig.R(), sig.S()
	rb, sb := r.Bytes(), s.Bytes()
	out := make([]byte, 64)
	copy(out[:32], rb[:])
	copy(out[32:], sb[:])
	return out
}

// The gate was `len(sig) < 64`, which read the first 64 bytes and ignored
// anything after them — so a real signature with junk appended verified exactly
// as the real one did. Two encodings of one statement, which the comment three
// lines below it argues against.
//
// This starts from a signature that genuinely verifies, so the assertion is
// about the extra bytes and not about a bad key failing for another reason.
func TestOverLongSignatureIsRefused(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey().SerializeCompressed()

	digest := make([]byte, 32)
	digest[0] = 0xa1

	good := signRS(t, priv, digest)
	if !verifySecp256k1(pub, digest, good) {
		t.Fatal("a signature this package produced does not verify; the test is wrong, not the code")
	}

	if verifySecp256k1(pub, digest, append(append([]byte{}, good...), 0x00)) {
		t.Error("a valid signature with a byte appended still verified")
	}
	if verifySecp256k1(pub, digest, append(append([]byte{}, good...), []byte("trailing junk")...)) {
		t.Error("a valid signature with junk appended still verified")
	}
	if verifySecp256k1(pub, digest, good[:63]) {
		t.Error("a truncated signature verified")
	}
}

// A digest that is not 32 bytes is not a digest: the card signs a fixed-size
// input and nothing else, so anything else means a caller has gone wrong.
func TestWrongDigestLengthIsRefused(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey().SerializeCompressed()
	digest := make([]byte, 32)
	sig := signRS(t, priv, digest)

	for _, n := range []int{0, 31, 33, 64} {
		if verifySecp256k1(pub, make([]byte, n), sig) {
			t.Errorf("a %d-byte digest was accepted", n)
		}
	}
}

// High-S is accepted on purpose: keycard-go does not canonicalise s, so a card
// emits the high half about half the time, and refusing it would make signing
// with the card a coin flip. Nothing here authorises on signature bytes, so the
// second encoding says the same thing.
//
// Pinned as a test because it is a deliberate deviation from what every
// blockchain does with the same curve, and the next person to read the code
// should find the reason attached rather than assume an oversight.
func TestHighSIsAcceptedDeliberately(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey().SerializeCompressed()
	digest := make([]byte, 32)
	digest[0] = 0x5c

	good := signRS(t, priv, digest)

	// Flip s to n-s, the other valid encoding of the same signature.
	var s secp256k1.ModNScalar
	s.SetByteSlice(good[32:])
	s.Negate()
	sb := s.Bytes()
	flipped := make([]byte, 64)
	copy(flipped[:32], good[:32])
	copy(flipped[32:], sb[:])

	if !verifySecp256k1(pub, digest, flipped) {
		t.Error("the other valid encoding was refused; a Keycard emits it about half the time")
	}
}

// An admin key must be one of the two sizes this package understands.
func TestOnlyKnownKeySizes(t *testing.T) {
	for _, n := range []int{0, 31, 32, 33, 34, 64} {
		want := n == 32 || n == secp256k1PubKeySize
		if got := knownKeySize(n); got != want {
			t.Errorf("knownKeySize(%d) = %v, want %v", n, got, want)
		}
	}
}
