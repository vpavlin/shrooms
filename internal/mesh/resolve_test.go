package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/state"
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

// Names are self-asserted, so two peers can claim one. Picking either would
// send traffic to a machine the user did not mean; the qualified form and the
// address still work.
func TestResolveRefusesAmbiguousName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	a, _ := identity.New()
	b, _ := identity.New()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	now := time.Now()
	m.roster.Apply(newAnnounce(t, a, "box", nil, 1), now)
	m.roster.Apply(newAnnounce(t, b, "box", nil, 1), now)

	if _, ok := m.Resolve("box"); ok {
		t.Error("resolved a name two peers claim")
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
		{"home-server.home", server},     // the mesh-qualified form still works
		{"immich.nonesuch", nil},         // the device does not exist
		{"nonesuch", nil},                // nor does this
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
