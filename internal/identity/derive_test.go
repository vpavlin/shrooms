package identity

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

// Two meshes must not see the same device, or anyone in both can correlate
// them — which is the leak per-mesh derivation exists to close.
func TestPerMeshIdentitiesDiffer(t *testing.T) {
	m, err := NewMaster()
	if err != nil {
		t.Fatal(err)
	}
	home, err := m.Derive("aaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := m.Derive("bbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(home.DevicePub, shared.DevicePub) {
		t.Error("the same device key in two meshes")
	}
	if home.WGPub == shared.WGPub {
		// WireGuard allows one preshared key per peer per device, and our PSK
		// is per mesh — so a peer in two meshes cannot be represented at all
		// unless its public key differs between them.
		t.Error("the same tunnel key in two meshes")
	}
	if home.WGPriv == shared.WGPriv {
		t.Error("the same tunnel private key in two meshes")
	}
}

// Derivation must be a function, not a generator: a device that came back with
// different keys after a restart would look like a new device to every peer.
func TestDerivationIsStable(t *testing.T) {
	m, _ := NewMaster()
	a, _ := m.Derive("home")
	b, _ := m.Derive("home")

	if !bytes.Equal(a.DevicePriv, b.DevicePriv) || a.WGPriv != b.WGPriv {
		t.Error("deriving the same mesh twice gave different keys")
	}
}

func TestDifferentMastersDiffer(t *testing.T) {
	a, _ := NewMaster()
	b, _ := NewMaster()
	ia, _ := a.Derive("home")
	ib, _ := b.Derive("home")

	if bytes.Equal(ia.DevicePub, ib.DevicePub) {
		t.Fatal("two devices derived the same identity")
	}
}

// The derived keys must be usable, not merely distinct: a clamped Curve25519
// key and a public half that actually corresponds to the private one.
func TestDerivedKeysAreValid(t *testing.T) {
	m, _ := NewMaster()
	id, err := m.Derive("home")
	if err != nil {
		t.Fatal(err)
	}
	if id.WGPriv[0]&7 != 0 || id.WGPriv[31]&128 != 0 || id.WGPriv[31]&64 == 0 {
		t.Errorf("tunnel key is not clamped: %x", id.WGPriv)
	}
	pub, err := PublicFromPrivate(id.WGPriv)
	if err != nil {
		t.Fatal(err)
	}
	if pub != id.WGPub {
		t.Error("the public tunnel key does not match the private one")
	}
	// And the device key signs and verifies, which is what the announce needs.
	msg := []byte("hello")
	if !ed25519.Verify(id.DevicePub, msg, ed25519.Sign(id.DevicePriv, msg)) {
		t.Error("the derived device key does not verify its own signature")
	}
}

// The sealing key must be a third key, not a rename of one of the other two,
// and must be as deterministic as they are — it is derived on every start and
// never stored, so a device that derived a different one each time could not be
// sent anything.
func TestSealingKeyIsItsOwnKey(t *testing.T) {
	m, err := NewMaster()
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Derive("mesh-one")
	if err != nil {
		t.Fatal(err)
	}
	if id.SealPub == (WGKey{}) || id.SealPriv == (WGKey{}) {
		t.Fatal("no sealing key was derived")
	}
	// Distinct from the tunnel key. Sharing one static across WireGuard's Noise
	// and the control plane is the reuse this key exists to avoid.
	if id.SealPriv == id.WGPriv || id.SealPub == id.WGPub {
		t.Error("the sealing key is the WireGuard key")
	}
	if bytes.Equal(id.SealPub[:], id.DevicePub) {
		t.Error("the sealing key is the signing key")
	}
	// Deterministic: derived again from the same master and mesh, identical.
	again, err := m.Derive("mesh-one")
	if err != nil {
		t.Fatal(err)
	}
	if again.SealPriv != id.SealPriv {
		t.Error("the sealing key is not deterministic; nothing could be sent to it")
	}
	// Per-mesh, like everything else here: the same device on two meshes must
	// not present one key to both, which is what per-mesh identities are for.
	other, err := m.Derive("mesh-two")
	if err != nil {
		t.Fatal(err)
	}
	if other.SealPriv == id.SealPriv {
		t.Error("the same sealing key on two meshes")
	}
	// Clamped, so it is a usable X25519 scalar.
	if id.SealPriv[0]&7 != 0 || id.SealPriv[31]&128 != 0 || id.SealPriv[31]&64 == 0 {
		t.Errorf("sealing key is not clamped: %x", id.SealPriv)
	}
	// And it really is the public half of the private one.
	pub, err := PublicFromPrivate(id.SealPriv)
	if err != nil {
		t.Fatal(err)
	}
	if pub != id.SealPub {
		t.Error("SealPub is not the public half of SealPriv")
	}
}

// Every path that produces an Identity must produce a sealing key.
//
// The hazard is specific and silent: PublicFromPrivate accepts an all-zero
// scalar without error and returns an ordinary-looking public key whose private
// half is thirty-two zero bytes. A constructor that forgot to fill this in
// would have every device advertising the SAME sealing key, with anything
// sealed to it readable by anybody — and nothing would fail loudly.
func TestNoConstructorLeavesTheSealingKeyZero(t *testing.T) {
	// The all-zero scalar's public key: what a forgotten field looks like.
	var zero WGKey
	zeroPub, err := PublicFromPrivate(zero)
	if err != nil {
		t.Fatalf("expected the zero scalar to be accepted silently: %v", err)
	}

	m, err := NewMaster()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := m.Derive("mesh-one")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// The shape the state loader builds: fields set by hand, then filled in.
	rebuilt := &Identity{DevicePriv: fresh.DevicePriv, DevicePub: fresh.DevicePub}
	if err := rebuilt.DeriveSealing(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what string
		id   *Identity
	}{
		{"Master.Derive", derived},
		{"identity.New", fresh},
		{"rebuilt from disk", rebuilt},
	} {
		if c.id.SealPriv == (WGKey{}) {
			t.Errorf("%s: sealing private key is zero", c.what)
		}
		if c.id.SealPub == (WGKey{}) || c.id.SealPub == zeroPub {
			t.Errorf("%s: sealing public key is the all-zero scalar's — "+
				"every device would share it and anything sealed to it is public", c.what)
		}
	}

	// A rebuilt identity must match the one it was rebuilt from, or a restart
	// changes the address people seal to and rekeys stop arriving.
	if rebuilt.SealPriv != fresh.SealPriv {
		t.Error("the sealing key changed when the identity was rebuilt from disk")
	}
}
