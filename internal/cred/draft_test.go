package cred

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// A card authority, and a credential drafted for it, signed outside this
// package the way keycard-basecamp signs: over the digest, handed back in
// whichever form its stack produces.
func cardDraft(t *testing.T) (*secp256k1.PrivateKey, *Authority, []byte, [32]byte, time.Time) {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthority(ed25519.PublicKey(priv.PubKey().SerializeCompressed()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	key := bytes.Repeat([]byte{7}, 32)
	c, d, err := Draft(auth, key, key, key, "kitchen-pi", 0, now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !IsDraft(wire) {
		t.Fatal("a draft does not read as one")
	}
	return priv, auth, wire, d, now
}

func rs(sig *ecdsa.Signature) []byte {
	r, s := sig.R(), sig.S()
	rb, sb := r.Bytes(), s.Bytes()
	return append(append([]byte{}, rb[:]...), sb[:]...)
}

func TestFinishTakesEveryFormACardHandsOver(t *testing.T) {
	priv, auth, draft, d, now := cardDraft(t)
	sig := ecdsa.Sign(priv, d[:])
	flat := rs(sig)

	for name, form := range map[string][]byte{
		"r‖s":   flat,
		"r‖s‖v": append(append([]byte{}, flat...), 1),
		"DER":   sig.Serialize(),
	} {
		out, err := Finish(auth, draft, form, now)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if IsDraft(out) {
			t.Fatalf("%s: still a draft", name)
		}
		c, err := UnmarshalCredential(out)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != "kitchen-pi" || c.MeshID != auth.ID() || c.NotBefore != now.Add(-time.Minute).Unix() {
			t.Fatalf("%s: fields changed on the way: %+v", name, c)
		}
		if err := VerifyBy(auth, c, now); err != nil {
			t.Fatalf("%s: finished credential does not verify: %v", name, err)
		}
	}
}

// A card is not obliged to make a low s, and the strict DER parsers refuse a
// high one — which would turn a good signature into "the card failed".
func TestFinishTakesAHighS(t *testing.T) {
	priv, auth, draft, d, now := cardDraft(t)
	sig := ecdsa.Sign(priv, d[:])
	r, s := sig.R(), sig.S()
	s.Negate() // n - s: the same signature, high
	high := ecdsa.NewSignature(&r, &s)
	der := highDER(high)
	if _, err := Finish(auth, draft, der, now); err != nil {
		t.Fatalf("a high-s DER signature was refused: %v", err)
	}
}

// DER by hand, because Serialize canonicalises s.
func highDER(sig *ecdsa.Signature) []byte {
	r, s := sig.R(), sig.S()
	enc := func(v [32]byte) []byte {
		b := v[:]
		for len(b) > 1 && b[0] == 0 && b[1] < 0x80 {
			b = b[1:]
		}
		if b[0] >= 0x80 {
			b = append([]byte{0}, b...)
		}
		return append([]byte{0x02, byte(len(b))}, b...)
	}
	body := append(enc(r.Bytes()), enc(s.Bytes())...)
	return append([]byte{0x30, byte(len(body))}, body...)
}

func TestFinishRefusesAnotherKeyOrAnotherDraft(t *testing.T) {
	_, auth, draft, d, now := cardDraft(t)
	other, _ := secp256k1.GeneratePrivateKey()
	if _, err := Finish(auth, draft, rs(ecdsa.Sign(other, d[:])), now); err == nil {
		t.Fatal("a signature by a key outside admin_keys was accepted")
	}
	// A draft for another mesh, signed by its own key: refused here, where
	// the authority is this mesh's.
	priv2, auth2, draft2, d2, _ := cardDraft(t)
	if _, err := Finish(auth, draft2, rs(ecdsa.Sign(priv2, d2[:])), now); err == nil {
		t.Fatal("a draft for another mesh was finished")
	}
	_ = auth2
	// A draft whose fields were changed after the digest was taken.
	priv3, auth3, draft3, d3, _ := cardDraft(t)
	tampered := append([]byte{}, draft3...)
	tampered[1+MeshIDLen] ^= 1 // the device key
	if _, err := Finish(auth3, tampered, rs(ecdsa.Sign(priv3, d3[:])), now); err == nil {
		t.Fatal("a tampered draft was finished")
	}
}

func TestSignatureFromRefusesWhatItCannotRead(t *testing.T) {
	for _, b := range [][]byte{nil, make([]byte, 10), make([]byte, 70), {0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01}} {
		if _, err := SignatureFrom(b); err == nil {
			t.Errorf("%x read as a signature", b)
		}
	}
}
