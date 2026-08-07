package mesh

import (
	"testing"
	"time"
)

func discovered(m *Milestones) *time.Time { return &m.Discovered }
func tunnelAt(m *Milestones) *time.Time   { return &m.Tunnel }

func TestMilestonesRecordedOnce(t *testing.T) {
	start := time.Now()
	tm := newTimings(start)

	if !tm.mark("peer", discovered, start.Add(3*time.Second)) {
		t.Fatal("first mark did not report as first")
	}
	// A peer that reconnects must not overwrite how long it originally took —
	// otherwise the number silently becomes "time since the last blip".
	if tm.mark("peer", discovered, start.Add(90*time.Second)) {
		t.Error("second mark reported as first")
	}
	if got := tm.since(tm.snapshot("peer").Discovered); got != 3*time.Second {
		t.Errorf("discovered after %s, want 3s", got)
	}
}

// An unreached milestone must read as absent, not as zero. Reporting a peer
// that never connected as "connected in 0s" is worse than reporting nothing.
func TestUnreachedMilestoneIsZero(t *testing.T) {
	tm := newTimings(time.Now())
	tm.mark("peer", discovered, time.Now())

	if got := tm.since(tm.snapshot("peer").Tunnel); got != 0 {
		t.Errorf("unreached milestone = %s, want 0", got)
	}
	if got := tm.since(tm.snapshot("nobody").Discovered); got != 0 {
		t.Errorf("unknown peer = %s, want 0", got)
	}
}

// The three stages are separate because they fail for separate reasons.
func TestStagesRecordedIndependently(t *testing.T) {
	start := time.Now()
	tm := newTimings(start)

	tm.mark("p", discovered, start.Add(2*time.Second))
	tm.mark("p", func(m *Milestones) *time.Time { return &m.PathConfirmed }, start.Add(5*time.Second))
	tm.mark("p", tunnelAt, start.Add(11*time.Second))

	s := tm.snapshot("p")
	for _, c := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"discovered", tm.since(s.Discovered), 2 * time.Second},
		{"path", tm.since(s.PathConfirmed), 5 * time.Second},
		{"tunnel", tm.since(s.Tunnel), 11 * time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestForgetClearsTiming(t *testing.T) {
	tm := newTimings(time.Now())
	tm.mark("p", discovered, time.Now())
	tm.forget("p")
	if got := tm.since(tm.snapshot("p").Discovered); got != 0 {
		t.Errorf("timing survived forget: %s", got)
	}
}
