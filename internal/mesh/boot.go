package mesh

import (
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// Publishing an address other members can bootstrap their rendezvous
// connection from (ADR-031).
//
// The problem this solves is narrow and was expensive: on 2026-08-20 five of
// the six public entry nodes refused connections. Every machine already
// connected stayed up; the one that had restarted went to zero peers and could
// not get back in, because bootstrap addresses are read when the delivery node
// is constructed and it had nowhere else to look. A mesh that publishes its own
// entry points does not have that failure.

// bootPeerID caches the delivery node's peer id.
//
// One FFI call, not one per announce: the id is fixed for the life of the node
// and announces go out every 45 seconds, per mesh, forever.
var (
	bootOnce sync.Once
	bootID   string
)

// bootAddr is the multiaddr to publish, or "" when this node is not one others
// should bootstrap from.
//
// Three conditions, all necessary:
//
//   - Core. An Edge node subscribes and forwards nothing, so bootstrapping from
//     it buys a connection to a node with no gossip to share.
//   - relay. Already means publicly reachable — selectRelay skips relays for
//     relay nodes "because a relay is publicly reachable by definition" — which
//     is exactly the property a bootstrap address needs.
//   - a pinned delivery port. The library will not say which port it chose, so
//     an unpinned node cannot describe itself; and a random port would change
//     on the next restart, publishing an address that quietly stops working.
//
// The address is the same public IP this node already advertises for
// WireGuard, with the delivery port rather than the tunnel port. A node that
// cannot name a public address for itself publishes nothing rather than
// guessing: a bootstrap address that does not answer is worse than none, since
// a peer that keeps it wastes its first dial on it.
func (m *Mesh) bootAddr(now time.Time) string { return m.bootAddrFor(m.cfg, now) }

// bootAddrFor is bootAddr with the config passed in, so the conditions can be
// exercised without standing up a delivery node.
func (m *Mesh) bootAddrFor(cfg state.Config, now time.Time) string {
	if cfg.Mode != "Core" || !cfg.Relay || cfg.DeliveryPort == 0 {
		return ""
	}
	ip := m.publicIPFrom(cfg, now)
	if !ip.IsValid() {
		return ""
	}
	bootOnce.Do(func() {
		if id, err := m.node.PeerID(); err == nil {
			bootID = strings.TrimSpace(id)
		}
	})
	if bootID == "" {
		return ""
	}
	host := "ip4"
	if ip.Is6() {
		host = "ip6"
	}
	return "/" + host + "/" + ip.String() +
		"/tcp/" + strconv.Itoa(int(cfg.DeliveryPort)) +
		"/p2p/" + bootID
}

// publicIP is a globally routable address this node is reachable at.
//
// Configured first, because an operator who wrote it down knows better than we
// do; then what peers report seeing us at. Private and loopback addresses are
// refused — they are useless to anybody off this LAN, and a mesh's members are
// mostly off it.
func (m *Mesh) publicIPFrom(cfg state.Config, now time.Time) netip.Addr {
	for _, a := range cfg.Advertise {
		if ap, err := netip.ParseAddrPort(a); err == nil && routable(ap.Addr()) {
			return ap.Addr()
		}
	}
	if m.prober != nil {
		for _, ap := range m.prober.Reflexive(now) {
			if routable(ap.Addr()) {
				return ap.Addr()
			}
		}
	}
	return netip.Addr{}
}

func routable(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsPrivate() && !a.IsLoopback() &&
		!a.IsLinkLocalUnicast() && !a.IsUnspecified() && !a.IsMulticast()
}
