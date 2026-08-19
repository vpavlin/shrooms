package cred

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// The card exports an uncompressed point and an authority is written with a
// compressed one, so this conversion decides whether a mesh minted from a card
// has the right mesh ID — and a mesh ID is fixed at mint and cannot be
// corrected later.
func TestCompressPointRoundTrips(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey()

	got, err := CompressPoint(pub.SerializeUncompressed())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pub.SerializeCompressed()) {
		t.Error("compressing the card's form did not produce the stored form")
	}
	if len(got) != secp256k1PubKeySize {
		t.Errorf("compressed key is %d bytes, want %d", len(got), secp256k1PubKeySize)
	}

	// A caller that already holds the compressed form gets it back, so it need
	// not know which form it has.
	same, err := CompressPoint(pub.SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(same, pub.SerializeCompressed()) {
		t.Error("an already-compressed key did not survive")
	}
}

// Slicing X out of an uncompressed point would "work" on garbage. Going through
// the curve means an authority cannot be minted from something that is not a
// key, at the moment it is minted rather than the first time it fails.
func TestCompressPointRefusesRubbish(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"short", make([]byte, 20)},
		{"right length, not on the curve", append([]byte{0x04}, make([]byte, 64)...)},
		{"ed25519-sized", make([]byte, 32)},
	} {
		if _, err := CompressPoint(tc.in); err == nil {
			t.Errorf("%s was accepted as a public key", tc.name)
		}
	}
}

// One signature in 256 has a short r or s, because the card returns each as a
// minimal-length integer. Often enough to reach production, rare enough to look
// like a flake — so it is pinned rather than left to chance.
func TestCompactSigPadsShortHalves(t *testing.T) {
	r := []byte{0x01, 0x02}             // 2 bytes
	s := bytes.Repeat([]byte{0xff}, 32) // already full width
	got, err := CompactSig(r, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != secp256k1SigSize {
		t.Fatalf("signature is %d bytes, want %d", len(got), secp256k1SigSize)
	}
	want := make([]byte, 32)
	want[30], want[31] = 0x01, 0x02
	if !bytes.Equal(got[:32], want) {
		t.Errorf("r was not left-padded: %x", got[:32])
	}
	if !bytes.Equal(got[32:], s) {
		t.Errorf("s was altered: %x", got[32:])
	}
}

func TestCompactSigRejectsOversize(t *testing.T) {
	if _, err := CompactSig(bytes.Repeat([]byte{1}, 33), make([]byte, 32)); err == nil {
		t.Error("a 33-byte r was accepted")
	}
	if _, err := CompactSig(nil, make([]byte, 32)); err == nil {
		t.Error("an empty r was accepted")
	}
	// A single leading zero is padding, not a different number.
	got, err := CompactSig(append([]byte{0}, bytes.Repeat([]byte{7}, 32)...), make([]byte, 32))
	if err != nil {
		t.Fatalf("a zero-prefixed 32-byte r was refused: %v", err)
	}
	if got[0] != 7 {
		t.Errorf("stripping the pad byte changed r: %x", got[:32])
	}
}

// The whole point: a signature that has been through these adapters must
// verify against the compressed key, using the same path a peer uses when it
// checks a credential.
func TestAdaptedSignatureVerifies(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	if _, err := rand.Read(digest[:]); err != nil {
		t.Fatal(err)
	}

	// What a card hands back: r and s as integers, an uncompressed point.
	sig := ecdsa.Sign(priv, digest[:])
	r, s := sig.R(), sig.S()
	rb, sb := r.Bytes(), s.Bytes()

	compact, err := CompactSig(rb[:], sb[:])
	if err != nil {
		t.Fatal(err)
	}
	pub, err := CompressPoint(priv.PubKey().SerializeUncompressed())
	if err != nil {
		t.Fatal(err)
	}

	if !verifyKey(pub, digest[:], compact) {
		t.Error("a signature through the card adapters did not verify")
	}
}
