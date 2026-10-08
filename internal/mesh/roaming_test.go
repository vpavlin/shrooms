package mesh

import (
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/wg"
)

// Roaming (2026-10-08): a phone that moved onto the office Wi-Fi reached the
// VPS and none of the office's machines until the app was force-stopped. Its
// delivery node — kept across the app's in-process rebuilds — had stopped
// bringing announces, and every way back to a LAN peer went through an
// announce: a peer reading offline was never probed, so it had no direct path
// and was routed through a relay it was not registered with. These are the
// paths that must not need the bus.

// probingMesh is a mesh with a prober whose packets are captured.
func probingMesh(t *testing.T, m *Mesh) (disco.Key, *[][]byte) {
	t.Helper()
	key := disco.DeriveKey(m.nk)
	var sent [][]byte
	m.prober = disco.NewProber(key, m.st.Identity.DevicePriv, func(pkt []byte, _ netip.AddrPort) error {
		sent = append(sent, pkt)
		return nil
	})
	m.discoKey = key
	return key, &sent
}

// rememberLANPeer writes down a peer last heard of long ago on the office LAN,
// and starts a new mesh from that memory — the app rebuilding its session.
func rememberLANPeer(t *testing.T) (*Mesh, *identity.Identity, time.Time) {
	t.Helper()
	dir := t.TempDir()
	nk, _ := identity.NewNetworkKey()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	peer, _ := identity.New()
	now := time.Now()
	seen := now.Add(-30 * time.Minute)

	first := rememberingMesh(t, dir, auth, nk)
	raw := credentialFor(t, admin, auth, peer, 1, seen, 24*time.Hour)
	first.roster.Apply(announceWithCred(t, peer, raw, []string{"192.168.10.59:51820"}, 1), seen)
	if err := first.checkMembership(announceWithCred(t, peer, raw, nil, 1), seen); err != nil {
		t.Fatal(err)
	}
	first.saveRememberedPeers()

	m := rememberingMesh(t, dir, auth, nk)
	m.loadRememberedPeers(now)
	return m, peer, now
}

func TestARememberedPeerIsProbedWithoutItsAnnounces(t *testing.T) {
	m, _, now := rememberLANPeer(t)
	_, sent := probingMesh(t, m)
	p := m.roster.Peers()[0]
	if p.Online(now) {
		t.Fatal("the test needs the peer offline by its announces")
	}
	m.probeAll(now)
	if len(*sent) == 0 {
		t.Fatal("a remembered peer was not probed: it can only come back through an announce, or a relay it may not use")
	}
}

func TestAPeerThatAnswersIsKeptWithoutItsAnnounces(t *testing.T) {
	m, peer, now := rememberLANPeer(t)
	key, sent := probingMesh(t, m)
	p := m.roster.Peers()[0]
	addr := netip.MustParseAddrPort("192.168.10.59:51820")
	confirmPath(t, m.prober, key, sent, peer, p.ID(), addr, now)

	// Past the window a memory is carried for, its announces still not
	// arriving — the bus is down, the LAN is not.
	later := now.Add(ProvisionalWindow + disco.PathRefresh + time.Second)
	confirmPath(t, m.prober, key, sent, peer, p.ID(), addr, later.Add(-time.Second))
	if !m.carry(p, wg.PeerStat{}, false, later) {
		t.Error("a peer that answers its probes was dropped from the data plane for want of announces")
	}
	*sent = nil
	m.probeAll(later.Add(disco.PathRefresh))
	if len(*sent) == 0 {
		t.Error("a peer that answers its probes is no longer probed once its announces stop")
	}
}

func TestEveryPeerIsProbedAfterTheNetworkChanges(t *testing.T) {
	m, _, now := rememberLANPeer(t)
	_, sent := probingMesh(t, m)
	m.log = slog.New(slog.DiscardHandler)
	// Long after the start: the memory's window has closed, the peer reads
	// offline, nothing answers.
	later := now.Add(10 * time.Minute)
	m.probeAll(later)
	if len(*sent) != 0 {
		t.Fatal("the test needs a peer that is not probed when nothing has changed")
	}
	m.networkChangedAt(later)
	m.probeAll(later.Add(time.Second))
	if len(*sent) == 0 {
		t.Error("after a move, a peer reading offline is not probed: on the new network it may be one hop away")
	}
	*sent = nil
	m.probeAll(later.Add(ProvisionalWindow + disco.PathRefresh + time.Second))
	if len(*sent) != 0 {
		t.Error("a peer that never answered is probed for ever after a move")
	}
}

// The office machine's side: the phone's announces are not arriving there
// either, so the phone reads offline — but it is probing, and is probed back.
func TestAPeerThatProbesUsIsProbedBack(t *testing.T) {
	m, peer, now := rememberLANPeer(t)
	key, sent := probingMesh(t, m)
	// Running for a while: the memory's window closed long ago.
	m.timing = newTimings(now.Add(-10 * time.Minute))
	m.probeAll(time.Now())
	if len(*sent) != 0 {
		t.Fatal("the test needs a peer that is not probed when nothing has been heard")
	}
	// Its ping, as it arrives.
	other := disco.NewProber(key, peer.DevicePriv, func(pkt []byte, _ netip.AddrPort) error {
		msg, err := disco.Decode(key, pkt)
		if err != nil {
			t.Fatal(err)
		}
		m.prober.HandlePing(msg, netip.MustParseAddrPort("192.168.10.173:51821"))
		return nil
	})
	other.Probe(m.roster.Peers()[0].ID(), []netip.AddrPort{netip.MustParseAddrPort("192.168.10.59:51820")}, time.Now())
	*sent = nil // our pong
	m.probeAll(time.Now())
	if len(*sent) == 0 {
		t.Error("a peer that probed us is not probed back while its announces are not arriving")
	}
}
