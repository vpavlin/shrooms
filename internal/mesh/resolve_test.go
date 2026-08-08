package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
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
