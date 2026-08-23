package control

import (
	"bytes"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

func rekeyFixture(t *testing.T) (identity.NetworkKey, *identity.Identity, *identity.Identity, *cred.Rotation, []byte) {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	secret := bytes.Repeat([]byte{0x5a}, 32)
	rot, err := cred.RotateWith(admin, auth, 4, 99, secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return nk, sender, recipient, rot, secret
}

func TestRekeyReachesItsRecipient(t *testing.T) {
	nk, sender, recipient, rot, secret := rekeyFixture(t)
	rotRaw, _ := rot.MarshalBinary()
	now := time.Now()

	sealed, err := SealRekey(nk, 1, sender.DevicePriv, rotRaw,
		recipient.SealPub[:], secret, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRekey(nk, 1, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.For(recipient.SealPub) {
		t.Error("the envelope is not addressed to the recipient")
	}
	out, err := got.Unseal(recipient.SealPriv)
	if err != nil {
		t.Fatalf("the recipient could not open its own envelope: %v", err)
	}
	if !bytes.Equal(out, secret) {
		t.Error("the secret did not survive")
	}
	// And it is the secret the admin committed to.
	back, err := cred.UnmarshalRotation(got.Rotation)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Commits(out) {
		t.Error("the delivered secret does not match the signed commitment")
	}
}

// The outer envelope is sealed at generation zero on purpose: it has to reach a
// device that does not have the current generation. Sealing it under the
// generation it delivers would be a lock with its key inside.
func TestARekeyIsReadableByADeviceThatMissedTheRotation(t *testing.T) {
	nk, sender, recipient, rot, secret := rekeyFixture(t)
	rotRaw, _ := rot.MarshalBinary()
	now := time.Now()

	sealed, err := SealRekey(nk, 1, sender.DevicePriv, rotRaw,
		recipient.SealPub[:], secret, now)
	if err != nil {
		t.Fatal(err)
	}
	// A device with the network key and no generation at all.
	if _, err := OpenRekey(nk, 1, sealed, now); err != nil {
		t.Errorf("a device at generation zero could not read the envelope: %v", err)
	}
}

// A holder of the network key sees the envelope go past and cannot read it.
// That is the revoked device, and it is the whole point.
func TestTheNetworkKeyDoesNotOpenTheBox(t *testing.T) {
	nk, sender, recipient, rot, secret := rekeyFixture(t)
	rotRaw, _ := rot.MarshalBinary()
	now := time.Now()
	other, _ := identity.New()

	sealed, _ := SealRekey(nk, 1, sender.DevicePriv, rotRaw,
		recipient.SealPub[:], secret, now)
	got, err := OpenRekey(nk, 1, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.For(other.SealPub) {
		t.Error("an envelope for one device claimed to be for another")
	}
	if _, err := got.Unseal(other.SealPriv); err == nil {
		t.Error("a device opened an envelope addressed to somebody else")
	}
}

// Anyone can address an envelope to this device and put anything in it. What
// stops a member injecting a generation of its own is the commitment, not the
// sealing — so Unseal must not be mistaken for a check.
func TestAnInjectedSecretFailsTheCommitment(t *testing.T) {
	nk, sender, recipient, rot, _ := rekeyFixture(t)
	rotRaw, _ := rot.MarshalBinary()
	now := time.Now()
	forged := bytes.Repeat([]byte{0x11}, 32)

	// A member seals its OWN secret, with the admin's real statement attached.
	sealed, err := SealRekey(nk, 1, sender.DevicePriv, rotRaw,
		recipient.SealPub[:], forged, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRekey(nk, 1, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	out, err := got.Unseal(recipient.SealPriv)
	if err != nil {
		t.Fatal("the box opened, as it must — sealing is not the check")
	}
	back, _ := cred.UnmarshalRotation(got.Rotation)
	if back.Commits(out) {
		t.Error("an injected secret satisfied the admin's commitment")
	}
}

// A box lifted out of one envelope and replayed inside another, addressed
// elsewhere, must not open: both public keys are bound into the derivation.
func TestABoxCannotBeMovedBetweenEnvelopes(t *testing.T) {
	nk, sender, recipient, rot, secret := rekeyFixture(t)
	rotRaw, _ := rot.MarshalBinary()
	now := time.Now()
	victim, _ := identity.New()

	a, _ := SealRekey(nk, 1, sender.DevicePriv, rotRaw, recipient.SealPub[:], secret, now)
	b, _ := SealRekey(nk, 1, sender.DevicePriv, rotRaw, victim.SealPub[:], secret, now)
	openedA, _ := OpenRekey(nk, 1, a, now)
	openedB, _ := OpenRekey(nk, 1, b, now)

	// Graft A's box and ephemeral key onto B's addressing.
	openedB.Box = openedA.Box
	if _, err := openedB.Unseal(victim.SealPriv); err == nil {
		t.Error("a box opened inside an envelope it was not sealed for")
	}
}
