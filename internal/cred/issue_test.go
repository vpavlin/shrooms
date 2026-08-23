package cred

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

// twoKeyMesh is the ordinary shape: an authority of two admins (ADR-018), one
// of which is doing the signing.
func twoKeyMesh(t *testing.T) (*Admin, *Authority) {
	t.Helper()
	a, err := NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	spare, err := NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthority(a.Pub, spare.Pub)
	if err != nil {
		t.Fatal(err)
	}
	return a, auth
}

// What both the desktop and the phone now depend on: a credential issued
// through this path is one the mesh accepts. Everything else here is about the
// ways that can fail.
func TestIssueForProducesSomethingTheMeshAccepts(t *testing.T) {
	admin, auth := twoKeyMesh(t)
	dev, wg := device(t)
	now := time.Now()

	raw, err := IssueFor(admin, auth, dev, wg, nil, "phone", 0, now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := UnmarshalCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBy(auth, c, now); err != nil {
		t.Fatalf("a credential issued through IssueFor did not verify: %v", err)
	}
	// The mesh id is the authority's, not the signer's. Getting this wrong
	// produces a credential that verifies against the admin who signed it and
	// is refused by every peer, which is the confusing failure IssueFor exists
	// to prevent.
	if c.MeshID != auth.ID() {
		t.Error("the credential was stamped with a mesh id that is not the authority's")
	}
	if c.Name != "phone" {
		t.Errorf("name came back as %q", c.Name)
	}
}

// A serial of zero means "now", because a revocation withdraws a serial and
// everything below it: re-issuing at a fixed serial would land the new
// credential inside the range an old revocation already covers.
func TestZeroSerialBecomesTheClock(t *testing.T) {
	admin, auth := twoKeyMesh(t)
	dev, wg := device(t)
	now := time.Unix(1_700_000_000, 0)

	raw, err := IssueFor(admin, auth, dev, wg, nil, "phone", 0, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := UnmarshalCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Serial != uint64(now.Unix()) {
		t.Errorf("serial is %d, want the clock %d", c.Serial, now.Unix())
	}
}

// An admin key that is not in this mesh's set cannot issue for it. The check
// that catches this is the verification inside IssueFor, which is why it is
// there rather than left to the caller.
func TestAStrangerCannotIssueForThisMesh(t *testing.T) {
	_, auth := twoKeyMesh(t)
	stranger, err := NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	dev, wg := device(t)

	if _, err := IssueFor(stranger, auth, dev, wg, nil, "phone", 0, time.Now(), time.Hour); err == nil {
		t.Fatal("an admin outside the authority issued a credential for it")
	}
}

// brokenCard signs with the wrong key while reporting the right public one.
//
// This is the failure a file-backed key cannot have and a smartcard can: a
// well-formed response that is not a signature by the key it claims. A tag that
// moves mid-operation, a card paired to a different mesh, a driver returning
// the wrong slot — all land here.
type brokenCard struct {
	pub   ed25519.PublicKey
	other ed25519.PrivateKey
}

func (b brokenCard) Public() ed25519.PublicKey { return b.pub }
func (b brokenCard) SignDigest(d [32]byte) ([]byte, error) {
	return ed25519.Sign(b.other, d[:]), nil
}

// The reason IssueFor verifies before returning: the alternative is finding out
// on somebody else's device, days later, when the credential is the only
// evidence left and the card is in a drawer.
func TestACardThatSignsWrongIsCaughtHere(t *testing.T) {
	admin, auth := twoKeyMesh(t)
	impostor, err := NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	dev, wg := device(t)

	card := brokenCard{pub: admin.Pub, other: impostor.Priv}
	_, err = IssueFor(card, auth, dev, wg, nil, "phone", 0, time.Now(), time.Hour)
	if err == nil {
		t.Fatal("a credential signed by the wrong key was returned as valid")
	}
	// The message has to name the signer, because the person holding the card
	// is the only one who can do anything about it.
	if !strings.Contains(err.Error(), "signer") {
		t.Errorf("the error does not point at the signer: %v", err)
	}
}

// deadCard is a tag that left the field mid-signature.
type deadCard struct{ pub ed25519.PublicKey }

func (d deadCard) Public() ed25519.PublicKey         { return d.pub }
func (deadCard) SignDigest([32]byte) ([]byte, error) { return nil, errors.New("tag lost") }

// A card removed mid-operation must surface as itself rather than as a
// malformed credential, since the fix is to tap again.
func TestACardRemovedMidSignatureSaysSo(t *testing.T) {
	admin, auth := twoKeyMesh(t)
	dev, wg := device(t)

	_, err := IssueFor(deadCard{pub: admin.Pub}, auth, dev, wg, nil, "phone", 0, time.Now(), time.Hour)
	if err == nil {
		t.Fatal("a lost tag produced a credential")
	}
	if !strings.Contains(err.Error(), "tag lost") {
		t.Errorf("the card's own error was swallowed: %v", err)
	}
}

// Issuing against no authority is a programming error on the mobile side,
// where the authority comes from a running mesh that may have none.
func TestIssuingWithoutAnAuthorityIsRefused(t *testing.T) {
	admin, _ := twoKeyMesh(t)
	dev, wg := device(t)
	if _, err := IssueFor(admin, nil, dev, wg, nil, "phone", 0, time.Now(), time.Hour); err == nil {
		t.Fatal("a credential was issued against no authority")
	}
}
