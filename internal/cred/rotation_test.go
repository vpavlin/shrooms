package cred

import (
	"crypto/rand"
	"testing"
	"time"
)

func secret(t *testing.T) []byte {
	t.Helper()
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRotationRoundTripsAndVerifies(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	s := secret(t)
	now := time.Now()

	r, err := RotateWith(admin, auth, 7, 1787464532, s, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalRotation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Generation != 7 || back.Serial != 1787464532 {
		t.Errorf("round trip lost fields: %+v", back)
	}
	if err := VerifyRotationBy(auth, back); err != nil {
		t.Errorf("a good rotation did not verify: %v", err)
	}
	if !back.Commits(s) {
		t.Error("the rotation does not commit to the secret it was made for")
	}
	if back.Commits(secret(t)) {
		t.Error("the rotation committed to a different secret")
	}
}

// The commitment is the whole defence against a rogue member handing out a
// generation of its own choosing. If a member could substitute a secret and
// still pass, any member could silently take over the control plane.
func TestASubstitutedSecretIsRefused(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	real, fake := secret(t), secret(t)

	r, err := RotateWith(admin, auth, 1, 0, real, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Commits(fake) {
		t.Fatal("a member's own secret satisfied the admin's commitment")
	}
	// And the commitment itself is signed, so editing it breaks the statement.
	r.Commit = RotationCommit(fake)
	if err := VerifyRotationBy(auth, r); err == nil {
		t.Error("a rotation with a rewritten commitment still verified")
	}
}

// A rotation signed by another mesh's admin is valid and simply not ours.
// Accepting it would let anybody who runs a mesh move this one.
func TestARotationForAnotherMeshIsRefused(t *testing.T) {
	mine, _ := NewAdmin()
	myAuth, _ := NewAuthority(mine.Pub)
	theirs, _ := NewAdmin()
	theirAuth, _ := NewAuthority(theirs.Pub)

	r, err := RotateWith(theirs, theirAuth, 1, 0, secret(t), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRotationBy(myAuth, r); err == nil {
		t.Error("accepted another mesh's rotation")
	}
}

// A member cannot mint one: the statement is what carries the admin's decision,
// and a mesh where any member could rotate would let one member lock the rest
// out of the control plane.
func TestAMemberCannotMintARotation(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	member, _ := NewAdmin() // any other keypair

	r, err := RotateWith(member, auth, 1, 0, secret(t), time.Now())
	if err == nil {
		t.Fatal("a non-admin minted a rotation this mesh accepts")
	}
	if r != nil {
		t.Error("returned a rotation alongside the error")
	}
}

// Generation zero is the un-rotated mesh. Announcing it would be a statement
// that a mesh is at the generation every holder of the network key can read.
func TestGenerationZeroCannotBeAnnounced(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	if _, err := RotateWith(admin, auth, 0, 0, secret(t), time.Now()); err == nil {
		t.Error("signed a rotation to generation zero")
	}
}

func TestUnmarshalRotationRejectsMalformedInput(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	r, _ := RotateWith(admin, auth, 3, 0, secret(t), time.Now())
	good, _ := r.MarshalBinary()

	for _, c := range []struct {
		what string
		in   []byte
	}{
		{"empty", nil},
		{"truncated", good[:len(good)-1]},
		{"overlong", append(append([]byte(nil), good...), 0)},
		{"unknown version", append([]byte{9}, good[1:]...)},
	} {
		if _, err := UnmarshalRotation(c.in); err == nil {
			t.Errorf("%s: accepted", c.what)
		}
	}
	// A flipped bit anywhere in the signed body must break verification.
	for _, i := range []int{1, 20, 40, 60} {
		bad := append([]byte(nil), good...)
		bad[i] ^= 0x01
		back, err := UnmarshalRotation(bad)
		if err != nil {
			continue // rejected at parse, which is also fine
		}
		if err := VerifyRotationBy(auth, back); err == nil {
			t.Errorf("a rotation with byte %d flipped still verified", i)
		}
	}
}
