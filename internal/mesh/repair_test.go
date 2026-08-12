package mesh

import (
	"log/slog"
	"testing"
	"time"
)

// Subscriptions live in the delivery library; m.subscribed is only our belief
// about them. A node that drops and returns has lost them while our map still
// says otherwise, so nothing is ever received again and the mesh decays until
// the process restarts. These are the moments that must trigger a repair.
func TestNeedsRendezvousRepair(t *testing.T) {
	now := time.Now()
	fresh := Health{Status: "Connected", LastMessage: now.Add(-time.Second)}
	stale := Health{Status: "Connected", LastMessage: now.Add(-RendezvousStale - time.Second)}
	down := Health{Status: "Disconnected", LastMessage: now.Add(-time.Second)}

	cases := []struct {
		name   string
		h      Health
		lastOK bool
		want   bool
		why    string
	}{
		// The ordinary reconnect: the edge from unhealthy to healthy.
		{"recovered", fresh, false, true, "reconnected"},

		// Steady state must not resubscribe on every tick; that would be a
		// subscribe storm against the fleet.
		{"healthy all along", fresh, true, false, ""},

		// Connected by the library's account, yet nothing arrives. This is the
		// case the library never reports and the one that stranded the mesh.
		{"silent while nominally up", stale, true, true, "silent for too long"},
		// And it must fire even when the previous verdict was already false —
		// Health.OK is false BECAUSE nothing arrives, so waiting for a recovery
		// edge waits on a message the missing subscription prevents. That
		// circle is the bug.
		{"silent, and was already unhealthy", stale, false, true, "silent for too long"},

		// While it is down there is nothing to subscribe into; the recovery
		// edge will fire when it returns.
		{"still down", down, true, false, ""},
		{"still down, was down", down, false, false, ""},
	}

	for _, c := range cases {
		why, need := needsRendezvousRepair(c.h, c.lastOK, now)
		if need != c.want {
			t.Errorf("%s: need = %v, want %v", c.name, need, c.want)
		}
		if need && why != c.why {
			t.Errorf("%s: why = %q, want %q", c.name, why, c.why)
		}
	}
}

// A node that has never received anything must not be called silent — it has
// simply not started yet, and repairing on a zero timestamp would fire on every
// tick before the first message ever arrives.
func TestNoRepairBeforeTheFirstMessage(t *testing.T) {
	now := time.Now()
	h := Health{Status: "Connected"} // LastMessage is the zero time
	if _, need := needsRendezvousRepair(h, true, now); need {
		t.Error("repaired before any message had ever arrived")
	}
}

// A one-off correction is what the drift check exists for, so the first
// attempts must still pull the endpoint back.
func TestRoamIsCorrectedBeforeItIsConceded(t *testing.T) {
	m := &Mesh{log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	for i := 1; i < RoamFights; i++ {
		if m.yieldRoam("peer", "phone", now) {
			t.Fatalf("conceded after %d correction(s), wanted %d", i, RoamFights)
		}
		now = now.Add(10 * time.Second)
	}
	if !m.yieldRoam("peer", "phone", now) {
		t.Error("never conceded, which is the rewrite-every-few-seconds loop")
	}
}

// And while the truce holds, nothing is rewritten at all.
func TestATruceIsKept(t *testing.T) {
	m := &Mesh{log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	for i := 0; i < RoamFights; i++ {
		m.yieldRoam("peer", "phone", now)
		now = now.Add(time.Second)
	}
	if !m.yieldRoam("peer", "phone", now.Add(RoamTruce/2)) {
		t.Error("started correcting again inside the truce")
	}
	// Once it expires, a peer that has settled is corrected again if it needs
	// to be: the concession is temporary, not a decision about that peer.
	if m.yieldRoam("peer", "phone", now.Add(2*RoamTruce)) {
		t.Error("the truce never expired")
	}
}

// An occasional correction must not accumulate into a concession: three drifts
// an hour apart are three one-offs, not a losing fight.
func TestOccasionalCorrectionsDoNotAccumulate(t *testing.T) {
	m := &Mesh{log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	for i := 0; i < 5; i++ {
		if m.yieldRoam("peer", "vps", now) {
			t.Fatal("conceded to a peer that drifts once an hour")
		}
		now = now.Add(time.Hour)
	}
}
