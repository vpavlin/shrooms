package identity

import (
	"bytes"
	"testing"
)

// Two meshes must not touch, and the guarantee has to hold at every layer that
// derives from the network key — not just the obvious one.
//
// The question this answers is "can my two meshes collide?", and it is asked of
// a design where meshes deliberately share one public shard. They are separated
// by derivation alone: different rendezvous topics so neither sees the other's
// announces, different payload keys so a captured one cannot be read, different
// address prefixes so a peer of one is not addressable in the other, and
// different pairwise PSKs so even an identical WireGuard key pair produces a
// different session.
//
// A regression in any single one is a silent cross-mesh leak, which is exactly
// the kind of thing that is never noticed until it matters.
func TestMeshesDeriveNothingInCommon(t *testing.T) {
	a, err := NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what string
		a, b []byte
	}{
		{"topic key", a.TopicKey(), b.TopicKey()},
		{"payload key", a.PayloadKey(), b.PayloadKey()},
		{"PSK key", a.PSKKey(), b.PSKKey()},
	} {
		if bytes.Equal(c.a, c.b) {
			t.Errorf("two meshes share a %s", c.what)
		}
	}

	if a.Prefix() == b.Prefix() {
		t.Error("two meshes share an address prefix; peers of one would be addressable in the other")
	}

	// The same device keys in both meshes must still yield different session
	// keys. Otherwise a device that belongs to two meshes could carry traffic
	// between them, which is the collision people actually fear.
	x, y := WGKey{1, 2, 3}, WGKey{4, 5, 6}
	if PairPSK(a, x, y) == PairPSK(b, x, y) {
		t.Error("the same device pair derives one PSK in two meshes")
	}
}

// Each label must produce independent material from one key. Deriving two
// things from the same secret with the same label is an easy mistake and gives
// the payload key to anyone who learns the topic key.
func TestLabelsSeparateDerivedMaterial(t *testing.T) {
	nk, err := NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{
		"topic":   string(nk.TopicKey()),
		"payload": string(nk.PayloadKey()),
		"psk":     string(nk.PSKKey()),
	}
	for n1, v1 := range seen {
		for n2, v2 := range seen {
			if n1 < n2 && v1 == v2 {
				t.Errorf("%s and %s derive the same bytes", n1, n2)
			}
		}
	}
}

// A prefix is fd + 40 bits of hash, so two meshes collide with probability
// around 2^-20 at a million meshes — irrelevant for personal use and worth
// stating, since it is the one part of the isolation that is probabilistic
// rather than absolute.
func TestPrefixIsInsideTheULARange(t *testing.T) {
	nk, err := NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	p := nk.Prefix()
	if p.Bits() != 48 {
		t.Errorf("prefix is /%d, want /48", p.Bits())
	}
	if a := p.Addr().As16(); a[0] != 0xfd {
		t.Errorf("prefix starts %#x, want 0xfd (a ULA, not routable on the internet)", a[0])
	}
}
