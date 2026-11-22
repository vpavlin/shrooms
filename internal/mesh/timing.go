package mesh

import (
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/wg"
)

// Milestones are the stages a peer passes through from nothing to a working
// tunnel. Each is recorded once, the first time it happens.
//
// This exists because "it connected eventually" is not a measurement, and the
// three stages fail for entirely different reasons: Discovered is the
// rendezvous plane, PathConfirmed is NAT traversal, Tunnel is WireGuard. A
// single end-to-end number cannot tell you which of the three is slow, and that
// is exactly what you need to know.
type Milestones struct {
	// Discovered is when the peer's first announce was accepted.
	Discovered time.Time
	// PathConfirmed is when an address of theirs first answered a probe.
	PathConfirmed time.Time
	// Tunnel is when a WireGuard handshake first completed.
	Tunnel time.Time
}

// timings records milestones per peer, relative to daemon start.
type timings struct {
	mu      sync.Mutex
	started time.Time
	peers   map[string]*Milestones
}

func newTimings(started time.Time) *timings {
	return &timings{started: started, peers: make(map[string]*Milestones)}
}

func (t *timings) get(id string) *Milestones {
	m, ok := t.peers[id]
	if !ok {
		m = &Milestones{}
		t.peers[id] = m
	}
	return m
}

// mark records a milestone the first time it occurs and reports whether this
// call was the first. Later calls are ignored, so a peer that reconnects does
// not overwrite how long it originally took.
func (t *timings) mark(id string, field func(*Milestones) *time.Time, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	p := field(t.get(id))
	if !p.IsZero() {
		return false
	}
	*p = now
	return true
}

// snapshot returns a copy of a peer's milestones.
func (t *timings) snapshot(id string) Milestones {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m, ok := t.peers[id]; ok {
		return *m
	}
	return Milestones{}
}

func (t *timings) forget(id string) {
	t.mu.Lock()
	delete(t.peers, id)
	t.mu.Unlock()
}

// Since reports how long after daemon start a milestone happened. Zero time
// means the milestone has not been reached.
func (t *timings) since(at time.Time) time.Duration {
	if at.IsZero() {
		return 0
	}
	return at.Sub(t.started)
}

// Timing is the reportable form for one peer.
type Timing struct {
	DiscoveredAfter time.Duration
	PathAfter       time.Duration
	TunnelAfter     time.Duration
}

// Timing reports how long each stage took for a peer, measured from when this
// daemon started.
func (m *Mesh) Timing(id string) Timing {
	s := m.timing.snapshot(id)
	return Timing{
		DiscoveredAfter: m.timing.since(s.Discovered),
		PathAfter:       m.timing.since(s.PathConfirmed),
		TunnelAfter:     m.timing.since(s.Tunnel),
	}
}

// fromMemory reports whether the tunnel came up before this peer's first
// announce — which means WireGuard used an endpoint the last run wrote down.
//
// Zero means "has not happened", so no announce at all is a memory hit, and a
// tunnel that has not happened is not one.
func fromMemory(t Timing) bool {
	if t.TunnelAfter <= 0 {
		return false
	}
	return t.DiscoveredAfter <= 0 || t.TunnelAfter < t.DiscoveredAfter
}

// checkTunnels notices the first completed handshake for each peer and reports
// the full breakdown once.
//
// Polled rather than event-driven because wireguard-go does not tell us when a
// handshake completes; the UAPI is the only source. stats is the tick's one
// read of it, shared with sampleRates.
func (m *Mesh) checkTunnels(stats map[string]wg.PeerStat) {
	for _, p := range m.roster.Peers() {
		st, ok := stats[p.WGPub.String()]
		if !ok || !st.Handshaked() {
			continue
		}
		if !m.timing.mark(p.ID(), func(x *Milestones) *time.Time { return &x.Tunnel }, st.LastHandshake) {
			continue
		}
		t := m.Timing(p.ID())
		// Unreached milestones are omitted rather than logged as zero. A zero
		// here means "has not happened", and printing it as 0s read as
		// "happened instantly" — which is wrong in the one case worth
		// noticing, below.
		args := []any{"peer", p.Name, "after", t.TunnelAfter.Round(time.Millisecond)}
		if t.DiscoveredAfter > 0 {
			args = append(args, "discovered_after", t.DiscoveredAfter.Round(time.Millisecond))
		}
		// A tunnel before the first announce: this peer came from the
		// remembered roster and WireGuard reached it on a stored endpoint while
		// the rendezvous plane was still coming up. That is the whole point of
		// remembering, and it used to be impossible — no announce meant no peer
		// meant nothing to handshake with.
		//
		// Decided by comparing WHEN each happened, not by whether discovery has
		// happened by the time this poll runs. Handshakes are noticed by
		// polling the UAPI every couple of seconds, so a tunnel that came up at
		// 22ms is commonly first seen after an announce that arrived at 1.3s —
		// and this then reported a memory hit as an announce-driven connection.
		// The end-to-end suite caught it on 2026-09-18: node B restarted,
		// handshook on the remembered endpoint in 22ms, and was recorded as
		// having waited for discovery it had beaten by more than a second.
		if fromMemory(t) {
			args = append(args, "from", "memory")
		}
		if t.PathAfter > 0 {
			args = append(args, "path_after", t.PathAfter.Round(time.Millisecond))
		}
		args = append(args, "via", st.Endpoint)
		m.log.Info("tunnel established", args...)
	}
}
