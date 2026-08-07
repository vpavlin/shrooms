package disco

import (
	"crypto/ed25519"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// fakeNet couples two probers so packets sent by one arrive at the other,
// optionally rewriting the source address to simulate a NAT.
type fakeNet struct {
	mu sync.Mutex
	// rewrite maps a sender's identity to the source address its packets
	// appear to come from — i.e. what a NAT would do.
	rewrite map[string]netip.AddrPort
	deliver map[netip.AddrPort]func(pkt []byte, from netip.AddrPort)
	drops   map[netip.AddrPort]bool
}

func newFakeNet() *fakeNet {
	return &fakeNet{
		rewrite: map[string]netip.AddrPort{},
		deliver: map[netip.AddrPort]func([]byte, netip.AddrPort){},
		drops:   map[netip.AddrPort]bool{},
	}
}

func (f *fakeNet) sendFrom(src netip.AddrPort) func([]byte, netip.AddrPort) error {
	return func(pkt []byte, to netip.AddrPort) error {
		f.mu.Lock()
		if f.drops[to] {
			f.mu.Unlock()
			return nil // black hole, like an unroutable candidate
		}

		// Outbound: a NAT rewrites our source to its external address.
		observed := src
		if rw, ok := f.rewrite[src.String()]; ok {
			observed = rw
		}

		// Inbound: a packet addressed to a NAT's external address is
		// translated back to the internal host. Without this the reply to a
		// NATed peer is delivered nowhere, which is a property of the fake,
		// not of NAT.
		dst := to
		for internal, external := range f.rewrite {
			if external == to {
				if ap, err := netip.ParseAddrPort(internal); err == nil {
					dst = ap
				}
				break
			}
		}

		h := f.deliver[dst]
		f.mu.Unlock()

		if h != nil {
			cp := append([]byte(nil), pkt...)
			h(cp, observed)
		}
		return nil
	}
}

func newTestProber(t *testing.T, key Key, f *fakeNet, at netip.AddrPort) *Prober {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	p := NewProber(key, pub, f.sendFrom(at))

	f.mu.Lock()
	f.deliver[at] = func(pkt []byte, from netip.AddrPort) {
		m, err := Decode(key, pkt)
		if err != nil {
			return
		}
		switch m.Type {
		case TypePing:
			p.HandlePing(m, from)
		case TypePong:
			p.HandlePong(m, from, time.Now())
		}
	}
	f.mu.Unlock()
	return p
}

func testKey(t *testing.T) Key {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("network key: %v", err)
	}
	return DeriveKey(nk)
}

func TestProbeFindsWorkingPath(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()

	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	bAddr := netip.MustParseAddrPort("10.0.0.2:51820")

	a := newTestProber(t, key, f, aAddr)
	newTestProber(t, key, f, bAddr)

	now := time.Now()
	a.Probe("peer-b", []netip.AddrPort{bAddr}, now)

	if _, ok := a.Best("peer-b", time.Now()); !ok {
		t.Fatal("no working path after probing a reachable candidate")
	}
}

// A candidate that black-holes must never be selected. This is the difference
// between "the peer announced this address" and "packets reach the peer here".
func TestDeadCandidateNotSelected(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()

	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	bAddr := netip.MustParseAddrPort("10.0.0.2:51820")
	dead := netip.MustParseAddrPort("192.168.99.99:51820")

	f.drops[dead] = true

	a := newTestProber(t, key, f, aAddr)
	newTestProber(t, key, f, bAddr)

	a.Probe("peer-b", []netip.AddrPort{dead, bAddr}, time.Now())

	best, ok := a.Best("peer-b", time.Now())
	if !ok {
		t.Fatal("no working path")
	}
	if best.Addr == dead {
		t.Fatalf("selected the black-holed candidate %s", dead)
	}
}

func TestNoPathBeforeProbing(t *testing.T) {
	a := NewProber(testKey(t), make([]byte, 32), func([]byte, netip.AddrPort) error { return nil })
	if _, ok := a.Best("peer-b", time.Now()); ok {
		t.Fatal("reported a working path with no probe sent")
	}
}

// Reflexive discovery: the peer tells us where it saw us, which is how a NATed
// node learns its public address without a STUN server.
func TestPongCarriesOurReflexiveAddress(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()

	aAddr := netip.MustParseAddrPort("10.91.0.100:51820")   // A's LAN address
	aPublic := netip.MustParseAddrPort("203.0.113.4:40001") // what a NAT rewrites it to
	bAddr := netip.MustParseAddrPort("10.90.0.10:51820")

	f.rewrite[aAddr.String()] = aPublic

	a := newTestProber(t, key, f, aAddr)
	newTestProber(t, key, f, bAddr)

	a.Probe("peer-b", []netip.AddrPort{bAddr}, time.Now())

	refl := a.Reflexive(time.Now())
	if len(refl) != 1 || refl[0] != aPublic {
		t.Fatalf("reflexive = %v, want [%s]", refl, aPublic)
	}
}

// A stale path must not be reported as usable.
func TestStalePathExpires(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()
	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	bAddr := netip.MustParseAddrPort("10.0.0.2:51820")

	a := newTestProber(t, key, f, aAddr)
	newTestProber(t, key, f, bAddr)
	a.Probe("peer-b", []netip.AddrPort{bAddr}, time.Now())

	if _, ok := a.Best("peer-b", time.Now()); !ok {
		t.Fatal("expected a fresh path")
	}
	if _, ok := a.Best("peer-b", time.Now().Add(PathFresh+time.Second)); ok {
		t.Fatal("a stale path was still reported as usable")
	}
}

func TestUnsolicitedPongIgnored(t *testing.T) {
	key := testKey(t)
	p := NewProber(key, make([]byte, 32), func([]byte, netip.AddrPort) error { return nil })

	tx, _ := NewTxID()
	pkt, _ := EncodePong(key, make([]byte, 32), tx, netip.MustParseAddrPort("1.2.3.4:1"))
	m, err := Decode(key, pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if _, ok := p.HandlePong(m, netip.MustParseAddrPort("5.6.7.8:9"), time.Now()); ok {
		t.Fatal("accepted a pong for a probe we never sent")
	}
	if len(p.Reflexive(time.Now())) != 0 {
		t.Fatal("an unsolicited pong contributed a reflexive address")
	}
}

func TestIPv6Preferred(t *testing.T) {
	now := time.Now()
	v4 := Path{Addr: netip.MustParseAddrPort("203.0.113.4:51820"), RTT: time.Millisecond, LastPong: now}
	v6 := Path{Addr: netip.MustParseAddrPort("[2001:db8::1]:51820"), RTT: 50 * time.Millisecond, LastPong: now}

	// v6 wins despite much worse RTT: with global v6 on both ends there is no
	// NAT at all, only filtering.
	if !better(v6, v4) {
		t.Error("IPv4 preferred over IPv6")
	}
	if better(v4, v6) {
		t.Error("ranking is not antisymmetric")
	}
}

func TestLowerRTTPreferredWithinFamily(t *testing.T) {
	now := time.Now()
	slow := Path{Addr: netip.MustParseAddrPort("203.0.113.4:1"), RTT: 100 * time.Millisecond, LastPong: now}
	fast := Path{Addr: netip.MustParseAddrPort("203.0.113.5:1"), RTT: 5 * time.Millisecond, LastPong: now}
	if !better(fast, slow) {
		t.Error("slower path preferred")
	}
}

func TestProbeExpiry(t *testing.T) {
	key := testKey(t)
	sent := 0
	p := NewProber(key, make([]byte, 32), func([]byte, netip.AddrPort) error { sent++; return nil })

	start := time.Now()
	p.Probe("peer-b", []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:1")}, start)
	if sent != 1 {
		t.Fatalf("sent %d probes, want 1", sent)
	}

	// A later probe round expires the earlier unanswered one.
	p.Probe("peer-b", []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:1")}, start.Add(ProbeTimeout+time.Second))

	p.mu.Lock()
	pending := len(p.pending)
	p.mu.Unlock()
	if pending != 1 {
		t.Fatalf("%d pending probes, want 1 (the stale one should be dropped)", pending)
	}
}

// A path must be renewed while it is still good. Refreshing only after it has
// expired leaves a window, every PathFresh, in which the peer has no usable
// path — which shows up as a node dropping its relay and re-acquiring it
// seconds later, and on a real link lasts a full round trip rather than the
// ~0 it costs in a container.
func TestPathIsRefreshedBeforeItExpires(t *testing.T) {
	if PathRefresh >= PathFresh {
		t.Fatalf("PathRefresh (%s) must be less than PathFresh (%s), or paths lapse before renewal", PathRefresh, PathFresh)
	}

	key := testKey(t)
	f := newFakeNet()
	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	bAddr := netip.MustParseAddrPort("10.0.0.2:51820")

	a := newTestProber(t, key, f, aAddr)
	newTestProber(t, key, f, bAddr)

	now := time.Now()
	a.Probe("peer-b", []netip.AddrPort{bAddr}, now)

	if a.NeedsProbe("peer-b", now) {
		t.Error("a just-confirmed path was immediately due for renewal")
	}

	// A hair past PathRefresh: the pong is recorded at the moment it is
	// handled, microseconds after `now`, so exactly PathRefresh later is
	// marginally too early.
	due := now.Add(PathRefresh + time.Second)
	if !a.NeedsProbe("peer-b", due) {
		t.Errorf("path not due for renewal after %s", PathRefresh)
	}
	if _, ok := a.Best("peer-b", due); !ok {
		t.Errorf("path had already expired by renewal time — the gap this exists to close")
	}
}

// A peer with no path at all must always be probed, or it is never found.
func TestUnknownPeerAlwaysNeedsProbe(t *testing.T) {
	key := testKey(t)
	p := NewProber(key, make([]byte, 32), func([]byte, netip.AddrPort) error { return nil })
	if !p.NeedsProbe("nobody", time.Now()) {
		t.Error("a peer with no known path was not due for probing")
	}
}
