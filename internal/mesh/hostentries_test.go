package mesh

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
)

// The daemon rewrites the managed /etc/hosts block after every peer sync, and
// systemd-resolved answers from that file synthetically — ahead of the mesh
// resolver. So whatever is missing here is missing from almost every name
// lookup on the machine, however correct the resolver is.
//
// That is not hypothetical. The IPv4 aliases were added to `shrooms hosts` and
// not to this path, so the daemon kept overwriting the file with IPv6-only
// entries and `nslookup` kept returning an IPv6 address for a name that had a
// perfectly good A record behind it.
func TestHostEntriesCarryBothFamilies(t *testing.T) {
	self := netip.MustParseAddr("fd3b:ffe9:f81:81a7::1")
	peer := netip.MustParseAddr("fd3b:ffe9:f81:891a::2")

	selfPub, peerPub := pubOf(t, 1), pubOf(t, 2)
	block := v4.Block("net")
	table := v4.NewTableIn(block,
		v4.Entry{Overlay: self, DevicePub: selfPub},
		[]v4.Entry{{Overlay: peer, DevicePub: peerPub}},
	)

	m := &Mesh{
		cfg:    state.Config{Name: "laptop"},
		self:   self,
		v4:     table,
		roster: rosterWith(t, peerPub, "jimmy-crib", peer),
	}

	entries := m.hostEntries()
	if len(entries) != 2 {
		t.Fatalf("built %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.AddrV4 == "" {
			t.Errorf("%s has no IPv4 alias, so every A lookup for it will fail", e.Name)
			continue
		}
		a, err := netip.ParseAddr(e.AddrV4)
		if err != nil || !a.Is4() {
			t.Errorf("%s got %q, which is not an IPv4 address", e.Name, e.AddrV4)
			continue
		}
		if !block.Contains(a) {
			t.Errorf("%s got %s, outside this mesh's block %s", e.Name, a, block)
		}
	}

	// And each name keeps its own alias — the two must not collide, or an
	// IPv4 connection lands on the wrong machine.
	if entries[0].AddrV4 == entries[1].AddrV4 {
		t.Error("two devices share one alias")
	}
}

// A peer the alias table has never heard of must produce an empty field rather
// than something invented, so the hosts block simply omits its A record instead
// of publishing a wrong one.
func TestAPeerWithNoAliasGetsNoIPv4(t *testing.T) {
	self := netip.MustParseAddr("fd3b:ffe9:f81:81a7::1")
	stranger := netip.MustParseAddr("fd3b:ffe9:f81:cccc::9")

	m := &Mesh{
		cfg:    state.Config{Name: "laptop"},
		self:   self,
		v4:     nil, // no table at all, which is what a mesh without v4 looks like
		roster: rosterWith(t, pubOf(t, 2), "stranger", stranger),
	}
	for _, e := range m.hostEntries() {
		if e.AddrV4 != "" {
			t.Errorf("%s was given an alias %q from a table that does not exist", e.Name, e.AddrV4)
		}
	}
}

func pubOf(t *testing.T, n byte) ed25519.PublicKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = n
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
}

// rosterWith is a roster holding one peer, built through the real type so the
// test does not depend on a shape the production code does not use.
func rosterWith(t *testing.T, pub ed25519.PublicKey, name string, overlay netip.Addr) *Roster {
	t.Helper()
	r := &Roster{peers: map[string]PeerInfo{
		strings.ToLower(hex.EncodeToString(pub)): {
			DevicePub: pub, Name: name, Overlay: overlay,
		},
	}}
	return r
}
