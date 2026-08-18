package mesh

import (
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

// confirmPath drives a full ping/pong exchange so the prober records addr as a
// working path to peer. Going through the real code rather than reaching into
// the prober's internals keeps the test honest about what "confirmed" means.
func confirmPath(t *testing.T, pr *disco.Prober, key disco.Key, sent *[][]byte, peer *identity.Identity, id string, addr netip.AddrPort, now time.Time) {
	t.Helper()

	*sent = nil
	pr.Probe(id, []netip.AddrPort{addr}, now)
	if len(*sent) != 1 {
		t.Fatalf("probe sent %d packets, want 1", len(*sent))
	}

	ping, err := disco.Decode(key, (*sent)[0])
	if err != nil {
		t.Fatalf("decode ping: %v", err)
	}
	raw, err := disco.EncodePong(key, peer.DevicePriv, ping.TxID, addr)
	if err != nil {
		t.Fatalf("encode pong: %v", err)
	}
	pong, err := disco.Decode(key, raw)
	if err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	if _, ok := pr.HandlePong(pong, addr, now); !ok {
		t.Fatal("pong was not credited")
	}
}

// relayFixture builds a mesh with one relay peer and one ordinary peer, both
// announcing and both on a confirmed path.
type relayFixture struct {
	m         *Mesh
	relayID   string
	relayAddr netip.AddrPort
	now       time.Time
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()

	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	key := disco.DeriveKey(nk)
	now := time.Now()

	var sent [][]byte
	pr := disco.NewProber(key, self.DevicePriv, func(pkt []byte, _ netip.AddrPort) error {
		sent = append(sent, pkt)
		return nil
	})

	m := &Mesh{
		nk:       nk,
		roster:   NewRoster(nk, self.DevicePub),
		prober:   pr,
		discoKey: key,
		relayKey: relay.DeriveKey(nk),
	}

	rl, _ := identity.New()
	a := newAnnounce(t, rl, "vps", []string{"203.0.113.9:51820"}, 1)
	a.Relay = true
	rp, _ := m.roster.Apply(a, now)

	plain, _ := identity.New()
	m.roster.Apply(newAnnounce(t, plain, "laptop", []string{"198.51.100.4:51820"}, 1), now)

	addr := netip.MustParseAddrPort("203.0.113.9:51820")
	confirmPath(t, pr, key, &sent, rl, rp.ID(), addr, now)

	return &relayFixture{m: m, relayID: rp.ID(), relayAddr: addr, now: now}
}

func TestSelectRelayPrefersDiscoveredRelay(t *testing.T) {
	f := newRelayFixture(t)

	got := f.m.selectRelay(f.now)
	if !got.ok {
		t.Fatal("no relay selected, want the announced one")
	}
	if got.addr != f.relayAddr {
		t.Errorf("relay addr = %v, want %v", got.addr, f.relayAddr)
	}
	if got.id != f.relayID {
		t.Errorf("relay id = %q, want %q", got.id, f.relayID)
	}
}

// A peer that never answered a probe must not be used. Unlike a direct
// endpoint, WireGuard cannot relearn a relay path from an inbound packet, so an
// unverified relay address blackholes silently and forever.
func TestSelectRelayIgnoresUnprobedRelay(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	now := time.Now()

	m := &Mesh{
		nk:     nk,
		roster: NewRoster(nk, self.DevicePub),
		prober: disco.NewProber(disco.DeriveKey(nk), self.DevicePriv, func([]byte, netip.AddrPort) error { return nil }),
	}

	rl, _ := identity.New()
	a := newAnnounce(t, rl, "vps", []string{"203.0.113.9:51820"}, 1)
	a.Relay = true
	m.roster.Apply(a, now)

	if got := m.selectRelay(now); got.ok {
		t.Errorf("selected unprobed relay %v", got.addr)
	}
}

// Selection must be a pure function of shared state, because relaying only
// works if both ends pick the SAME relay: the relay forwards by destination key
// and only knows peers that registered with it. Lowest device ID is a value
// both ends compute identically; lowest RTT is not.
func TestSelectRelayIsDeterministicAcrossNodes(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	key := disco.DeriveKey(nk)
	now := time.Now()

	// Two relays, announced to two different nodes in opposite order and with
	// opposite RTTs.
	r1, _ := identity.New()
	r2, _ := identity.New()
	addr1 := netip.MustParseAddrPort("203.0.113.1:51820")
	addr2 := netip.MustParseAddrPort("203.0.113.2:51820")

	build := func(first, second *identity.Identity, firstAddr, secondAddr netip.AddrPort, delay time.Duration) relayChoice {
		self, _ := identity.New()
		var sent [][]byte
		pr := disco.NewProber(key, self.DevicePriv, func(pkt []byte, _ netip.AddrPort) error {
			sent = append(sent, pkt)
			return nil
		})
		m := &Mesh{nk: nk, roster: NewRoster(nk, self.DevicePub), prober: pr}

		for _, e := range []struct {
			id    *identity.Identity
			addr  netip.AddrPort
			extra time.Duration
		}{{first, firstAddr, 0}, {second, secondAddr, delay}} {
			a := newAnnounce(t, e.id, "relay", []string{e.addr.String()}, 1)
			a.Relay = true
			p, _ := m.roster.Apply(a, now)
			// Probe at now, pong at now+extra: a different measured RTT.
			sent = nil
			pr.Probe(p.ID(), []netip.AddrPort{e.addr}, now)
			ping, _ := disco.Decode(key, sent[0])
			raw, _ := disco.EncodePong(key, e.id.DevicePriv, ping.TxID, e.addr)
			pong, _ := disco.Decode(key, raw)
			pr.HandlePong(pong, e.addr, now.Add(e.extra))
		}
		return m.selectRelay(now.Add(delay))
	}

	a := build(r1, r2, addr1, addr2, 50*time.Millisecond)
	b := build(r2, r1, addr2, addr1, 50*time.Millisecond)

	if !a.ok || !b.ok {
		t.Fatal("a node failed to select any relay")
	}
	if a.addr != b.addr {
		t.Errorf("nodes disagree on the relay: %v vs %v — traffic through them cannot meet", a.addr, b.addr)
	}
}

// A relay is publicly reachable by definition, so it has no use for one. More
// importantly it must never select itself, which would loop.
func TestSelectRelaySkippedWhenActingAsRelay(t *testing.T) {
	f := newRelayFixture(t)
	f.m.relaySrv = relay.NewServer(f.m.relayKey, nil)

	if got := f.m.selectRelay(f.now); got.ok {
		t.Errorf("a relay selected an upstream relay %v", got.addr)
	}
}

// An explicit relay_addr still wins, so a mesh whose relay has not announced
// yet can be brought up by hand.
func TestSelectRelayPinOverridesDiscovery(t *testing.T) {
	f := newRelayFixture(t)
	pin := netip.MustParseAddrPort("192.0.2.7:51820")
	f.m.relayPin = pin

	got := f.m.selectRelay(f.now)
	if !got.ok || got.addr != pin {
		t.Errorf("relay addr = %v, want the pinned %v", got.addr, pin)
	}
	if got.id != "" {
		t.Errorf("pinned relay reported a device id %q", got.id)
	}
}

// A relay that has stopped announcing must fall out of the candidate set even
// though its probed path is still cached.
func TestSelectRelayDropsOfflineRelay(t *testing.T) {
	f := newRelayFixture(t)

	if got := f.m.selectRelay(f.now.Add(OfflineAfter + time.Second)); got.ok {
		t.Errorf("selected relay %v that has been silent for %s", got.addr, OfflineAfter)
	}
}

// The flag has to survive the wire, or discovery works only in tests.
func TestRelayFlagSurvivesSealOpen(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	now := time.Now()

	a := newAnnounce(t, id, "vps", []string{"203.0.113.9:51820"}, 1)
	a.Relay = true

	sealed, err := control.Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := control.OpenAnnounce(nk, 1, sealed, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !got.Relay {
		t.Error("relay flag did not survive the round trip")
	}
}

// The relay registration must be renewed well inside the relay's TTL, and far
// less often than the probe tick it used to ride on.
//
// It went out every ProbeInterval (3s) against a RegistrationTTL of 2 minutes —
// about 40x more often than the mapping needed, forever, even when every peer
// had a direct path and the relay carried none of our traffic.
func TestRelayRefreshIsInsideTheTTL(t *testing.T) {
	if RelayRefresh >= relay.RegistrationTTL {
		t.Fatalf("RelayRefresh (%s) must be less than RegistrationTTL (%s), or the mapping expires",
			RelayRefresh, relay.RegistrationTTL)
	}
	// Half the TTL survives a lost registration; much closer to the TTL does not.
	if RelayRefresh > relay.RegistrationTTL/2 {
		t.Errorf("RelayRefresh (%s) leaves no room for a lost packet before the TTL (%s)",
			RelayRefresh, relay.RegistrationTTL)
	}
	if RelayRefresh <= disco.ProbeInterval {
		t.Errorf("RelayRefresh (%s) is no better than the probe tick (%s) it replaced",
			RelayRefresh, disco.ProbeInterval)
	}
}
