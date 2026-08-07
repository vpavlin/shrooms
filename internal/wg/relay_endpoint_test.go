package wg

import (
	"net/netip"
	"testing"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/relay"
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
