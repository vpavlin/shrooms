package mesh

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

func genMesh(t *testing.T) (*Mesh, *cred.Admin, *cred.Authority) {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	st, err := state.LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Identity = id
	m := &Mesh{
		nk: nk, st: st, authority: auth,
		revoked: cred.NewList(),
		roster:  NewRoster(nk, id.DevicePub),
		log:     slog.New(slog.DiscardHandler),
	}
	return m, admin, auth
}

func rotationFor(t *testing.T, admin *cred.Admin, auth *cred.Authority, gen, serial uint64, secret []byte) []byte {
	t.Helper()
	r, err := cred.RotateWith(admin, auth, gen, serial, secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAdoptingAGenerationChangesTheAnnounceKey(t *testing.T) {
	m, admin, auth := genMesh(t)
	secret := bytes.Repeat([]byte{3}, 32)

	before := m.keys()
	if err := m.adoptGeneration(rotationFor(t, admin, auth, 5, 0, secret), secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	if m.Generation() != 5 {
		t.Errorf("generation is %d, want 5", m.Generation())
	}
	// An announce sealed now must not open under what we used before.
	sealed, err := m.keys().Seal(1, m.st.Identity.DevicePriv, announceFor(t, m.st.Identity, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.OpenAnnounce(1, sealed, time.Now()); err == nil {
		t.Error("the pre-rotation keyring still opened a post-rotation announce")
	}
	if _, err := m.keys().OpenAnnounce(1, sealed, time.Now()); err != nil {
		t.Errorf("the current keyring could not open our own announce: %v", err)
	}
}

// The previous generation stays readable, or every peer becomes unreadable the
// moment we rotate and the mesh looks broken until each one is rekeyed.
func TestThePreviousGenerationIsStillRead(t *testing.T) {
	m, admin, auth := genMesh(t)
	first, second := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)

	if err := m.adoptGeneration(rotationFor(t, admin, auth, 1, 0, first), first, time.Now()); err != nil {
		t.Fatal(err)
	}
	old := m.keys()
	if err := m.adoptGeneration(rotationFor(t, admin, auth, 2, 0, second), second, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A peer still on generation 1.
	sealed, err := old.Seal(1, m.st.Identity.DevicePriv, announceFor(t, m.st.Identity, nil))
	if err != nil {
		t.Fatal(err)
	}
	var opened bool
	for _, kr := range m.readKeys() {
		if _, err := kr.OpenAnnounce(1, sealed, time.Now()); err == nil {
			opened = true
		}
	}
	if !opened {
		t.Error("a peer one generation behind became unreadable")
	}
}

// The floor. A revoked device holds the previous secret and can replay the
// public statement naming it.
func TestAnOlderGenerationIsRefused(t *testing.T) {
	m, admin, auth := genMesh(t)
	a, b := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)

	if err := m.adoptGeneration(rotationFor(t, admin, auth, 9, 0, a), a, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, gen := range []uint64{1, 8, 9} {
		if err := m.adoptGeneration(rotationFor(t, admin, auth, gen, 0, b), b, time.Now()); err == nil {
			t.Errorf("accepted generation %d over 9", gen)
		}
	}
	if m.Generation() != 9 {
		t.Errorf("generation moved to %d", m.Generation())
	}
}

// A member cannot substitute a secret of its own: the commitment is signed.
func TestASubstitutedGenerationSecretIsRefused(t *testing.T) {
	m, admin, auth := genMesh(t)
	real, forged := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{9}, 32)
	rot := rotationFor(t, admin, auth, 3, 0, real)

	if err := m.adoptGeneration(rot, forged, time.Now()); err == nil {
		t.Error("adopted a secret that does not match the admin's commitment")
	}
	if m.Generation() != 0 {
		t.Error("moved generation on a refused rekey")
	}
}

// A rotation signed by anybody but this mesh's admin is not a rotation.
func TestARotationFromAStrangerIsRefused(t *testing.T) {
	m, _, _ := genMesh(t)
	other, _ := cred.NewAdmin()
	otherAuth, _ := cred.NewAuthority(other.Pub)
	secret := bytes.Repeat([]byte{4}, 32)

	if err := m.adoptGeneration(rotationFor(t, other, otherAuth, 2, 0, secret), secret, time.Now()); err == nil {
		t.Error("adopted a generation signed by another mesh's admin")
	}
}

// A rotation must not be served before the revocation it enforces has arrived,
// or a device that was offline during the revoke rekeys and then answers the
// revoked device from a list that does not yet contain it.
func TestARotationWaitsForTheRevocationItEnforces(t *testing.T) {
	m, admin, auth := genMesh(t)
	secret := bytes.Repeat([]byte{6}, 32)
	dev, _ := identity.New()

	rot := rotationFor(t, admin, auth, 100, 100, secret)
	if err := m.adoptGeneration(rot, secret, time.Now()); err == nil {
		t.Fatal("adopted a rotation whose revocation has not arrived")
	}

	// Once the revocation is on the list, the same rotation is fine.
	rev, err := admin.Revoke(dev.DevicePub, 100, time.Time{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rawRev, err := rev.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	m.revoked.Add(rev, rawRev)
	if err := m.adoptGeneration(rot, secret, time.Now()); err != nil {
		t.Errorf("refused a rotation whose revocation we hold: %v", err)
	}
}
