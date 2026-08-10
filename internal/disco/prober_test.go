package disco

import (
	"crypto/ed25519"
	"encoding/hex"
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

// newTestProber returns a prober and the peer id others must use to probe it.
//
// The id is hex of its device key, exactly as the roster derives it, because a
// pong is now accepted only from the device that was probed. Passing a label
// like idB would be rejected — correctly, and confusingly if the helper
// hid it.
func newTestProber(t *testing.T, key Key, f *fakeNet, at netip.AddrPort) (*Prober, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	p := NewProber(key, pub, f.sendFrom(at))
	id := hex.EncodeToString(pub)

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
	return p, id
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

	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, bAddr)

	now := time.Now()
	a.Probe(idB, []netip.AddrPort{bAddr}, now)

	if _, ok := a.Best(idB, time.Now()); !ok {
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

	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, bAddr)

	a.Probe(idB, []netip.AddrPort{dead, bAddr}, time.Now())

	best, ok := a.Best(idB, time.Now())
	if !ok {
		t.Fatal("no working path")
	}
	if best.Addr == dead {
		t.Fatalf("selected the black-holed candidate %s", dead)
	}
}

// unprobed is any peer id: these tests never deliver a pong, so the identity
// check that guards path acceptance is not in play.
const unprobed = "0f1e2d3c"

func TestNoPathBeforeProbing(t *testing.T) {
	a := NewProber(testKey(t), make([]byte, 32), func([]byte, netip.AddrPort) error { return nil })
	if _, ok := a.Best(unprobed, time.Now()); ok {
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

	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, bAddr)

	a.Probe(idB, []netip.AddrPort{bAddr}, time.Now())

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

	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, bAddr)
	a.Probe(idB, []netip.AddrPort{bAddr}, time.Now())

	if _, ok := a.Best(idB, time.Now()); !ok {
		t.Fatal("expected a fresh path")
	}
	if _, ok := a.Best(idB, time.Now().Add(PathFresh+time.Second)); ok {
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
	p.Probe(unprobed, []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:1")}, start)
	if sent != 1 {
		t.Fatalf("sent %d probes, want 1", sent)
	}

	// A later probe round expires the earlier unanswered one.
	p.Probe(unprobed, []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:1")}, start.Add(ProbeTimeout+time.Second))

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

	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, bAddr)

	now := time.Now()
	a.Probe(idB, []netip.AddrPort{bAddr}, now)

	if a.NeedsProbe(idB, now) {
		t.Error("a just-confirmed path was immediately due for renewal")
	}

	// A hair past PathRefresh: the pong is recorded at the moment it is
	// handled, microseconds after `now`, so exactly PathRefresh later is
	// marginally too early.
	due := now.Add(PathRefresh + time.Second)
	if !a.NeedsProbe(idB, due) {
		t.Errorf("path not due for renewal after %s", PathRefresh)
	}
	if _, ok := a.Best(idB, due); !ok {
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

// pathAt injects a confirmed path directly, so a test can set exact RTTs.
func pathAt(p *Prober, peerID string, addr netip.AddrPort, rtt time.Duration, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	byAddr := p.paths[peerID]
	if byAddr == nil {
		byAddr = make(map[netip.AddrPort]*Path)
		p.paths[peerID] = byAddr
	}
	byAddr[addr] = &Path{Addr: addr, RTT: rtt, LastPong: at}
}

func newBareProber(t *testing.T) *Prober {
	t.Helper()
	return NewProber(testKey(t), make([]byte, 32), func([]byte, netip.AddrPort) error { return nil })
}

// Two near-equal paths must not trade places. Observed on real infrastructure:
// a peer with two addresses on the same host alternated every 3s, and every
// alternation rewrites WireGuard's endpoint — churn that costs a full handshake
// retry if it lands mid-negotiation.
func TestBestDoesNotFlapBetweenNearEqualPaths(t *testing.T) {
	p := newBareProber(t)
	now := time.Now()
	a := netip.MustParseAddrPort("10.89.7.1:51820")
	b := netip.MustParseAddrPort("10.93.0.1:51820")

	pathAt(p, "peer", a, 3*time.Millisecond, now)
	pathAt(p, "peer", b, 4*time.Millisecond, now)

	first, ok := p.Best("peer", now)
	if !ok {
		t.Fatal("no path selected")
	}

	// Jitter reverses the ordering on each subsequent tick.
	for i := 1; i <= 10; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		if i%2 == 0 {
			pathAt(p, "peer", a, 3*time.Millisecond, at)
			pathAt(p, "peer", b, 2*time.Millisecond, at)
		} else {
			pathAt(p, "peer", a, 2*time.Millisecond, at)
			pathAt(p, "peer", b, 3*time.Millisecond, at)
		}
		got, ok := p.Best("peer", at)
		if !ok {
			t.Fatalf("tick %d: lost the path entirely", i)
		}
		if got.Addr != first.Addr {
			t.Fatalf("tick %d: switched %v -> %v on %s of jitter",
				i, first.Addr, got.Addr, time.Millisecond)
		}
	}
}

// Stickiness must not become stubbornness: a decisively better path wins.
func TestBestSwitchesWhenClearlyBetter(t *testing.T) {
	p := newBareProber(t)
	now := time.Now()
	slow := netip.MustParseAddrPort("10.0.0.1:51820")
	fast := netip.MustParseAddrPort("10.0.0.2:51820")

	pathAt(p, "peer", slow, 200*time.Millisecond, now)
	if got, _ := p.Best("peer", now); got.Addr != slow {
		t.Fatalf("selected %v, want the only path %v", got.Addr, slow)
	}

	pathAt(p, "peer", fast, 20*time.Millisecond, now)
	if got, _ := p.Best("peer", now); got.Addr != fast {
		t.Errorf("stayed on the %s path with a %s one available", 200*time.Millisecond, 20*time.Millisecond)
	}
}

// IPv6 wins regardless of latency: no NAT to traverse at all.
func TestBestSwitchesToIPv6EvenIfSlower(t *testing.T) {
	p := newBareProber(t)
	now := time.Now()
	v4 := netip.MustParseAddrPort("10.0.0.1:51820")
	v6 := netip.MustParseAddrPort("[2001:db8::1]:51820")

	pathAt(p, "peer", v4, 5*time.Millisecond, now)
	p.Best("peer", now)

	pathAt(p, "peer", v6, 40*time.Millisecond, now)
	if got, _ := p.Best("peer", now); got.Addr != v6 {
		t.Errorf("selected %v, want the IPv6 path", got.Addr)
	}
}

// When the path in use goes stale, another must take over rather than the peer
// being reported unreachable.
func TestBestLeavesStaleSelection(t *testing.T) {
	p := newBareProber(t)
	now := time.Now()
	old := netip.MustParseAddrPort("10.0.0.1:51820")
	fresh := netip.MustParseAddrPort("10.0.0.2:51820")

	pathAt(p, "peer", old, 5*time.Millisecond, now)
	p.Best("peer", now)

	later := now.Add(PathFresh + time.Second)
	pathAt(p, "peer", fresh, 50*time.Millisecond, later)

	got, ok := p.Best("peer", later)
	if !ok {
		t.Fatal("reported unreachable while a fresh path existed")
	}
	if got.Addr != fresh {
		t.Errorf("stayed on the stale path %v", got.Addr)
	}
}

// Probing our own address must never produce a path.
//
// This is a real failure, not a hypothetical. A peer announced an address this
// machine also held — DHCP had moved the lease — so we probed ourselves, our
// own responder answered in about a millisecond, and that beat every genuine
// candidate. WireGuard then sent the peer's handshakes here, which reported
// them as "Received invalid initiation". The peer sat at "no handshake" with
// 32 KB sent and nothing received, its selected path a 1ms route to itself.
//
// Two things stop it: we do not probe an address we hold, and a pong is
// accepted only from the device that was probed.
func TestOwnAddressNeverBecomesAPath(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()

	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	a, _ := newTestProber(t, key, f, aAddr)
	_, idB := newTestProber(t, key, f, netip.MustParseAddrPort("10.0.0.2:51820"))

	a.SetSelfAddrs([]netip.Addr{aAddr.Addr()})

	// The peer announces an address that is really ours.
	a.Probe(idB, []netip.AddrPort{aAddr}, time.Now())

	if best, ok := a.Best(idB, time.Now()); ok {
		t.Fatalf("selected our own address %s as a path to a peer", best.Addr)
	}
}

// Even without the self-address list, a pong from the wrong device is refused.
// That is the check that holds when a candidate is ours but unrecognised — and
// it is also what stops one mesh member answering for another.
func TestPongFromTheWrongDeviceIsRejected(t *testing.T) {
	key := testKey(t)
	f := newFakeNet()

	aAddr := netip.MustParseAddrPort("10.0.0.1:51820")
	a, _ := newTestProber(t, key, f, aAddr)

	// c answers, but we asked for b.
	cAddr := netip.MustParseAddrPort("10.0.0.3:51820")
	_, idC := newTestProber(t, key, f, cAddr)
	_, idB := newTestProber(t, key, f, netip.MustParseAddrPort("10.0.0.2:51820"))

	a.Probe(idB, []netip.AddrPort{cAddr}, time.Now())

	if _, ok := a.Best(idB, time.Now()); ok {
		t.Error("accepted a path to b that was answered by c")
	}
	// And c answering for itself is still fine.
	a.Probe(idC, []netip.AddrPort{cAddr}, time.Now())
	if _, ok := a.Best(idC, time.Now()); !ok {
		t.Error("rejected a pong from the device actually probed")
	}
}
