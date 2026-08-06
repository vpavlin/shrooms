package identity

import (
	"crypto/ed25519"
	"testing"
)

func mustNK(t *testing.T) NetworkKey {
	t.Helper()
	nk, err := NewNetworkKey()
	if err != nil {
		t.Fatalf("NewNetworkKey: %v", err)
	}
	return nk
}

func TestNetworkKeyRoundTrip(t *testing.T) {
	nk := mustNK(t)
	got, err := ParseNetworkKey(nk.String())
	if err != nil {
		t.Fatalf("ParseNetworkKey: %v", err)
	}
	if got != nk {
		t.Fatalf("round trip changed the key")
	}
}

func TestParseNetworkKeyRejectsWrongLength(t *testing.T) {
	if _, err := ParseNetworkKey("AAAA"); err == nil {
		t.Fatal("expected an error for a short key")
	}
}

// The prefix must be a valid RFC 4193 ULA (fd00::/8) and a /48. Using fc00::/8
// would collide with real ULA space; 0200::/7 is Yggdrasil's.
func TestPrefixIsULA(t *testing.T) {
	p := mustNK(t).Prefix()
	if p.Bits() != 48 {
		t.Errorf("prefix is /%d, want /48", p.Bits())
	}
	if b := p.Addr().As16()[0]; b != 0xfd {
		t.Errorf("prefix starts %#x, want 0xfd (RFC 4193 ULA)", b)
	}
}

func TestPrefixDiffersPerNetwork(t *testing.T) {
	if mustNK(t).Prefix() == mustNK(t).Prefix() {
		t.Fatal("two networks derived the same prefix")
	}
}

// Every node computes every other node's address locally; if this were not
// deterministic the mesh would need an allocator, which is the thing derived
// addressing exists to delete.
func TestOverlayAddrIsDeterministic(t *testing.T) {
	nk := mustNK(t)
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if OverlayAddr(nk, pub) != OverlayAddr(nk, pub) {
		t.Fatal("OverlayAddr is not deterministic")
	}
}

func TestOverlayAddrInsidePrefix(t *testing.T) {
	nk := mustNK(t)
	for i := 0; i < 32; i++ {
		pub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		addr := OverlayAddr(nk, pub)
		if !nk.Prefix().Contains(addr) {
			t.Fatalf("addr %s outside prefix %s", addr, nk.Prefix())
		}
	}
}

func TestOverlayAddrDiffersPerDevice(t *testing.T) {
	nk := mustNK(t)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		pub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		a := OverlayAddr(nk, pub).String()
		if seen[a] {
			t.Fatalf("duplicate overlay address %s", a)
		}
		seen[a] = true
	}
}

// The same device key in two different networks must not land on the same
// address, or joining a second mesh would leak membership of the first.
func TestOverlayAddrDiffersPerNetwork(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if OverlayAddr(mustNK(t), pub) == OverlayAddr(mustNK(t), pub) {
		t.Fatal("same address across two networks")
	}
}

func TestNewIdentityKeysAreDistinctAndClamped(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if id.WGPriv.IsZero() || id.WGPub.IsZero() {
		t.Fatal("wireguard key unset")
	}
	if id.WGPriv == id.WGPub {
		t.Fatal("wireguard private and public key are equal")
	}
	// X25519 clamping, which WireGuard expects.
	if id.WGPriv[0]&7 != 0 {
		t.Errorf("private key low bits not cleared: %#x", id.WGPriv[0])
	}
	if id.WGPriv[31]&128 != 0 || id.WGPriv[31]&64 == 0 {
		t.Errorf("private key high bits not clamped: %#x", id.WGPriv[31])
	}
	if len(id.DevicePub) != ed25519.PublicKeySize {
		t.Errorf("device pub is %d bytes", len(id.DevicePub))
	}
}

func TestNewIdentityIsUnique(t *testing.T) {
	a, _ := New()
	b, _ := New()
	if a.WGPriv == b.WGPriv {
		t.Fatal("two identities share a wireguard key")
	}
	if string(a.DevicePub) == string(b.DevicePub) {
		t.Fatal("two identities share a device key")
	}
}

// Both sides must derive the same preshared key regardless of argument order,
// or the tunnel simply never comes up.
func TestPairPSKIsOrderIndependent(t *testing.T) {
	nk := mustNK(t)
	a, _ := New()
	b, _ := New()
	if PairPSK(nk, a.WGPub, b.WGPub) != PairPSK(nk, b.WGPub, a.WGPub) {
		t.Fatal("PairPSK depends on argument order")
	}
}

func TestPairPSKDiffersPerPairAndNetwork(t *testing.T) {
	nk := mustNK(t)
	a, _ := New()
	b, _ := New()
	c, _ := New()

	if PairPSK(nk, a.WGPub, b.WGPub) == PairPSK(nk, a.WGPub, c.WGPub) {
		t.Fatal("different pairs share a PSK")
	}
	// Rotating the network key must invalidate every tunnel at the WireGuard
	// layer, independent of the control plane being correct.
	if PairPSK(nk, a.WGPub, b.WGPub) == PairPSK(mustNK(t), a.WGPub, b.WGPub) {
		t.Fatal("PSK survived a network key rotation")
	}
}

func TestDerivedKeysAreDistinct(t *testing.T) {
	nk := mustNK(t)
	keys := map[string]string{
		"topic":   string(nk.TopicKey()),
		"payload": string(nk.PayloadKey()),
		"psk":     string(nk.PSKKey()),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if other, dup := seen[k]; dup {
			t.Fatalf("%s and %s derive the same key", name, other)
		}
		seen[k] = name
	}
}
