package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

func TestResolveFindsPeerByName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "laptop", nil, 1), time.Now())

	addr, ok := m.Resolve("laptop")
	if !ok {
		t.Fatal("did not resolve a peer that exists")
	}
	if addr != identity.OverlayAddr(nk, peer.DevicePub) {
		t.Errorf("resolved to %v", addr)
	}
}

// Names are self-asserted, so two entries can claim one — and the ordinary way
// that happens is one device that has changed identity, announcing under a new
// key while the old entry lives out its ForgetAfter. The live one is the one
// still announcing.
func TestResolvePicksTheFreshestClaimant(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	old, _ := identity.New()
	live, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	now := time.Now()
	m.roster.Apply(newAnnounce(t, old, "box", nil, 1), now.Add(-2*time.Hour))
	m.roster.Apply(newAnnounce(t, live, "box", nil, 1), now)

	addr, ok := m.Resolve("box")
	if !ok {
		t.Fatal("a name claimed twice resolved to nothing")
	}
	if addr != identity.OverlayAddr(nk, live.DevicePub) {
		t.Errorf("resolved to the stale entry: %v", addr)
	}

	// Order of arrival must not decide it — the same two entries applied the
	// other way round resolve the same.
	m2 := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m2.roster.Apply(newAnnounce(t, live, "box", nil, 1), now)
	m2.roster.Apply(newAnnounce(t, old, "box", nil, 1), now.Add(-2*time.Hour))
	if addr, _ := m2.Resolve("box"); addr != identity.OverlayAddr(nk, live.DevicePub) {
		t.Errorf("resolution depends on insertion order: %v", addr)
	}
}

// What resolves must match what the hosts file writes, or a name works in one
// and not the other.
func TestResolveMatchesHostsSanitising(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "My Laptop", nil, 1), time.Now())

	for _, q := range []string{"my-laptop", "MY-LAPTOP", "My_Laptop"} {
		if _, ok := m.Resolve(q); !ok {
			t.Errorf("did not resolve %q", q)
		}
	}
}

func TestResolveUnknownName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	if _, ok := m.Resolve("nosuch"); ok {
		t.Error("resolved a name nobody claims")
	}
}

// A service name is <service>.<device>, which is the same shape as the
// mesh-qualified <device>.<mesh> form. Lookup must handle both, and must not
// resolve a service by treating its leftmost label as a device.
func TestLookupResolvesServiceOnPeer(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub), cfg: state.Config{Name: "laptop"}}
	m.self = identity.OverlayAddr(nk, self.DevicePub)
	m.roster.Apply(newAnnounce(t, peer, "home-server", nil, 1), time.Now())
	server := identity.OverlayAddr(nk, peer.DevicePub)

	cases := []struct {
		name string
		want any // netip.Addr, or nil for "must not resolve"
	}{
		{"home-server", server},          // a device
		{"immich.home-server", server},   // a service on a device
		{"jellyfin.home-server", server}, // any service name; none are announced
		{"laptop", m.self},               // ourselves
		{"immich.laptop", m.self},        // a service on ourselves
		// The mesh-qualified form is NOT this function's job. A Mesh does not
		// know its own label, so it could not tell "home" from any other word
		// and resolved dev.<anything> — which is how a name qualified with one
		// mesh's label fell through to a device on another. Both production
		// callers wrap this in a resolver that does know the labels.
		{"home-server.home", nil},
		{"immich.nonesuch", nil}, // the device does not exist
		{"nonesuch", nil},        // nor does this
	}
	for _, c := range cases {
		addr, ok := m.Lookup(c.name)
		if c.want == nil {
			if ok {
				t.Errorf("Lookup(%q) resolved to %v, want no answer", c.name, addr)
			}
			continue
		}
		if !ok {
			t.Errorf("Lookup(%q) did not resolve", c.name)
			continue
		}
		if addr != c.want {
			t.Errorf("Lookup(%q) = %v, want %v", c.name, addr, c.want)
		}
	}
}

// The name a peer is told to use has to reach the mesh the thing is on. The
// short form is answered by the primary mesh alone, so on a multi-mesh node it
// names an address on another network — which is what `shrooms bound`, the
// desktop panel and the phone all showed before this existed.
func TestQualifiedDNSName(t *testing.T) {
	for _, tc := range []struct{ name, label, suffix, want string }{
		{"laptop", "", "mesh", "laptop.mesh"},
		{"laptop", "test", "mesh", "laptop.test.mesh"},
		{"laptop", "home", "", "laptop.home.mesh"},
		{"Living Room NAS", "shared", "mesh", "living-room-nas.shared.mesh"},
		{"laptop", "test", ".lan.", "laptop.test.lan"},
		{"", "test", "mesh", ""},
	} {
		if got := QualifiedDNSName(tc.name, tc.label, tc.suffix); got != tc.want {
			t.Errorf("QualifiedDNSName(%q,%q,%q) = %q, want %q",
				tc.name, tc.label, tc.suffix, got, tc.want)
		}
	}
}
