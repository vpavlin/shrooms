package cred

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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

// The signing key can be found from two signatures, without trusting whatever
// key a card claims to have used.
//
// A real Keycard reported 04681f8e… while its signature recovered to
// 02e6242f… or 03970770… — neither being the reported key. What verifies a
// credential is the key that signed, so that is the one to find.
func TestSignerOfFindsTheKeyThatSigned(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := priv.PubKey().SerializeCompressed()

	sign := func(d [32]byte) []byte {
		sig := ecdsa.Sign(priv, d[:])
		r, sc := sig.R(), sig.S()
		rb, sb := r.Bytes(), sc.Bytes()
		out := append([]byte(nil), rb[:]...)
		return append(out, sb[:]...)
	}
	_ = sign
	a := sha256.Sum256([]byte("one"))
	b := sha256.Sum256([]byte("two"))

	got, err := SignerOf(a, sign(a), b, sign(b))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("found %x, want %x", got, want)
	}

	// Two signatures from different keys share no candidate, and saying so is
	// better than picking one: it means the card is not behaving as assumed.
	other, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	sigOther := ecdsa.Sign(other, b[:])
	or, os := sigOther.R(), sigOther.S()
	rb, sb := or.Bytes(), os.Bytes()
	mixed := append(append([]byte(nil), rb[:]...), sb[:]...)
	if _, err := SignerOf(a, sign(a), b, mixed); err == nil {
		t.Error("accepted two signatures made by different keys")
	}

	// A malformed signature is refused rather than guessed at.
	if _, err := SignerOf(a, []byte("short"), b, sign(b)); err == nil {
		t.Error("accepted a signature of the wrong length")
	}
}

// keycard-go hands back an s that is two bytes short, and the two that are
// missing can be found because only one pair produces a signature that
// verifies. Reproduced here exactly as the library mangles it.
func TestRepairCardSignature(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey().SerializeCompressed()
	digest := sha256.Sum256([]byte("a card signature"))

	sig := ecdsa.Sign(priv, digest[:])
	r, sc := sig.R(), sig.S()
	rb, sb := r.Bytes(), sc.Bytes()
	good := append(append([]byte(nil), rb[:]...), sb[:]...)
	if !VerifyDigest(pub, digest, good) {
		t.Fatal("the fixture signature does not verify; the test proves nothing")
	}

	// What the library returns: s shifted two left, two junk bytes appended.
	broken := append([]byte(nil), good...)
	copy(broken[32:62], good[34:64])
	broken[62], broken[63] = 0x01, 0xd2
	if VerifyDigest(pub, digest, broken) {
		t.Fatal("the mangled signature verified; the fixture is wrong")
	}

	got, ok := RepairCardSignature(pub, digest, broken)
	if !ok {
		t.Fatal("could not recover the signature")
	}
	if !bytes.Equal(got, good) {
		t.Errorf("recovered %x, want %x", got, good)
	}

	// A signature that is already correct is returned untouched, without a
	// search — the common case once the library is fixed.
	same, ok := RepairCardSignature(pub, digest, good)
	if !ok || !bytes.Equal(same, good) {
		t.Error("a good signature was not passed straight through")
	}

	// Nonsense is refused rather than searched into something plausible.
	if _, ok := RepairCardSignature(pub, digest, make([]byte, 64)); ok {
		t.Error("recovered a signature from zeroes")
	}
}
