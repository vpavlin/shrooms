package mesh

import (
	"crypto/ed25519"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

// relayTarget is a relay this device is configured to use, and how to talk to
// it.
//
// The key travels with the address because it is a property of the relay rather
// than of the mesh. A relay that is a member shares the network key and so
// shares the key derived from it; a relay somebody else runs must never hold
// either, and authenticates under its own token — or under a public key when it
// is open, where the MAC is a checksum and every real guarantee comes from the
// registrant's signature and the routability check instead.
//
// Keeping them together is what lets both kinds be configured at once, which is
// the ordinary case while moving off a relay of your own: keep the VPS listed
// while trying other people's, and drop it when they have proved themselves.
type relayTarget struct {
	addr  netip.AddrPort
	key   relay.Key
	blind bool
}

// handleFor is what this relay knows a device by.
//
// On a member relay, the tunnel key: it already holds the network key, so
// disguising anything from it hides nothing and would only be a way to get the
// two ends out of step.
//
// On a blind relay, a tag derived from the mesh relay key, which the stranger
// forwarding for us does not have. The relay never performs cryptography with
// this value — it is a map key and nothing else — which is what lets it be
// substituted at all. What it buys is that the operator cannot recognise a
// device on a second relay, match it against a key seen anywhere else, or learn
// anything about the mesh from the identifier; two operators comparing notes
// see unrelated numbers, because the relay's own address goes into the
// derivation.
//
// Both ends derive the same tag because both hold the mesh relay key and each
// other's tunnel keys, so this needs no negotiation and no extra wire field.
func (m *Mesh) handleFor(t relayTarget, wg identity.WGKey) identity.WGKey {
	if !t.blind {
		return wg
	}
	return relay.Tag(m.relayKey, t.addr, wg)
}

// signerFor is the key this device signs registrations to a relay with.
//
// A member relay is told our mesh identity because it already knows it — it
// holds the roster and checks the pairing against it. A blind relay is told a
// key derived per relay instead: the signing key travels in cleartext, so using
// the mesh identity would hand every operator one stable name for this device
// and undo the tag beside it entirely.
func (m *Mesh) signerFor(t relayTarget) ed25519.PrivateKey {
	if !t.blind {
		return m.st.Identity.DevicePriv
	}
	return relay.RelayIdentity(m.relayKey, t.addr,
		m.st.Identity.DevicePriv.Public().(ed25519.PublicKey))
}

// relayHandle is handleFor against the relay currently carrying traffic, for
// callers that have already chosen one.
func (m *Mesh) relayHandle(wg identity.WGKey) identity.WGKey {
	if !m.blind || len(m.relays) == 0 {
		return wg
	}
	return m.handleFor(m.relays[0], wg)
}

// RelayInUse is the relay currently carrying traffic, and whether it is one
// somebody else runs.
//
// Reported because status could not otherwise see it. A blind relay is not a
// peer and never announces, so a node counting relays in its roster finds none
// and says "no relay" while routing everything through one — which is a
// confusing thing to read while it is working.
func (m *Mesh) RelayInUse() (netip.AddrPort, bool, bool) {
	if !m.relayNow.IsValid() {
		return netip.AddrPort{}, false, false
	}
	t, ok := m.targetFor(m.relayNow)
	return m.relayNow, ok && t.blind, true
}

// ConfiguredRelays counts the relays this device was told about, and how many
// of those are run by people who are not members.
func (m *Mesh) ConfiguredRelays() (total, blind int) {
	for _, t := range m.relays {
		total++
		if t.blind {
			blind++
		}
	}
	return total, blind
}

// parseRelayAddr accepts one configured relay address, complaining rather than
// failing: a mistyped entry should cost that relay, not the mesh.
func parseRelayAddr(log *slog.Logger, s string) (netip.AddrPort, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.AddrPort{}, false
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		log.Warn("ignoring unparseable relay address", "value", s, "err", err)
		return netip.AddrPort{}, false
	}
	return ap, true
}
