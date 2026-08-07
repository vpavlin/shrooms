package mesh

import (
	"encoding/json"
	"fmt"
	"strings"
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

// ShortLived is how quickly a connection must die to count as churn rather than
// as a normal disconnect.
//
// A healthy session lasts minutes. A peer dropped within a second of connecting
// did not fail — it refused, and by far the most common reason is a metadata
// disagreement, which is exactly what a preset or cluster mismatch looks like
// from here.
const ShortLived = 2 * time.Second

// ChurnThreshold is how many short-lived connections before we call it. More
// than a couple is no longer coincidence.
const ChurnThreshold = 3

// Health is what the node knows about its own connection to the rendezvous
// plane, as opposed to the data plane.
//
// This exists because the two fail independently and look identical from the
// outside. When the fleet is unreachable, existing tunnels keep working — by
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

	// Churn counts peers that connected and were dropped again almost
	// immediately. See ShortLived.
	Churn int
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

// Problem states the condition, and only the condition. It returns "" when the
// rendezvous plane is fine.
//
// Deliberately says what it means rather than what was observed — "not
// connected to any fleet peers", not "connectionStatus=Disconnected" — and
// deliberately says nothing about consequences. The consequence is the same
// whatever the cause, so the caller states it once instead of every branch
// repeating it.
func (h Health) Problem(now time.Time) string {
	switch {
	// Checked before the plain disconnected case: same symptom, far more useful
	// cause, and the one nothing else surfaces — the disconnect reason never
	// leaves nwaku's own log.
	case h.Status == "Disconnected" && h.Churn >= ChurnThreshold:
		return "peers connect and are dropped immediately — usually a preset or cluster_id mismatch"
	case h.Status == "Disconnected":
		return "not connected to any fleet peers"
	case h.Topics == 0:
		return "not subscribed to any topics"
	case h.LastMessage.IsZero():
		return "nothing has arrived yet"
	case now.Sub(h.LastMessage) >= RendezvousStale:
		return "nothing received for " + now.Sub(h.LastMessage).Round(time.Second).String()
	}
	return ""
}

// Detail is the supporting evidence for Problem, as a short parenthetical.
//
// Only carries what Problem has not already said: repeating "Disconnected"
// after "not connected to any peers" is noise, and noise is what makes people
// stop reading warnings.
func (h Health) Detail(now time.Time) string {
	var parts []string
	// Each field is omitted when Problem has already conveyed it.
	if h.Status != "Disconnected" && h.Status != "unknown" {
		parts = append(parts, h.Status)
	}
	if h.Topics > 0 {
		parts = append(parts, fmt.Sprintf("%d topics", h.Topics))
	}
	if !h.LastMessage.IsZero() && now.Sub(h.LastMessage) < RendezvousStale {
		parts = append(parts, "last message "+now.Sub(h.LastMessage).Round(time.Second).String()+" ago")
	}
	return strings.Join(parts, ", ")
}

// maxTrackedPeers bounds the connect-time map. Discovery churns through many
// peers, and this is a diagnostic, not an accounting record.
const maxTrackedPeers = 256

// health is the mutable half, updated from the event loop.
type health struct {
	mu sync.Mutex
	h  Health

	// connectedAt is when each peer's current connection began, so a later
	// disconnect can be classified as churn or as a normal drop.
	connectedAt map[string]time.Time
}

func newHealth() *health {
	return &health{
		h:           Health{Status: "unknown"},
		connectedAt: make(map[string]time.Time),
	}
}

// observe folds a raw Waku event into the health record.
func (s *health) observe(ev waku.Event, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch waku.EventType(ev.JSON) {
	case waku.EventMessageReceived:
		s.h.LastMessage = now

	case waku.EventConnectionChange:
		// The disconnect REASON is not in the event stream — it only reaches
		// nwaku's own log. But a peer dropped within ShortLived of connecting
		// is a refusal, and counting those recovers the diagnosis without it.
		var e struct {
			PeerID    string `json:"peerId"`
			PeerEvent string `json:"peerEvent"`
		}
		if json.Unmarshal([]byte(ev.JSON), &e) != nil || e.PeerID == "" {
			return
		}
		switch e.PeerEvent {
		case "EventConnected":
			if len(s.connectedAt) < maxTrackedPeers {
				s.connectedAt[e.PeerID] = now
			}
		case "EventDisconnected":
			if at, ok := s.connectedAt[e.PeerID]; ok {
				if now.Sub(at) < ShortLived {
					s.h.Churn++
				}
				delete(s.connectedAt, e.PeerID)
			}
		}

	case waku.EventConnectionStatus:
		var e struct {
			ConnectionStatus string `json:"connectionStatus"`
		}
		if json.Unmarshal([]byte(ev.JSON), &e) == nil && e.ConnectionStatus != "" {
			if e.ConnectionStatus != s.h.Status {
				s.h.Status = e.ConnectionStatus
				s.h.StatusSince = now
			}
			// Reaching Connected disproves the mismatch theory: whatever churn
			// happened, some peer accepted us. Without this reset the warning
			// would persist on a node that recovered.
			if e.ConnectionStatus == "Connected" {
				s.h.Churn = 0
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
