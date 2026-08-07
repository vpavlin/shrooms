package mesh

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/vpavlin/logos-vpn/internal/waku"
)

// RendezvousStale is how long without a received message before the rendezvous
// plane is treated as suspect.
//
// Every node announces every AnnounceInterval, so in a mesh of more than one
// device something should arrive well inside this. It is deliberately longer
// than one interval: a single missed gossip message is normal.
const RendezvousStale = 4 * AnnounceInterval

// Health is what the node knows about its own connection to the rendezvous
// plane, as opposed to the data plane.
//
// This exists because the two fail independently and look identical from the
// outside. When logos.dev is unreachable, existing tunnels keep working — by
// design, Waku is rendezvous and not a control plane — so `status` shows
// healthy tunnels and an empty or frozen roster, with nothing to say why. The
// answer "the fleet is down, your tunnels are fine" is the one piece of
// information that is otherwise impossible to get without reading nwaku's logs.
type Health struct {
	// Status is the library's own connection state: Connected,
	// PartiallyConnected, Disconnected, or "unknown" before the first event.
	Status string

	// StatusSince is when Status last changed.
	StatusSince time.Time

	// LastMessage is when any Waku message last arrived — including traffic
	// from other applications on the shard, which we cannot decrypt but which
	// still proves the subscription is live.
	LastMessage time.Time

	// LastAnnounce is when we last successfully opened a peer's announce.
	LastAnnounce time.Time

	// Topics is how many content topics we hold subscriptions for.
	Topics int
}

// OK reports whether the rendezvous plane looks usable.
func (h Health) OK(now time.Time) bool {
	if h.Status == "Disconnected" {
		return false
	}
	// Never having received anything is only a problem once there has been time
	// for it; the caller decides that by not asking too early.
	return !h.LastMessage.IsZero() && now.Sub(h.LastMessage) < RendezvousStale
}

// Problem returns a short human explanation when the rendezvous plane is
// unhealthy, or "" when it is fine.
//
// Phrased to say what it means rather than what was observed: "no fleet peers"
// rather than "connectionStatus=Disconnected".
func (h Health) Problem(now time.Time) string {
	switch {
	case h.Status == "Disconnected":
		return "not connected to any logos.dev peers — discovery is down, existing tunnels are unaffected"
	case h.Topics == 0:
		return "no rendezvous subscriptions"
	case h.LastMessage.IsZero():
		return "connected, but nothing has arrived yet — still joining the shard"
	case now.Sub(h.LastMessage) >= RendezvousStale:
		return "nothing received for " + now.Sub(h.LastMessage).Round(time.Second).String() + " — the shard may be unreachable"
	}
	return ""
}

// health is the mutable half, updated from the event loop.
type health struct {
	mu sync.Mutex
	h  Health
}

func newHealth() *health {
	return &health{h: Health{Status: "unknown"}}
}

// observe folds a raw Waku event into the health record.
func (s *health) observe(ev waku.Event, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch waku.EventType(ev.JSON) {
	case waku.EventMessageReceived:
		s.h.LastMessage = now

	case waku.EventConnectionStatus:
		var e struct {
			ConnectionStatus string `json:"connectionStatus"`
		}
		if json.Unmarshal([]byte(ev.JSON), &e) == nil && e.ConnectionStatus != "" {
			if e.ConnectionStatus != s.h.Status {
				s.h.Status = e.ConnectionStatus
				s.h.StatusSince = now
			}
		}
	}
}

func (s *health) announceOpened(now time.Time) {
	s.mu.Lock()
	s.h.LastAnnounce = now
	s.mu.Unlock()
}

func (s *health) setTopics(n int) {
	s.mu.Lock()
	s.h.Topics = n
	s.mu.Unlock()
}

func (s *health) snapshot() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.h
}

// Health reports the state of the rendezvous plane.
func (m *Mesh) Health() Health { return m.health.snapshot() }
