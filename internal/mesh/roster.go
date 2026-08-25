package mesh

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
)

// OfflineAfter is how long without an announce before a peer is shown offline.
// Announces go out every AnnounceInterval (45s), so this tolerates three
// misses before a peer is called offline.
const OfflineAfter = 3 * time.Minute

// ForgetAfter is when a peer stops being remembered at all, as opposed to
// being remembered as offline.
//
// Comfortably beyond control.MaxClockSkew (2h), and that bound is the reason
// this is safe: forgetting a peer also forgets its replay counter, and a
// captured announce older than the skew window is already rejected on its
// timestamp. Dropping the counter therefore cannot reopen a replay.
//
// Long enough that a machine switched off for the working day comes back as
// itself, with its timings and throughput history intact.
const ForgetAfter = 6 * time.Hour

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

	// Relay reports that this peer will forward for others.
	Relay bool
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
		Relay:     a.Relay,
	}

	prev, existed := r.peers[id]
	r.peers[id] = p

	// A heartbeat that changes nothing must not trigger a data-plane update:
	// SetPeers replaces the whole peer set, and doing that every 30s would
	// churn tunnels for no reason.
	changed := !existed ||
		prev.WGPub != p.WGPub ||
		prev.Name != p.Name ||
		prev.Relay != p.Relay ||
		!equalStrings(prev.Endpoints, p.Endpoints)

	return p, changed
}

// Restore puts a remembered peer back without an announce to carry it.
//
// The timestamp is the one the peer actually had, not now. That is the whole
// discipline of this: the entry says when the peer was last heard, which is a
// fact about the peer and not a claim about this process, so Online() keeps
// telling the truth and nothing downstream needs a new idea of what a peer is.
//
// Two consequences, and both are wanted. A memory older than OfflineAfter comes
// back reading offline, and needs the provisional window to be installed at all.
// A memory from ten seconds before a crash comes back reading online — honestly,
// because it was — and is carried by the ordinary rule exactly as it would have
// been had the process never died. What this buys in both cases is an endpoint
// for the data plane to try before the rendezvous plane has come up.
//
// A peer already in the roster is left alone. Restore runs at startup, so the
// only way that happens is an announce having arrived first — which is better
// evidence than a memory, and later.
//
// Membership is the caller's to check. This deliberately takes a PeerInfo and
// not the stored form, so nothing can reach the roster without having gone
// through the same credential check an announce does.
func (r *Roster) Restore(p PeerInfo) bool {
	id := hex.EncodeToString(p.DevicePub)

	r.mu.Lock()
	defer r.mu.Unlock()

	if id == r.self {
		return false
	}
	if _, exists := r.peers[id]; exists {
		return false
	}
	p.Overlay = identity.OverlayAddr(r.nk, p.DevicePub)
	r.peers[id] = p
	return true
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

// Current is the roster with superseded entries left out.
//
// A device that changes identity — re-enrolled, re-installed, or given a
// per-mesh identity by ADR-015 — announces under a key nothing has seen
// before, and the entry for its old key stays until ForgetAfter drops it hours
// later. Both are the same machine. The old one has no tunnel and never will,
// so it sits in the roster as a peer that is permanently offline, and on a
// device that has been re-added a few times the list is mostly ghosts.
//
// So an entry is hidden when another entry claims the same name, was heard
// from more recently, and this one has gone quiet. All three conditions
// matter: two live machines that genuinely share a name are both still shown,
// which is the case where hiding one would be a lie.
//
// Peers stays as it is — everything that reasons about membership, replay or
// revocation must see every key that announced, and only the display wants
// this.
func (r *Roster) Current(now time.Time) []PeerInfo {
	all := r.Peers()
	freshest := map[string]time.Time{}
	for _, p := range all {
		if t, ok := freshest[sanitiseName(p.Name)]; !ok || p.LastSeen.After(t) {
			freshest[sanitiseName(p.Name)] = p.LastSeen
		}
	}
	out := all[:0:0]
	for _, p := range all {
		if !p.Online(now) && freshest[sanitiseName(p.Name)].After(p.LastSeen) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Known reports whether this peer id is on the roster.
//
// The question anything arriving outside an announce has to ask: a roster entry
// is the outcome of a verified announce — credential checked, replay guard
// passed — so reusing it is stronger than any check a later message could make
// for itself.
func (r *Roster) Known(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.peers[id]
	return ok
}

// Len reports how many peers are known.
func (r *Roster) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// Prune drops peers not seen for longer than ForgetAfter and returns their ids.
//
// Without this nothing is ever removed: Apply only inserts, Forget was wired
// solely to revocation (unimplemented until M5), and Online() merely reports
// false while the entry stays. Since a peer is anyone holding the network key
// — a bearer credential — one member announcing endless distinct device keys
// grows every node's maps until restart. Peers legitimately come and go too,
// so this is a liveness fix as much as a hardening one.
//
// The caller is expected to forget the same ids elsewhere (replay, paths,
// timings, rates), which is why the ids are returned rather than dropped
// silently.
func (r *Roster) Prune(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var gone []string
	for id, p := range r.peers {
		if now.Sub(p.LastSeen) > ForgetAfter {
			delete(r.peers, id)
			gone = append(gone, id)
		}
	}
	return gone
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
