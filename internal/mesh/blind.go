package mesh

import (
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

// relayHandle is what a relay knows a device by.
//
// On a relay that is a member of this mesh, that is simply the tunnel key: it
// already holds the network key, so nothing is hidden by disguising it.
//
// On a blind relay it is a tag derived from the mesh relay key, which the
// stranger forwarding for us does not have. The relay never performs
// cryptography with this value — it is a map key and nothing else — which is
// what lets it be substituted at all. What that buys is that the operator
// cannot recognise a device on a second relay, match it against a key seen
// anywhere else, or learn anything about the mesh from the identifier; two
// operators comparing notes see unrelated numbers.
//
// Both ends derive the same tag because both hold the mesh relay key and each
// other's tunnel keys, so this needs no negotiation and no extra wire field.
func (m *Mesh) relayHandle(wg identity.WGKey) identity.WGKey {
	if !m.blind {
		return wg
	}
	return relay.Tag(m.relayKey, wg)
}
