package wg

import (
	"net/netip"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
)

func peer(b byte, endpoint string) Peer {
	var k identity.WGKey
	k[0] = b
	return Peer{
		WGPub:     k,
		Endpoint:  endpoint,
		AllowedIP: netip.MustParseAddr("fd00::1"),
		Keepalive: 25,
	}
}

// The regression this guards against: SetPeers used replace_peers=true on every
// call, so every sync tore down and recreated each peer. A handshake initiation
// arriving while a peer was being recreated is rejected by wireguard-go with
// "Received invalid initiation message", and the handshake never completes.
//
// It only shows up over a real network: on a LAN the handshake finishes inside
// the gap, but at 100-450 ms round trips it loses the race repeatedly.
func TestSamePeersDetectsNoOp(t *testing.T) {
	a := []Peer{peer(1, "203.0.113.1:51820"), peer(2, "203.0.113.2:51820")}
	b := []Peer{peer(1, "203.0.113.1:51820"), peer(2, "203.0.113.2:51820")}

	if !samePeers(a, b) {
		t.Fatal("identical peer sets reported as different; every sync would rewrite the device")
	}
}

// The roster is sorted by name, so a rename reorders it. That must not be
// mistaken for a configuration change.
func TestSamePeersIgnoresOrder(t *testing.T) {
	a := []Peer{peer(1, "203.0.113.1:51820"), peer(2, "203.0.113.2:51820")}
	b := []Peer{peer(2, "203.0.113.2:51820"), peer(1, "203.0.113.1:51820")}

	if !samePeers(a, b) {
		t.Fatal("reordering was treated as a change")
	}
}

func TestSamePeersDetectsRealChanges(t *testing.T) {
	base := []Peer{peer(1, "203.0.113.1:51820")}

	roamed := []Peer{peer(1, "198.51.100.7:51820")}
	if samePeers(base, roamed) {
		t.Error("an endpoint change was missed")
	}

	added := []Peer{peer(1, "203.0.113.1:51820"), peer(2, "203.0.113.2:51820")}
	if samePeers(base, added) {
		t.Error("an added peer was missed")
	}

	if samePeers(added, base) {
		t.Error("a removed peer was missed")
	}

	rekeyed := []Peer{peer(1, "203.0.113.1:51820")}
	rekeyed[0].PSK = [32]byte{9}
	if samePeers(base, rekeyed) {
		t.Error("a preshared key change was missed")
	}

	rerouted := []Peer{peer(1, "203.0.113.1:51820")}
	rerouted[0].AllowedIP = netip.MustParseAddr("fd00::2")
	if samePeers(base, rerouted) {
		t.Error("an AllowedIP change was missed")
	}
}

// Switching a peer between direct and relayed must be seen as a change, or
// failover would never be applied.
func TestSamePeersDetectsRelaySwitch(t *testing.T) {
	direct := []Peer{peer(1, "203.0.113.1:51820")}

	relayed := []Peer{peer(1, "")}
	relayed[0].RelayVia = NewRelayEndpoint(testRelayKey(t),
		netip.MustParseAddrPort("203.0.113.9:51820"), identity.WGKey{7}, relayed[0].WGPub)

	if samePeers(direct, relayed) {
		t.Fatal("switching to a relayed path was treated as no change")
	}
	if samePeers(relayed, direct) {
		t.Fatal("switching back to direct was treated as no change")
	}

	// A different relay for the same peer is also a change.
	other := []Peer{peer(1, "")}
	other[0].RelayVia = NewRelayEndpoint(testRelayKey(t),
		netip.MustParseAddrPort("198.51.100.9:51820"), identity.WGKey{7}, other[0].WGPub)
	if samePeers(relayed, other) {
		t.Error("changing which relay is used was missed")
	}
}

func TestSamePeersEmpty(t *testing.T) {
	if !samePeers(nil, nil) {
		t.Error("two empty sets differ")
	}
	if samePeers(nil, []Peer{peer(1, "203.0.113.1:1")}) {
		t.Error("adding to an empty set was missed")
	}
}
