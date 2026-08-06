package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/logos-vpn/internal/control"
	"github.com/vpavlin/logos-vpn/internal/identity"
)

func newAnnounce(t *testing.T, id *identity.Identity, name string, eps []string, seq uint64) *control.Announce {
	t.Helper()
	return &control.Announce{
		Kind:      control.KindAnnounce,
		DevicePub: id.DevicePub,
		WGPub:     id.WGPub[:],
		Name:      name,
		Endpoints: eps,
		Seq:       seq,
		Timestamp: time.Now().Unix(),
	}
}

func TestRosterAddsPeer(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()

	r := NewRoster(nk, self.DevicePub)
	p, changed := r.Apply(newAnnounce(t, peer, "office", []string{"203.0.113.1:51820"}, 1), time.Now())

	if !changed {
		t.Fatal("adding a new peer did not report a change")
	}
	if p.Name != "office" {
		t.Errorf("name = %q", p.Name)
	}
	if p.Overlay != identity.OverlayAddr(nk, peer.DevicePub) {
		t.Error("overlay address does not match the derived value")
	}
	if r.Len() != 1 {
		t.Errorf("roster has %d peers, want 1", r.Len())
	}
}

// We must never add ourselves: our own announce comes back off the shard, and a
// self-peer would make WireGuard try to tunnel to itself.
func TestRosterIgnoresSelf(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()

	r := NewRoster(nk, self.DevicePub)
	_, changed := r.Apply(newAnnounce(t, self, "me", nil, 1), time.Now())

	if changed {
		t.Error("applying our own announce reported a change")
	}
	if r.Len() != 0 {
		t.Errorf("roster added self: %d peers", r.Len())
	}
}

// SetPeers replaces the whole peer set, so an unchanged heartbeat must not
// report a change — otherwise every announce interval would churn tunnels.
func TestRosterHeartbeatIsNotAChange(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	eps := []string{"203.0.113.1:51820"}
	if _, changed := r.Apply(newAnnounce(t, peer, "office", eps, 1), time.Now()); !changed {
		t.Fatal("first announce should be a change")
	}
	if _, changed := r.Apply(newAnnounce(t, peer, "office", eps, 2), time.Now()); changed {
		t.Error("an identical heartbeat reported a change")
	}
}

func TestRosterDetectsEndpointChange(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	r.Apply(newAnnounce(t, peer, "office", []string{"203.0.113.1:51820"}, 1), time.Now())
	_, changed := r.Apply(newAnnounce(t, peer, "office", []string{"198.51.100.7:51820"}, 2), time.Now())
	if !changed {
		t.Error("a roamed endpoint did not report a change")
	}
}

func TestRosterDetectsRename(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	eps := []string{"203.0.113.1:51820"}
	r.Apply(newAnnounce(t, peer, "office", eps, 1), time.Now())
	if _, changed := r.Apply(newAnnounce(t, peer, "office-box", eps, 2), time.Now()); !changed {
		t.Error("a rename did not report a change")
	}
}

func TestPeerOnlineWindow(t *testing.T) {
	now := time.Now()
	p := PeerInfo{LastSeen: now}

	if !p.Online(now) {
		t.Error("a peer seen now is offline")
	}
	if !p.Online(now.Add(OfflineAfter - time.Second)) {
		t.Error("a peer within the window is offline")
	}
	if p.Online(now.Add(OfflineAfter + time.Second)) {
		t.Error("a peer past the window is still online")
	}
}

func TestRosterSortedByName(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	for _, name := range []string{"zulu", "alpha", "mike"} {
		peer, _ := identity.New()
		r.Apply(newAnnounce(t, peer, name, nil, 1), time.Now())
	}

	got := r.Peers()
	want := []string{"alpha", "mike", "zulu"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("peer %d = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestRosterForget(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	r.Apply(newAnnounce(t, peer, "gone", nil, 1), time.Now())
	r.Forget(peer.DevicePub)
	if r.Len() != 0 {
		t.Fatalf("Forget left %d peers", r.Len())
	}
}
