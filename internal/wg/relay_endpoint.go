package wg

import (
	"encoding/hex"
	"net/netip"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/relay"
)

// RelayEndpoint is a conn.Endpoint that routes a peer's traffic through a relay.
//
// WireGuard holds exactly one endpoint per peer and treats it as opaque, which
// is what lets us substitute this for a direct address. Everything WireGuard
// sends to this peer gets wrapped in a relay frame and sent to the relay
// instead; the peer's own address never appears on the wire.
//
// Failing over between direct and relayed is therefore just swapping the
// endpoint — no tunnel teardown, no rehandshake, no dropped packets in flight.
type RelayEndpoint struct {
	// Relay is where frames are actually sent.
	Relay netip.AddrPort
	// PeerPub is the destination the relay should forward to.
	PeerPub identity.WGKey
	// SelfPub identifies us to the peer on the far side.
	SelfPub identity.WGKey

	key relay.Key
	src netip.AddrPort // the relay's address, reported as the packet source
}

// NewRelayEndpoint builds an endpoint routing to peerPub via relayAddr.
func NewRelayEndpoint(key relay.Key, relayAddr netip.AddrPort, selfPub, peerPub identity.WGKey) *RelayEndpoint {
	return &RelayEndpoint{
		Relay:   relayAddr,
		PeerPub: peerPub,
		SelfPub: selfPub,
		key:     key,
		src:     relayAddr,
	}
}

// Wrap encapsulates a WireGuard packet in a relay frame.
func (e *RelayEndpoint) Wrap(pkt []byte) ([]byte, error) {
	return relay.EncodeForward(e.key, e.PeerPub, e.SelfPub, pkt)
}

// --- conn.Endpoint ---
//
// DstToString reports the relay rather than the peer, because that is where
// packets actually go. `logos-vpn status` distinguishes the two separately, so
// a relayed peer is never mistaken for a direct one.

func (e *RelayEndpoint) ClearSrc()           { e.src = netip.AddrPort{} }
func (e *RelayEndpoint) SrcToString() string { return e.src.String() }

// DstToString returns the form ParseEndpoint understands, so an endpoint
// survives the UAPI round trip WireGuard performs when it is configured.
func (e *RelayEndpoint) DstToString() string {
	return relayScheme + hex.EncodeToString(e.PeerPub[:]) + "@" + e.Relay.String()
}
func (e *RelayEndpoint) DstIP() netip.Addr { return e.Relay.Addr() }
func (e *RelayEndpoint) SrcIP() netip.Addr { return e.src.Addr() }

func (e *RelayEndpoint) DstToBytes() []byte {
	// Used for mac2 cookie calculations, so it must be stable and unique per
	// destination. The peer key is both.
	return e.PeerPub[:]
}

var _ conn.Endpoint = (*RelayEndpoint)(nil)
