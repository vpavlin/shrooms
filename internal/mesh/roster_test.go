package mesh

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
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

// A peer that never comes back must eventually be forgotten, not merely
// remembered as offline.
//
// Every per-peer map was insert-only, and a peer is anyone holding the network
// key — a bearer credential — so one member announcing endless device keys grew
// every node's maps until restart.
func TestRosterForgetsLongGonePeers(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	gone, _ := identity.New()
	here, _ := identity.New()

	r := NewRoster(nk, self.DevicePub)
	start := time.Now()
	r.Apply(newAnnounce(t, gone, "gone", nil, 1), start)
	r.Apply(newAnnounce(t, here, "here", nil, 1), start)

	// Offline is not forgotten: a machine switched off overnight must come back
	// as itself, with its identity and history intact.
	if ids := r.Prune(start.Add(OfflineAfter * 2)); len(ids) != 0 {
		t.Errorf("pruned %d peers merely offline", len(ids))
	}
	if r.Len() != 2 {
		t.Fatalf("roster has %d peers", r.Len())
	}

	// One of them keeps announcing; only the silent one goes.
	r.Apply(newAnnounce(t, here, "here", nil, 2), start.Add(ForgetAfter))

	ids := r.Prune(start.Add(ForgetAfter + time.Minute))
	if len(ids) != 1 || ids[0] != hex.EncodeToString(gone.DevicePub) {
		t.Fatalf("pruned %v, want just the silent peer", ids)
	}
	if r.Len() != 1 {
		t.Errorf("roster has %d peers, want 1", r.Len())
	}
}

// Forgetting a peer also forgets its replay counter, which is only safe because
// ForgetAfter is beyond the clock-skew window: an announce old enough to be
// replayed is already rejected on its timestamp.
func TestForgetAfterOutlastsClockSkew(t *testing.T) {
	if ForgetAfter <= control.MaxClockSkew {
		t.Errorf("ForgetAfter (%s) must exceed MaxClockSkew (%s), or forgetting a peer reopens replay",
			ForgetAfter, control.MaxClockSkew)
	}
}

// A device that changes identity leaves its old entry behind for hours. Both
// are the same machine, the old one is unreachable, and on a device that has
// been re-added a few times the roster is mostly ghosts.
func TestCurrentHidesASupersededEntry(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	old, _ := identity.New()
	live, _ := identity.New()
	other, _ := identity.New()

	r := NewRoster(nk, self.DevicePub)
	now := time.Now()
	r.Apply(newAnnounce(t, old, "k11", nil, 1), now.Add(-2*time.Hour))
	r.Apply(newAnnounce(t, live, "k11", nil, 1), now)
	r.Apply(newAnnounce(t, other, "vps", nil, 1), now.Add(-2*time.Hour))

	got := r.Current(now)
	if len(got) != 2 {
		t.Fatalf("roster shows %d peers, wanted 2", len(got))
	}
	for _, p := range got {
		if p.ID() == hex.EncodeToString(old.DevicePub) {
			t.Error("the superseded entry is still shown")
		}
	}

	// An offline peer nothing has superseded stays: that one is a machine that
	// is switched off, which is worth seeing.
	var sawVPS bool
	for _, p := range got {
		if p.Name == "vps" {
			sawVPS = true
		}
	}
	if !sawVPS {
		t.Error("hid an offline peer that nothing replaced")
	}
}

// Two live machines that genuinely share a name are both shown. Hiding one
// there would be the lie the old rule was worried about.
func TestCurrentKeepsTwoLiveClaimants(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	a, _ := identity.New()
	b, _ := identity.New()

	r := NewRoster(nk, self.DevicePub)
	now := time.Now()
	r.Apply(newAnnounce(t, a, "box", nil, 1), now.Add(-time.Second))
	r.Apply(newAnnounce(t, b, "box", nil, 1), now)

	if got := r.Current(now); len(got) != 2 {
		t.Errorf("roster shows %d peers, wanted both live claimants", len(got))
	}
}
