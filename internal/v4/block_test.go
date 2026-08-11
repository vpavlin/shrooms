package v4

import (
	"crypto/ed25519"
	"testing"
)

// Every mesh needs its own slice of the range, because the range is routed at
// an interface and there is one interface per mesh. Sharing it meant the kernel
// kept a single route, so a packet for a peer on the second mesh entered the
// first mesh's translator and was dropped — a browser could not reach the
// service while curl over IPv6 could.
func TestBlocksDoNotOverlap(t *testing.T) {
	a := Block("ecoj2csc6s52g")
	b := Block("k3yih52s5gxhi")

	if a == b {
		t.Skip("these two ids happen to share a block; 16 exist and collisions are expected")
	}
	if a.Overlaps(b) {
		t.Errorf("%s overlaps %s", a, b)
	}
	if !Prefix.Contains(a.Addr()) || !Prefix.Contains(b.Addr()) {
		t.Errorf("a block escaped the range: %s %s", a, b)
	}
	if a.Bits() != 32-DeviceBits {
		t.Errorf("block is /%d, want /%d", a.Bits(), 32-DeviceBits)
	}
	// Stable, or a restart would re-address every peer.
	if Block("ecoj2csc6s52g") != a {
		t.Error("block derivation is not stable")
	}
}

// An alias must land inside its own mesh's block, or it would be routed to
// another mesh's interface.
func TestAliasesStayInTheirBlock(t *testing.T) {
	block := Block("ecoj2csc6s52g")
	other := Block("some-other-mesh-entirely")

	for i := 0; i < 50; i++ {
		pub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		a := Alias(block, pub, 0)
		if !block.Contains(a) {
			t.Fatalf("%s is outside its block %s", a, block)
		}
		if other != block && other.Contains(a) {
			t.Fatalf("%s landed in another mesh's block", a)
		}
		if last := a.As4()[3]; last == 0 || last == 255 {
			t.Errorf("%s ends in .%d, which confuses enough software to avoid", a, last)
		}
	}
}
