package control

import (
	"encoding/hex"
	"sync"
)

// ReplayGuard rejects announces that do not strictly advance a device's
// sequence number.
//
// This is what stops a passive observer on the public bus from capturing an old
// announce and replaying it to roll a peer's endpoint back to a stale address.
// The observer cannot decrypt or forge, but without this it can still rewind.
// With it, suppression is the only remaining attack — and suppression is
// bounded by credential expiry once M5 lands.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]uint64 // hex(devicePub) -> highest seq accepted
}

// NewReplayGuard returns an empty guard.
func NewReplayGuard() *ReplayGuard {
	return &ReplayGuard{seen: make(map[string]uint64)}
}

// Accept reports whether an announce is fresh, and records it if so.
//
// The first announce from a device is always accepted: we have no prior state,
// and the alternative (rejecting until we have seen two) would make a restarted
// peer permanently invisible. A device that restarts must therefore persist its
// sequence number — see State.
func (g *ReplayGuard) Accept(a *Announce) bool {
	key := hex.EncodeToString(a.DevicePub)

	g.mu.Lock()
	defer g.mu.Unlock()

	prev, known := g.seen[key]
	if known && a.Seq <= prev {
		return false
	}
	g.seen[key] = a.Seq
	return true
}

// Seq returns the highest sequence number seen for a device.
func (g *ReplayGuard) Seq(devicePub []byte) (uint64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.seen[hex.EncodeToString(devicePub)]
	return v, ok
}

// Forget drops a device's state, e.g. on revocation.
func (g *ReplayGuard) Forget(devicePub []byte) {
	g.mu.Lock()
	delete(g.seen, hex.EncodeToString(devicePub))
	g.mu.Unlock()
}
