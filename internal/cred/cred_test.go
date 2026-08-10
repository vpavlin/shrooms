package cred

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func device(t *testing.T) (ed25519.PublicKey, []byte) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wg := make([]byte, 32)
	if _, err := rand.Read(wg); err != nil {
		t.Fatal(err)
	}
	return pub, wg
}

func TestIssueAndVerify(t *testing.T) {
	admin, err := NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	dev, wg := device(t)
	now := time.Now()

	c, err := admin.Issue(dev, wg, "laptop", 1, now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(admin.Pub, c, now); err != nil {
		t.Fatalf("a freshly issued credential did not verify: %v", err)
	}
}

// The point of the whole design: the admin key is not on the device, so a
// device cannot mint its own membership. Holding a credential proves the admin
// admitted you; it does not let you admit anyone.
func TestADeviceCannotMintItsOwnMembership(t *testing.T) {
	admin, _ := NewAdmin()
	impostor, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	forged, err := impostor.Issue(dev, wg, "laptop", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Signed by the wrong authority, and claiming the wrong mesh with it.
	if err := Verify(admin.Pub, forged, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a credential from another authority verified as %v", err)
	}
}

// Every signed field must be covered. Tampering with any of them has to break
// the signature — otherwise a member could promote themselves by editing a
// credential they already hold.
func TestTamperingBreaksTheSignature(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	other, otherWG := device(t)
	now := time.Now()

	base, err := admin.Issue(dev, wg, "laptop", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what   string
		mutate func(*Credential)
	}{
		{"device key", func(c *Credential) { c.DevicePub = other }},
		{"wireguard key", func(c *Credential) { c.WGPub = otherWG }},
		{"name", func(c *Credential) { c.Name = "someone-else" }},
		{"serial", func(c *Credential) { c.Serial = 99 }},
		{"expiry", func(c *Credential) { c.NotAfter = now.Add(100 * 24 * time.Hour).Unix() }},
		{"start", func(c *Credential) { c.NotBefore = now.Add(-time.Hour).Unix() }},
		{"mesh", func(c *Credential) { c.MeshID = MeshID{1, 2, 3} }},
	} {
		tampered := *base
		c.mutate(&tampered)
		if err := Verify(admin.Pub, &tampered, now); err == nil {
			t.Errorf("editing the %s left the credential valid", c.what)
		}
	}
}

// Expiry is what bounds a suppressed revocation: a gossip bus lets an attacker
// drop a revocation it cannot forge, so a credential must stop being valid on
// its own.
func TestExpiryIsEnforced(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	c, err := admin.Issue(dev, wg, "laptop", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(admin.Pub, c, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("an expired credential verified as %v", err)
	}
	if err := Verify(admin.Pub, c, now.Add(-time.Hour)); !errors.Is(err, ErrNotYetValid) {
		t.Errorf("a credential verified before it was valid: %v", err)
	}
}

// A forged credential must report a bad signature, not a helpful diagnosis of
// what else was wrong with it.
func TestSignatureIsCheckedBeforeAnythingElse(t *testing.T) {
	admin, _ := NewAdmin()
	impostor, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	// Forged AND long expired: the signature must be the complaint.
	c, err := impostor.Issue(dev, wg, "laptop", 1, now.Add(-48*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(admin.Pub, c, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("got %v, want a signature failure — anything else tells a forger "+
			"their forgery was otherwise acceptable", err)
	}
}

func TestRevocation(t *testing.T) {
	admin, _ := NewAdmin()
	impostor, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	c, _ := admin.Issue(dev, wg, "phone", 4, now, time.Hour)

	r, err := admin.Revoke(dev, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRevocation(admin.Pub, r); err != nil {
		t.Fatalf("a genuine revocation did not verify: %v", err)
	}
	if !r.Revokes(c) {
		t.Error("a revocation did not withdraw the credential it names")
	}

	// Only the admin may revoke; otherwise any member could eject any other.
	fake, _ := impostor.Revoke(dev, 4, now)
	if err := VerifyRevocation(admin.Pub, fake); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a revocation from another authority verified as %v", err)
	}
}

// Anyone can rebroadcast a revocation, so an old one must not withdraw a newer
// credential — otherwise re-enrolling a device could be undone by replaying the
// message that removed it.
func TestAnOldRevocationDoesNotWithdrawAReissuedCredential(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	old, _ := admin.Issue(dev, wg, "phone", 4, now, time.Hour)
	r, _ := admin.Revoke(dev, 4, now)
	if !r.Revokes(old) {
		t.Fatal("the revocation does not withdraw the credential it was made for")
	}

	// The device is admitted again, with a higher serial.
	reissued, _ := admin.Issue(dev, wg, "phone", 5, now, time.Hour)
	if r.Revokes(reissued) {
		t.Error("replaying an old revocation withdrew a later credential")
	}
}

// The mesh's identity is public and derived, so a joining device can compute
// where to look before it holds any secret — which is what lets enrolment
// happen without one.
func TestMeshIdentityIsPublicAndStable(t *testing.T) {
	admin, _ := NewAdmin()

	id := MeshOf(admin.Pub)
	if MeshOf(admin.Pub) != id {
		t.Error("the mesh id is not stable for one admin key")
	}

	other, _ := NewAdmin()
	if MeshOf(other.Pub) == id {
		t.Error("two admin keys produced the same mesh id")
	}

	round, err := ParseMeshID(id.String())
	if err != nil || round != id {
		t.Errorf("mesh id did not survive a round trip: %v %v", round, err)
	}

	p := id.Prefix()
	if p.Bits() != 48 || p.Addr().As16()[0] != 0xfd {
		t.Errorf("prefix %v is not a /48 ULA", p)
	}
	if other := MeshOf(other.Pub).Prefix(); other == p {
		t.Error("two meshes derived the same prefix")
	}
}

// A credential has to fit in an announce, which is padded to a fixed size. This
// is the measurement that decided the design: at 512 bytes there was no room
// for one at all, which is why readers now accept 1024.
func TestCredentialSizeAgainstTheAnnounceBudget(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)

	c, err := admin.Issue(dev, wg, "a-fairly-long-device-name", 4294967295,
		time.Now(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("credential is %d bytes as JSON", len(raw))

	// The announce carries a few hundred bytes of its own, so a credential must
	// leave room beside them. 400 is comfortably inside 1024 with an announce
	// and its base64 expansion; anything approaching it means the encoding needs
	// revisiting before this ships, not after.
	if len(raw) > 400 {
		t.Errorf("credential is %d bytes, too large to ride an announce", len(raw))
	}
}
