package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/identity"
)

// A roamed endpoint is conceded to only with evidence that it works both
// ways: the relay, or an address a probe was answered from (ADR-027's review).
func TestTwoWayNeedsAPongOrTheRelay(t *testing.T) {
	var nk identity.NetworkKey
	rand.Read(nk[:])
	key := disco.DeriveKey(nk)
	_, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	peerID := hex.EncodeToString(pubB)
	direct := netip.MustParseAddrPort("198.51.100.7:51820")
	other := netip.MustParseAddrPort("198.51.100.8:51820")

	// A probes B at direct; B answers; the pong comes back to A from direct.
	var a, b *disco.Prober
	b = disco.NewProber(key, privB, func(pkt []byte, to netip.AddrPort) error {
		m, err := disco.Decode(key, pkt)
		if err == nil {
			a.HandlePong(m, direct, time.Now())
		}
		return nil
	})
	a = disco.NewProber(key, privA, func(pkt []byte, to netip.AddrPort) error {
		if to != direct {
			return nil
		}
		m, err := disco.Decode(key, pkt)
		if err == nil {
			b.HandlePing(m, netip.MustParseAddrPort("203.0.113.1:51820"))
		}
		return nil
	})
	m := &Mesh{log: slog.New(slog.DiscardHandler), prober: a}
	now := time.Now()

	if m.twoWay(peerID, direct.String(), now) {
		t.Fatal("an address never answered from was vouched for")
	}
	a.Probe(peerID, []netip.AddrPort{direct}, now)
	if !m.twoWay(peerID, direct.String(), now) {
		t.Fatal("an address a probe was answered from was not vouched for")
	}
	if m.twoWay(peerID, other.String(), now) {
		t.Fatal("an inbound-only address was vouched for")
	}
	if m.twoWay(peerID, direct.String(), now.Add(disco.PathFresh+time.Second)) {
		t.Fatal("a stale pong still vouched for the address")
	}
	if !m.twoWay(peerID, "relay:00@128.140.55.128:51822", now) {
		t.Fatal("the relay was not vouched for")
	}
}
