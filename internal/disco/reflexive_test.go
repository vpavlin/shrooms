package disco

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/netip"
	"testing"
	"time"
)

// observe makes the prober believe `peer` saw us at `at`, the way a pong does.
//
// Driven through HandlePong rather than by writing the map directly, so the
// test exercises the path a real pong takes — including the check that a pong
// comes from the device that was probed.
func observe(t *testing.T, p *Prober, peerPriv ed25519.PrivateKey, at netip.AddrPort) {
	t.Helper()
	peerID := hex.EncodeToString(peerPriv.Public().(ed25519.PublicKey))

	// A probe has to be outstanding for its answer to count.
	p.mu.Lock()
	var tx TxID
	tx[0] = byte(len(p.pending) + 1)
	p.pending[tx] = probe{peerID: peerID, sentAt: time.Now()}
	p.mu.Unlock()

	m := &Message{Type: TypePong, TxID: tx, Observed: at}
	copy(m.SenderPub[:], peerPriv.Public().(ed25519.PublicKey))
	if _, ok := p.HandlePong(m, netip.MustParseAddrPort("203.0.113.1:1"), time.Now()); !ok {
		t.Fatal("the pong was not accepted")
	}
}

func peerKey(t *testing.T, n byte) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = n
	return ed25519.NewKeyFromSeed(seed)
}

// Two peers agreeing means the NAT maps this socket the same way for everybody,
// so the address genuinely is ours and is worth telling people about.
func TestAgreeingPeersConfirmAnAddress(t *testing.T) {
	p := bareProber(t)
	pub := netip.MustParseAddrPort("198.51.100.7:51820")

	observe(t, p, peerKey(t, 1), pub)
	observe(t, p, peerKey(t, 2), pub)

	got := p.Reflexive(time.Now())
	if len(got) != 1 || got[0] != pub {
		t.Fatalf("got %v, want just %v", got, pub)
	}
}

// Peers disagreeing means the NAT maps per destination, and none of those
// addresses works for anybody but the peer that observed it. Announcing them
// is worse than announcing nothing: peers probe an address something else is
// behind, and the path flaps between direct and relayed.
//
// This is the case seen in the field, where two machines behind one home NAT
// both advertised the same external address and a phone alternated between a
// direct path and a relay every few minutes.
func TestDisagreeingPeersSuppressTheAddresses(t *testing.T) {
	p := bareProber(t)
	observe(t, p, peerKey(t, 1), netip.MustParseAddrPort("198.51.100.7:51820"))
	observe(t, p, peerKey(t, 2), netip.MustParseAddrPort("198.51.100.7:51999"))

	if got := p.Reflexive(time.Now()); len(got) != 0 {
		t.Errorf("announced %v from a NAT that maps per destination", got)
	}
}

// One peer is not a disagreement, and a two-node mesh has only one vantage
// point. An unverified address is kept, because it is no worse than having no
// candidate at all — and without it a pair of NATed nodes could never find a
// direct path.
func TestASingleObserverIsStillUsed(t *testing.T) {
	p := bareProber(t)
	pub := netip.MustParseAddrPort("198.51.100.7:51820")
	observe(t, p, peerKey(t, 1), pub)

	got := p.Reflexive(time.Now())
	if len(got) != 1 || got[0] != pub {
		t.Fatalf("got %v, want %v — a lone observation is better than nothing", got, pub)
	}
}

// When some addresses are corroborated and others are not, the corroborated
// ones are what get announced rather than merely being listed first: an
// uncorroborated address on an endpoint-dependent NAT is not a weaker
// candidate, it is a wrong one.
func TestCorroboratedWinsOverMerelyRecent(t *testing.T) {
	p := bareProber(t)
	good := netip.MustParseAddrPort("198.51.100.7:51820")
	odd := netip.MustParseAddrPort("198.51.100.7:51999")

	observe(t, p, peerKey(t, 1), good)
	observe(t, p, peerKey(t, 2), good)
	observe(t, p, peerKey(t, 3), odd) // a third peer sees something else

	got := p.Reflexive(time.Now())
	if len(got) != 1 || got[0] != good {
		t.Fatalf("got %v, want just the corroborated %v", got, good)
	}
}

// An observation ages out, and when the last one does the address goes with it.
func TestStaleObservationsAreDropped(t *testing.T) {
	p := bareProber(t)
	observe(t, p, peerKey(t, 1), netip.MustParseAddrPort("198.51.100.7:51820"))

	if got := p.Reflexive(time.Now().Add(reflexiveTTL + time.Minute)); len(got) != 0 {
		t.Errorf("a stale observation was still announced: %v", got)
	}
}

// bareProber is a prober with no network, for testing what it concludes rather
// than what it sends.
func bareProber(t *testing.T) *Prober {
	t.Helper()
	return NewProber(testKey(t), make([]byte, 64), func([]byte, netip.AddrPort) error { return nil })
}
