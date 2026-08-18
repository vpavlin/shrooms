package cred

import (
	"crypto/ed25519"
	"crypto/rand"
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

	r, err := admin.Revoke(dev, 4, time.Time{}, now)
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
	fake, _ := impostor.Revoke(dev, 4, time.Time{}, now)
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
	r, _ := admin.Revoke(dev, 4, time.Time{}, now)
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
	raw, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("credential is %d bytes on the wire", len(raw))

	// Measured, not guessed: an announce carrying a credential is base64'd into
	// JSON and then base64'd again inside the envelope, and the whole thing must
	// fit a fixed padding. 320 bytes was the ceiling found by bisecting against
	// Seal; the JSON encoding was 364 and did not fit at all, which is why this
	// is binary.
	if len(raw) > 320 {
		t.Errorf("credential is %d bytes, too large to ride an announce", len(raw))
	}

	// And it must survive the round trip unchanged, including its signature.
	back, err := UnmarshalCredential(raw)
	if err != nil {
		t.Fatalf("a credential we just wrote did not parse: %v", err)
	}
	if err := Verify(admin.Pub, back, time.Now()); err != nil {
		t.Errorf("a credential did not verify after a round trip: %v", err)
	}
	if back.Name != c.Name || back.Serial != c.Serial || back.NotAfter != c.NotAfter {
		t.Error("fields changed across the round trip")
	}
}

// The parser reads bytes that arrived from a mesh member, who may be hostile,
// and it runs before anything is verified — so every malformed shape must be
// refused rather than panic.
func TestUnmarshalRejectsMalformedInput(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	c, _ := admin.Issue(dev, wg, "laptop", 1, time.Now(), time.Hour)
	good, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range [][]byte{
		nil,
		{},
		{1},
		good[:len(good)-1],                      // truncated signature
		good[:credFixed],                        // no signature at all
		append([]byte{9}, good[1:]...),          // unsupported version
		append(append([]byte(nil), good...), 0), // trailing junk
	} {
		if _, err := UnmarshalCredential(bad); err == nil {
			t.Errorf("accepted a malformed credential of %d bytes", len(bad))
		}
	}

	// A name length that disagrees with the buffer must not read past the end.
	tampered := append([]byte(nil), good...)
	tampered[credFixed-1] = 200
	if _, err := UnmarshalCredential(tampered); err == nil {
		t.Error("accepted a credential whose name length exceeds its buffer")
	}
}

func TestRevocationRoundTrip(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	r, err := admin.Revoke(dev, 7, time.Time{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalRevocation(raw)
	if err != nil {
		t.Fatalf("a revocation we just wrote did not parse: %v", err)
	}
	if err := VerifyRevocation(admin.Pub, back); err != nil {
		t.Errorf("did not verify after a round trip: %v", err)
	}
	if back.Serial != 7 {
		t.Errorf("serial came back as %d", back.Serial)
	}
	for _, bad := range [][]byte{nil, raw[:len(raw)-1], append(append([]byte(nil), raw...), 0)} {
		if _, err := UnmarshalRevocation(bad); err == nil {
			t.Errorf("accepted a malformed revocation of %d bytes", len(bad))
		}
	}
}

// The lifetime is the admin's to choose, per device, and lives inside the
// signature — so a holder cannot extend it. That is the property revocation
// depends on: a device able to configure its own expiry has undone it.
func TestLifetimeIsChosenByTheIssuerAndSigned(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	short, err := admin.Issue(dev, wg, "phone", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	long, err := admin.Issue(dev, wg, "vps", 2, now, 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if long.NotAfter <= short.NotAfter {
		t.Error("a longer lifetime did not produce a later expiry")
	}

	// Both are honoured as issued: the library does not impose a policy the
	// admin did not ask for.
	if err := Verify(admin.Pub, short, now.Add(30*time.Minute)); err != nil {
		t.Errorf("a short-lived credential was rejected inside its window: %v", err)
	}
	if err := Verify(admin.Pub, long, now.Add(300*24*time.Hour)); err != nil {
		t.Errorf("a long-lived credential was rejected inside its window: %v", err)
	}

	// And the holder cannot extend it.
	stretched := *short
	stretched.NotAfter = now.Add(10 * 365 * 24 * time.Hour).Unix()
	if err := Verify(admin.Pub, &stretched, now); err == nil {
		t.Error("a holder extended its own expiry")
	}

	if RenewBefore(DefaultLife) >= DefaultLife {
		t.Error("renewal would start before the credential was issued")
	}
}

// The mesh id commits to the whole set of admin keys, and the address prefix
// derives from the mesh id. So the set is fixed at mint: adding a key later
// changes every address on every node. That is why two keys exist from birth —
// one for recovery, one to become the renewal key.
func TestAuthorityIsFixedAtMint(t *testing.T) {
	root, _ := NewAdmin()
	backup, _ := NewAdmin()

	one, err := NewAuthority(root.Pub)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewAuthority(root.Pub, backup.Pub)
	if err != nil {
		t.Fatal(err)
	}
	if one.ID() == two.ID() {
		t.Fatal("adding an admin key did not change the mesh id; the id does not commit to the set")
	}
	if one.ID().Prefix() == two.ID().Prefix() {
		t.Error("two different meshes derived the same prefix")
	}

	// Order must not matter, or the same set would name two meshes depending on
	// how it happened to be assembled.
	flipped, err := NewAuthority(backup.Pub, root.Pub)
	if err != nil {
		t.Fatal(err)
	}
	if flipped.ID() != two.ID() {
		t.Error("the mesh id depends on the order the keys were given")
	}

	if _, err := NewAuthority(); err == nil {
		t.Error("a mesh with no admin key was accepted")
	}
	if _, err := NewAuthority(root.Pub, root.Pub); err == nil {
		t.Error("the same key twice was accepted")
	}
}

// Any trusted key may sign membership: the root that enrolled a device, or the
// renewal key that later extended it. A verifier does not care which.
func TestAnyTrustedKeyMaySignMembership(t *testing.T) {
	root, _ := NewAdmin()
	renewer, _ := NewAdmin()
	stranger, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	auth, err := NewAuthority(root.Pub, renewer.Pub)
	if err != nil {
		t.Fatal(err)
	}

	// Both trusted keys issue for this mesh. The mesh id is the authority's, not
	// the signer's, so each must name it.
	for _, signer := range []*Admin{root, renewer} {
		c, err := signer.Issue(dev, wg, "laptop", 1, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		c.MeshID = auth.ID()
		d, _ := c.Digest()
		c.Sig = signRaw(signer, d)

		if err := VerifyBy(auth, c, now); err != nil {
			t.Errorf("a credential from a trusted key was rejected: %v", err)
		}
	}

	// And a key the mesh never trusted cannot.
	c, _ := stranger.Issue(dev, wg, "laptop", 1, now, time.Hour)
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signRaw(stranger, d)
	if err := VerifyBy(auth, c, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an untrusted key signed membership: %v", err)
	}
}

func signRaw(a *Admin, d [32]byte) []byte { return ed25519.Sign(a.Priv, d[:]) }

// Signing a digest rather than the body is what lets the root live on a
// Keycard: a card signs a fixed-size input with an algorithm chosen per call,
// so 32 bytes works whatever it supports.
func TestSignatureCoversADigestOfEverything(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()

	c, err := admin.Issue(dev, wg, "laptop", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 32 {
		t.Fatalf("digest is %d bytes; a card signs 32", len(d))
	}
	// The digest must move when any signed field does, or it would not cover it.
	before := d
	c.Serial = 99
	after, _ := c.Digest()
	if before == after {
		t.Error("the digest did not change when a signed field did")
	}
}
