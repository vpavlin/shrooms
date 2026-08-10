package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
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

// A Fresh announce must draw a reply even when we believe the tunnel is fine.
//
// This is the case the first version got wrong: after a peer restarts, our
// session with it stays valid for REJECT_AFTER_TIME, so a tunnel-only rule
// stays silent for minutes — through exactly the window the restarted node is
// waiting in.
func TestFreshAnnounceSurvivesWireRoundTrip(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	now := time.Now()

	a := newAnnounce(t, id, "laptop", []string{"203.0.113.9:51820"}, 1)
	a.Fresh = true

	sealed, err := control.Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := control.OpenAnnounce(nk, 1, sealed, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !got.Fresh {
		t.Error("the fresh flag did not survive the wire; peers would never be asked")
	}
}

// A steady-state announce must not carry it, or every node asks for
// introductions forever and the reply rule is meaningless.
func TestSteadyStateAnnounceIsNotFresh(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()

	a := newAnnounce(t, id, "laptop", nil, 7)
	sealed, _ := control.Seal(nk, 1, id.DevicePriv, a)
	got, err := control.OpenAnnounce(nk, 1, sealed, time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.Fresh {
		t.Error("a default announce claimed to be fresh")
	}
}
