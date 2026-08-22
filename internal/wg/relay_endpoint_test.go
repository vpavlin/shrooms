package wg

import (
	"encoding/hex"
	"net/netip"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

func testRelayKey(t *testing.T) relay.Key {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("network key: %v", err)
	}
	return relay.DeriveKey(nk)
}

// WireGuard is configured over the UAPI with an endpoint *string* and calls
// ParseEndpoint to turn it back into an endpoint. If that round trip loses the
// relay-ness, Send is handed a plain endpoint and nothing is ever wrapped —
// which fails as a silently dead relay rather than an error.
func TestRelayEndpointSurvivesUAPIRoundTrip(t *testing.T) {
	b := NewBind()
	key := testRelayKey(t)
	self, peer := identity.WGKey{1, 2, 3}, identity.WGKey{4, 5, 6}
	b.SetRelayIdentity(key, self)

	orig := NewRelayEndpoint(key, netip.MustParseAddrPort("203.0.113.4:51820"), self, peer)

	got, err := b.ParseEndpoint(orig.DstToString())
	if err != nil {
		t.Fatalf("ParseEndpoint(%q): %v", orig.DstToString(), err)
	}
	re, ok := got.(*RelayEndpoint)
	if !ok {
		t.Fatalf("round trip produced %T, not a RelayEndpoint", got)
	}
	if re.PeerPub != peer || re.Relay != orig.Relay {
		t.Fatalf("round trip changed the endpoint: %+v", re)
	}
}

// The failure this guards against actually happened: SetRelayIdentity existed
// but was never called, so endpoints were built with a zero key and the relay
// dropped every frame with no error anywhere.
func TestParseEndpointFailsWithoutRelayIdentity(t *testing.T) {
	b := NewBind()
	ep := NewRelayEndpoint(testRelayKey(t), netip.MustParseAddrPort("203.0.113.4:51820"),
		identity.WGKey{1}, identity.WGKey{2})

	if _, err := b.ParseEndpoint(ep.DstToString()); err == nil {
		t.Fatal("built a relay endpoint without a relay identity; frames would be silently dropped")
	}
}

func TestParseEndpointStillHandlesPlainAddresses(t *testing.T) {
	b := NewBind()
	got, err := b.ParseEndpoint("203.0.113.4:51820")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if _, isRelay := got.(*RelayEndpoint); isRelay {
		t.Fatal("a plain address parsed as a relay endpoint")
	}
}

func TestParseEndpointRejectsMalformedRelayForm(t *testing.T) {
	b := NewBind()
	b.SetRelayIdentity(testRelayKey(t), identity.WGKey{1})

	for _, s := range []string{"relay:", "relay:nothex@1.2.3.4:1", "relay:aabb@notanaddr", "relay:aabb"} {
		if _, err := b.ParseEndpoint(s); err == nil {
			t.Errorf("accepted malformed relay endpoint %q", s)
		}
	}
}

// A node may use its own relay and a stranger's at once — the documentation
// calls that the ordinary case while moving between them — and the two are
// spoken to under different keys with different handles.
//
// WireGuard stores an endpoint as a string and hands it back to ParseEndpoint
// to rebuild, and that string carries only the peer and the relay address. So
// everything else is looked up, and looking it up in one global slot meant every
// frame to the blind relay was authenticated with the mesh key and dropped
// without comment. Registration was unaffected, so the relay looked healthy and
// carried nothing.
func TestEachRelayIsRebuiltWithItsOwnKey(t *testing.T) {
	b := NewBind()
	meshKey := testRelayKey(t)
	var blindKey relay.Key
	for i := range blindKey {
		blindKey[i] = 0xbb
	}
	mine := netip.MustParseAddrPort("198.51.100.1:51820")
	theirs := netip.MustParseAddrPort("203.0.113.9:31760")
	selfMesh := identity.WGKey{1}
	selfTag := identity.WGKey{2}

	b.SetRelayIdentity(meshKey, selfMesh)
	b.SetRelayIdentityFor(theirs, blindKey, selfTag)

	peer := identity.WGKey{9}
	for _, tc := range []struct {
		name string
		at   netip.AddrPort
		key  relay.Key
		self identity.WGKey
	}{
		{"our own relay", mine, meshKey, selfMesh},
		{"a stranger's", theirs, blindKey, selfTag},
	} {
		ep, err := b.ParseEndpoint(relayScheme + hex.EncodeToString(peer[:]) + "@" + tc.at.String())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		re, ok := ep.(*RelayEndpoint)
		if !ok {
			t.Fatalf("%s: not a relay endpoint", tc.name)
		}
		if re.key != tc.key {
			t.Errorf("%s: rebuilt with the wrong key, so its frames would be dropped", tc.name)
		}
		if re.SelfPub != tc.self {
			t.Errorf("%s: rebuilt with the wrong handle, so the peer could not address us back", tc.name)
		}
	}
}
