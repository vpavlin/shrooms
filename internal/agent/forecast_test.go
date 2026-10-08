package agent

import (
	"math"
	"testing"
	"time"
)

func reading(at time.Time, name string, u float64, reset time.Time) *Limits {
	return &Limits{At: at, Windows: map[string]LimitWindow{name: {Utilization: u, ResetsAt: reset}}}
}

// A plan window's forecast: from the recent pace, measured to now; from the
// average since the window began when there is too little history; nothing
// for a window renewed since or of unknown length; readings of an earlier
// window of the same name are not its history.
func TestAWindowIsForecastFromItsPace(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

	// 0.30 forty minutes ago, 0.50 now: 0.2 per 40 min, three more of those
	// before the reset — 1.1, out 100 minutes from now.
	l := reading(now, "five_hour", 0.5, reset)
	f := forecast(l, []*Limits{reading(now.Add(-40*time.Minute), "five_hour", 0.3, reset), l}, now).Windows["five_hour"]
	if f.Pace != "recent" || !near(f.Projected, 1.1) || f.RunsOutAt == nil || !f.RunsOutAt.Equal(now.Add(100*time.Minute)) {
		t.Fatalf("fast: %+v", f)
	}
	// The same 0.5, reached 40 minutes ago and nothing since: the idle time
	// counts, and it lasts.
	f = forecast(reading(now.Add(-40*time.Minute), "five_hour", 0.5, reset),
		[]*Limits{reading(now.Add(-40*time.Minute), "five_hour", 0.5, reset)}, now).Windows["five_hour"]
	if f.RunsOutAt != nil || !near(f.Projected, 0.5) {
		t.Fatalf("idle: %+v", f)
	}
	// No reading in the last hour but this one: the window's average. Three
	// hours in, 0.3 — 0.1 an hour, two hours left: 0.5.
	f = forecast(reading(now, "five_hour", 0.3, reset), nil, now).Windows["five_hour"]
	if f.Pace != "window" || !near(f.Projected, 0.5) || f.RunsOutAt != nil {
		t.Fatalf("window pace: %+v", f)
	}
	// An earlier window's reading is no history of this one.
	old := reading(now.Add(-30*time.Minute), "five_hour", 0.9, reset.Add(-5*time.Hour))
	f = forecast(reading(now, "five_hour", 0.3, reset), []*Limits{old}, now).Windows["five_hour"]
	if f.Pace != "window" {
		t.Fatalf("took another window's reading: %+v", f)
	}
	// Renewed since, or a window we do not know the length of: as it was.
	f = forecast(reading(now.Add(-3*time.Hour), "five_hour", 0.8, now.Add(-time.Minute)), nil, now).Windows["five_hour"]
	if f.Pace != "" || f.Projected != 0 {
		t.Fatalf("renewed: %+v", f)
	}
	if f := forecast(reading(now, "monthly", 0.5, reset), nil, now).Windows["monthly"]; f.Pace != "" {
		t.Fatalf("unknown window: %+v", f)
	}
	// The 7-day window looks back a day.
	week := now.Add(3 * 24 * time.Hour)
	f = forecast(reading(now, "seven_day", 0.6, week),
		[]*Limits{reading(now.Add(-10*time.Hour), "seven_day", 0.5, week)}, now).Windows["seven_day"]
	// 0.1 per 10 h; 0.4 left goes in 40 h, before the 72 h to the reset.
	if f.Pace != "recent" || f.RunsOutAt == nil || !f.RunsOutAt.Equal(now.Add(40*time.Hour)) {
		t.Fatalf("week: %+v", f)
	}
}

// A Venice key: at the last two hours' pace, out before the refill, or this
// much left at it; nothing from too little of today.
func TestAKeysCreditIsForecast(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	refill := now.Add(10 * time.Hour)
	c := Credit{Balances: map[string]float64{"DIEM": 4}, ResetsAt: refill, At: now}
	pts := []creditPoint{{now.Add(-2 * time.Hour), 6, refill}, {now, 4, refill}}
	got := projectCredit(c, pts, now) // 1 an hour: out in 4 h
	if got.RunsOutAt == nil || !got.RunsOutAt.Equal(now.Add(4*time.Hour)) || got.LeftAtRefill == nil || *got.LeftAtRefill != 0 {
		t.Fatalf("fast: %+v", got)
	}
	slow := []creditPoint{{now.Add(-2 * time.Hour), 4.2, refill}, {now, 4, refill}}
	got = projectCredit(c, slow, now) // 0.1 an hour: 3 left at the refill
	if got.RunsOutAt != nil || got.LeftAtRefill == nil || math.Abs(*got.LeftAtRefill-3) > 1e-6 {
		t.Fatalf("slow: %+v", got)
	}
	yesterday := []creditPoint{{now.Add(-time.Hour), 9, refill.Add(-24 * time.Hour)}, {now, 4, refill}}
	if got = projectCredit(c, yesterday, now); got.LeftAtRefill != nil {
		t.Fatalf("used yesterday's balance: %+v", got)
	}
}
