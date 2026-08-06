package mesh

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/vpavlin/logos-vpn/internal/control"
	"github.com/vpavlin/logos-vpn/internal/identity"
)

// OfflineAfter is how long without an announce before a peer is shown offline.
// Announces go out every 30-60s, so this tolerates a couple of misses.
const OfflineAfter = 3 * time.Minute

// PeerInfo is what the mesh knows about another device.
//
// It is assembled from two sources: the gossip roster (who exists, their keys,
// their claimed name, their candidate endpoints) and local WireGuard state
// (whether we actually have a tunnel). Neither alone is sufficient — a peer can
// be announcing happily and still be unreachable.
type PeerInfo struct {
	DevicePub ed25519.PublicKey
	WGPub     identity.WGKey
	Name      string
	Overlay   netip.Addr
	Endpoints []string
	Seq       uint64
	LastSeen  time.Time
}

// Online reports whether the peer has announced recently.
func (p PeerInfo) Online(now time.Time) bool {
	return now.Sub(p.LastSeen) < OfflineAfter
}

// ID is the peer's stable identifier.
func (p PeerInfo) ID() string { return hex.EncodeToString(p.DevicePub) }

// Roster is the set of known peers, built from announces.
//
// Every node holds the full roster, and no node is authoritative — this is the
// control plane, and it comes free with the announce stream.
type Roster struct {
	mu    sync.RWMutex
	nk    identity.NetworkKey
	self  string // hex device pub, so we never add ourselves
	peers map[string]PeerInfo
}

// NewRoster returns an empty roster.
func NewRoster(nk identity.NetworkKey, self ed25519.PublicKey) *Roster {
	return &Roster{
		nk:    nk,
		self:  hex.EncodeToString(self),
		peers: make(map[string]PeerInfo),
	}
}

// Apply records an announce. It returns the peer and whether anything
// materially changed — i.e. whether the data plane needs reconfiguring.
//
// Sequence numbers and signatures are checked before this is called; Apply
// assumes the announce is authentic and fresh.
func (r *Roster) Apply(a *control.Announce, now time.Time) (PeerInfo, bool) {
	id := hex.EncodeToString(a.DevicePub)

	r.mu.Lock()
	defer r.mu.Unlock()

	if id == r.self {
		return PeerInfo{}, false
	}

	var wg identity.WGKey
	copy(wg[:], a.WGPub)

	p := PeerInfo{
		DevicePub: append(ed25519.PublicKey(nil), a.DevicePub...),
		WGPub:     wg,
		Name:      a.Name,
		Overlay:   identity.OverlayAddr(r.nk, a.DevicePub),
		Endpoints: append([]string(nil), a.Endpoints...),
		Seq:       a.Seq,
		LastSeen:  now,
	}

	prev, existed := r.peers[id]
	r.peers[id] = p

	// A heartbeat that changes nothing must not trigger a data-plane update:
	// SetPeers replaces the whole peer set, and doing that every 30s would
	// churn tunnels for no reason.
	changed := !existed ||
		prev.WGPub != p.WGPub ||
		prev.Name != p.Name ||
		!equalStrings(prev.Endpoints, p.Endpoints)

	return p, changed
}

// Peers returns the roster sorted by name, for stable output.
func (r *Roster) Peers() []PeerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]PeerInfo, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID() < out[j].ID()
	})
	return out
}

// Len reports how many peers are known.
func (r *Roster) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// Forget drops a peer, e.g. on revocation.
func (r *Roster) Forget(devicePub ed25519.PublicKey) {
	r.mu.Lock()
	delete(r.peers, hex.EncodeToString(devicePub))
	r.mu.Unlock()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
