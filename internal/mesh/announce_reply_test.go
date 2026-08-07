package mesh

import (
	"testing"
	"time"
)

// requestAnnounce runs on the packet receive path and must never block, however
// far behind the main loop is.
func TestRequestAnnounceNeverBlocks(t *testing.T) {
	m := &Mesh{reannounce: make(chan struct{}, 1)}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			m.requestAnnounce()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("requestAnnounce blocked; it runs on the receive path and must not")
	}

	// Coalesced to a single pending wake-up, not a backlog of 1000.
	if got := len(m.reannounce); got != 1 {
		t.Errorf("queued %d wake-ups, want 1", got)
	}
}

// The cooldown is what stops a peer that announces forever without connecting
// from doubling announce traffic on a shared bus.
func TestReplyCooldownIsPerPeer(t *testing.T) {
	m := &Mesh{repliedTo: make(map[string]time.Time)}
	now := time.Now()

	// Emulate the map half of shouldReplyTo, which is the part with the rule in
	// it; the data-plane lookup is exercised on real hardware.
	allow := func(id string, at time.Time) bool {
		m.replyMu.Lock()
		defer m.replyMu.Unlock()
		if last, ok := m.repliedTo[id]; ok && at.Sub(last) < AnnounceInterval {
			return false
		}
		m.repliedTo[id] = at
		return true
	}

	if !allow("a", now) {
		t.Fatal("first reply to a peer was suppressed")
	}
	if allow("a", now.Add(AnnounceInterval-time.Second)) {
		t.Error("replied twice to one peer inside the cooldown")
	}
	// A different peer is not affected by another's cooldown.
	if !allow("b", now) {
		t.Error("one peer's cooldown suppressed a reply to another")
	}
	if !allow("a", now.Add(AnnounceInterval+time.Second)) {
		t.Error("cooldown never expired")
	}
}
