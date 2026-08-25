package mesh

import (
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/wg"
)

// A mesh with somewhere to write and an authority to check against.
func rememberingMesh(t *testing.T, dir string, auth *cred.Authority, nk identity.NetworkKey) *Mesh {
	t.Helper()
	st, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mesh{
		log:            slog.New(slog.DiscardHandler),
		st:             st,
		nk:             nk,
		roster:         NewRoster(nk, st.Identity.DevicePub),
		authority:      auth,
		revoked:        cred.NewList(),
		expiry:         map[string]int64{},
		expiredDropped: map[string]bool{},
		timing:         newTimings(time.Now()),
	}
	m.networkID = state.NetworkID(nk)
	return m
}

// An announce carrying a credential and endpoints, which is what a remembered
// peer is assembled from on the way in.
func announceWithCred(t *testing.T, id *identity.Identity, c []byte, eps []string, seq uint64) *control.Announce {
	t.Helper()
	a := newAnnounce(t, id, "peer", eps, seq)
	a.Credential = c
	return a
}

// Mint a credential for a device, and hand back its wire form.
func credentialFor(t *testing.T, admin *cred.Admin, auth *cred.Authority, id *identity.Identity, serial uint64, now time.Time, life time.Duration) []byte {
	t.Helper()
	c, err := admin.Issue(id.DevicePub, id.WGPub[:], "peer", serial, now, life)
	if err != nil {
		t.Fatal(err)
	}
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signWith(admin, d)
	raw, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The whole point: a peer written down on one run is back before anything has
// been heard on the bus, so the data plane has an endpoint to try while the
// delivery node is still dialling.
func TestARememberedPeerIsBackBeforeAnyAnnounce(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	// Seen half an hour ago and then the node was off. That is the case this
	// exists for: a memory younger than OfflineAfter needs no help, because the
	// ordinary rule already carries a peer that announced three minutes ago
	// whether or not we restarted since.
	seen := now.Add(-30 * time.Minute)

	first := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	first.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), seen)
	if err := first.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen); err != nil {
		t.Fatal(err)
	}
	first.saveRememberedPeers()

	// A new process, same directory, nothing on the bus.
	second := rememberingMesh(t, dir, auth, nk)
	second.loadRememberedPeers(now)

	peers := second.roster.Peers()
	if len(peers) != 1 {
		t.Fatalf("remembered %d peers, want 1", len(peers))
	}
	got := peers[0]
	if got.ID() != first.roster.Peers()[0].ID() {
		t.Error("remembered a different device")
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != "203.0.113.7:51820" {
		t.Errorf("endpoints came back as %v, which is what the data plane would try", got.Endpoints)
	}

	// Offline, honestly: it has not spoken, and the UI must not claim it has.
	if got.Online(now) {
		t.Error("a remembered peer reported itself online without having announced")
	}
	// But carried anyway, or the whole thing buys nothing.
	if !second.carry(got, wg.PeerStat{}, false, now) {
		t.Error("a remembered peer was not installed, so remembering it did nothing")
	}
}

// The window is the safety on the whole feature: past it, a peer that never
// proved itself is transmitting at a dead address, which is exactly what the
// ordinary rule refuses.
func TestARememberedPeerIsDroppedWhenTheWindowCloses(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	seen := now.Add(-30 * time.Minute)
	m := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	m.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), seen)
	_ = m.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen)
	m.saveRememberedPeers()

	next := rememberingMesh(t, dir, auth, nk)
	next.loadRememberedPeers(now)
	p := next.roster.Peers()[0]

	if !next.carry(p, wg.PeerStat{}, false, now) {
		t.Fatal("not carried at the start of the window")
	}
	// One second before, and one second after.
	just := now.Add(ProvisionalWindow - time.Second)
	if !next.carry(p, wg.PeerStat{}, false, just) {
		t.Error("dropped before the window closed")
	}
	after := now.Add(ProvisionalWindow + time.Second)
	if next.carry(p, wg.PeerStat{}, false, after) {
		t.Error("still transmitting at a remembered address after the window closed")
	}

	// And the window is a narrowing, not a widening: it must close before the
	// ordinary offline rule would have given up anyway.
	if ProvisionalWindow >= OfflineAfter {
		t.Errorf("ProvisionalWindow (%s) is not inside OfflineAfter (%s); the override "+
			"would extend how long a dead peer is carried rather than shorten it",
			ProvisionalWindow, OfflineAfter)
	}
}

// A remembered endpoint that works stops needing the window almost at once: the
// handshake makes the tunnel live and the ordinary rule takes over. Without
// this, every remembered peer would be torn down 90 seconds in, including the
// ones that had been carrying traffic the whole time.
func TestARememberedPeerThatConnectedSurvivesTheWindow(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	seen := now.Add(-30 * time.Minute)
	m := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	m.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), seen)
	_ = m.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen)
	m.saveRememberedPeers()

	next := rememberingMesh(t, dir, auth, nk)
	next.loadRememberedPeers(now)
	p := next.roster.Peers()[0]

	after := now.Add(ProvisionalWindow + time.Second)
	live := wg.PeerStat{LastHandshake: after.Add(-10 * time.Second)}
	if !next.carry(p, live, true, after) {
		t.Error("tore down a tunnel that was up, because the peer had been remembered")
	}
}

// Persistence must not become a way back in. The revocation list is loaded
// before the roster for exactly this.
func TestARevokedPeerIsNotRemembered(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	seen := now.Add(-30 * time.Minute)
	m := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	m.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), seen)
	_ = m.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen)
	m.saveRememberedPeers()

	// Revoked while this node was off.
	next := rememberingMesh(t, dir, auth, nk)
	rev, err := admin.Revoke(peer.DevicePub, 1, now.Add(24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	rawRev, err := rev.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !next.revoked.Add(rev, rawRev) {
		t.Fatal("the revocation was not accepted; the test proves nothing")
	}

	next.loadRememberedPeers(now)
	if len(next.roster.Peers()) != 0 {
		t.Error("a revoked device came back from disk")
	}
}

// Expiry is the gate SECURITY.md leans on because it cannot be suppressed, so
// it has to survive the peer being read from a file rather than a bus.
func TestAnExpiredCredentialIsNotRemembered(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	m := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, now, time.Hour)
	m.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), now)
	_ = m.checkMembership(announceWithCred(t, peer, raw, nil, 1), now)
	m.saveRememberedPeers()

	next := rememberingMesh(t, dir, auth, nk)
	next.loadRememberedPeers(now.Add(2 * time.Hour))
	if len(next.roster.Peers()) != 0 {
		t.Error("a device whose credential had run out came back from disk")
	}
}

// An announce is better evidence than a memory, and it is also later. If one
// arrives before the restore runs, the restore must not overwrite it with a
// stale endpoint and a stale timestamp.
func TestALiveAnnounceBeatsAMemory(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	now := time.Now()

	r := NewRoster(nk, self.DevicePub)
	r.Apply(newAnnounce(t, peer, "laptop", []string{"198.51.100.9:51820"}, 7), now)

	stale := PeerInfo{
		DevicePub: peer.DevicePub,
		WGPub:     peer.WGPub,
		Name:      "laptop",
		Endpoints: []string{"203.0.113.7:51820"},
		Seq:       1,
		LastSeen:  now.Add(-time.Hour),
	}
	if r.Restore(stale) {
		t.Error("a memory overwrote a peer that had just announced")
	}
	got := r.Peers()[0]
	if got.Endpoints[0] != "198.51.100.9:51820" || got.Seq != 7 {
		t.Errorf("the announced peer was clobbered: %v seq %d", got.Endpoints, got.Seq)
	}
}

// We are not our own peer, however we arrive.
func TestRestoreRefusesOurself(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	r := NewRoster(nk, self.DevicePub)

	if r.Restore(PeerInfo{DevicePub: self.DevicePub, WGPub: self.WGPub, LastSeen: time.Now()}) {
		t.Error("restored ourselves onto our own roster")
	}
	if len(r.Peers()) != 0 {
		t.Error("we are on our own roster")
	}
}

// A memory younger than OfflineAfter needs no exception at all: the ordinary
// rule already carries a peer that announced two minutes ago, and it does not
// care whether we restarted in between. Worth pinning, because it means the
// provisional window is not the only thing keeping a peer alive after a quick
// crash — and somebody reading only ProvisionalWindow would conclude otherwise.
func TestAFreshMemoryIsCarriedByTheOrdinaryRule(t *testing.T) {
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()

	// Seen ten seconds before a crash, which is the ordinary crash.
	seen := now.Add(-10 * time.Second)
	m := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	m.roster.Apply(announceWithCred(t, peer, raw, []string{"203.0.113.7:51820"}, 1), seen)
	_ = m.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen)
	m.saveRememberedPeers()

	next := rememberingMesh(t, dir, auth, nk)
	next.loadRememberedPeers(now)
	p := next.roster.Peers()[0]

	// Online, and honestly so: it really did announce ten seconds ago. The
	// timestamp is the peer's, not a claim about this process.
	if !p.Online(now) {
		t.Error("a peer seen ten seconds ago came back reading as offline")
	}
	if !next.carry(p, wg.PeerStat{}, false, now) {
		t.Error("not carried")
	}
	// And it is still carried past the provisional window, because the window
	// was never what was carrying it.
	if !next.carry(p, wg.PeerStat{}, false, now.Add(ProvisionalWindow+time.Second)) {
		t.Error("dropped at the provisional window a peer the ordinary rule still covers")
	}
}
