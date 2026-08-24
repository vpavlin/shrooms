package mobile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	ktypes "github.com/keycard-tech/keycard-go/v4/types"
)

// A pairing survives being written and read, because getting this wrong costs a
// pairing slot on a card that has very few and needs the PUK to recover them.
func TestPairingRoundTrip(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	in := ktypes.NewPairing(key, 3)

	out, err := decodePairing(encodePairing(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Index() != 3 {
		t.Errorf("slot index came back as %d, want 3", out.Index())
	}
	if out.Key() != key {
		t.Error("pairing key changed across the round trip")
	}
}

// A corrupt file must say so rather than producing a pairing that fails later
// inside the secure channel, where the error names nothing useful.
func TestCorruptPairingIsRefused(t *testing.T) {
	for _, s := range []string{"", "not base64!", "AAAA"} {
		if _, err := decodePairing(s); err == nil {
			t.Errorf("%q was accepted as a pairing", s)
		}
	}
}

// failing transport: no card in the field.
type deadCard struct{}

func (deadCard) Transmit([]byte) ([]byte, error) { return nil, errors.New("tag lost") }

// Before anything touches the card, an un-enrolled phone must say that plainly.
// The pairing file is what enrolment produces, so its absence is the check.
func TestUnenrolledCardIsReportedBeforeUse(t *testing.T) {
	dir := t.TempDir()
	if _, err := CardPublicKey(deadCard{}, dir, "123456"); err == nil {
		t.Error("an unenrolled card produced a public key")
	}
	// And with a pairing present, the failure comes from the card rather than
	// from the file — a different message, which is the point.
	if err := os.WriteFile(filepath.Join(dir, "keycard-pairing"),
		[]byte(encodePairing(ktypes.NewPairing([32]byte{1}, 0))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CardPublicKey(deadCard{}, dir, "123456"); err == nil {
		t.Error("a dead transport produced a public key")
	}
}

// A nil transport is a programming error on the Kotlin side and must not panic
// inside the card library.
func TestNilTransportIsAnError(t *testing.T) {
	if _, err := CardEnrol(nil, t.TempDir(), "x", "123456"); err == nil {
		t.Error("a nil transport was accepted")
	}
}
