package agent

// Forecasts: at the pace a limit is being used, does it last until it
// renews? For each Claude plan window, from the readings Claude Code reports
// after every turn; for each Venice key, from the balances asked every few
// minutes (credits.go).
//
// The pace is measured up to now, not to the last reading: hours with
// nothing used count, so a quiet evening brings the forecast down. Recent
// use first — the last hour of a 5-hour window, the last day of a 7-day one,
// the last two hours of a Venice day — because that is the pace that is
// still going on; without enough of it, the average since the window began.

import (
	"sort"
	"strings"
	"time"
)

// windowLength is how long a plan window runs, by Claude Code's name for it.
func windowLength(name string) time.Duration {
	switch {
	case name == "five_hour":
		return 5 * time.Hour
	case strings.HasPrefix(name, "seven_day"):
		return 7 * 24 * time.Hour
	}
	return 0
}

// paceLookback is how far back "recent" reaches in a window of length n.
func paceLookback(n time.Duration) time.Duration {
	if n <= 5*time.Hour {
		return time.Hour
	}
	return 24 * time.Hour
}

// keepReadings drops readings older than the longest window, plus a day.
func keepReadings(rs []*Limits, now time.Time) []*Limits {
	cut := now.Add(-8 * 24 * time.Hour)
	i := 0
	for i < len(rs) && rs[i].At.Before(cut) {
		i++
	}
	return rs[i:]
}

// forecast is l with each window's projection, from the readings (any order,
// any machine's sessions).
func forecast(l *Limits, readings []*Limits, now time.Time) *Limits {
	out := *l
	out.Windows = map[string]LimitWindow{}
	sorted := append([]*Limits(nil), readings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	for name, w := range l.Windows {
		out.Windows[name] = projectWindow(name, w, sorted, now)
	}
	return &out
}

func projectWindow(name string, w LimitWindow, readings []*Limits, now time.Time) LimitWindow {
	length := windowLength(name)
	if length == 0 || !w.ResetsAt.After(now) {
		return w // unknown length, or renewed since (the apps say so)
	}
	start := w.ResetsAt.Add(-length)
	// The earliest reading of this same window (same reset) within the
	// lookback: the pace from it to now.
	since := now.Add(-paceLookback(length))
	var first *LimitWindow
	var firstAt time.Time
	for _, r := range readings {
		rw, ok := r.Windows[name]
		if !ok || r.At.Before(since) || r.At.After(now) || absDur(rw.ResetsAt.Sub(w.ResetsAt)) > time.Minute {
			continue
		}
		first, firstAt = &rw, r.At
		break
	}
	var rate float64 // share per second
	switch span := now.Sub(firstAt); {
	case first != nil && span >= paceLookback(length)/4:
		rate = (w.Utilization - first.Utilization) / span.Seconds()
		w.Pace = "recent"
	case now.Sub(start) >= length/50:
		rate = w.Utilization / now.Sub(start).Seconds()
		w.Pace = "window"
	default:
		return w // too early in the window to say
	}
	if rate < 0 {
		rate = 0
	}
	left := w.ResetsAt.Sub(now).Seconds()
	w.Projected = w.Utilization + rate*left
	if w.Utilization < 1 && w.Projected > 1 && rate > 0 {
		at := now.Add(time.Duration((1 - w.Utilization) / rate * float64(time.Second))).Round(time.Second)
		w.RunsOutAt = &at
	}
	return w
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// creditPoint is one balance reading of a key.
type creditPoint struct {
	at       time.Time
	diem     float64
	resetsAt time.Time
}

// creditLookback is how far back a Venice key's pace reaches.
const creditLookback = 2 * time.Hour

// projectCredit adds to c, from its key's readings (oldest first), when the
// daily allowance runs out at the recent pace — if before it refills — and
// how much is left when it does.
func projectCredit(c Credit, points []creditPoint, now time.Time) Credit {
	diem, ok := c.Balances["DIEM"]
	if !ok || c.Error != "" || !c.ResetsAt.After(now) {
		return c
	}
	var first *creditPoint
	for i := range points {
		p := &points[i]
		if p.at.Before(now.Add(-creditLookback)) || !p.resetsAt.Equal(c.ResetsAt) {
			continue
		}
		first = p
		break
	}
	if first == nil || now.Sub(first.at) < 20*time.Minute {
		return c // not enough of today's readings yet
	}
	rate := (first.diem - diem) / now.Sub(first.at).Seconds() // DIEM per second
	if rate < 0 {
		rate = 0
	}
	left := diem - rate*c.ResetsAt.Sub(now).Seconds()
	if left < 0 {
		left = 0
		at := now.Add(time.Duration(diem / rate * float64(time.Second))).Round(time.Second)
		c.RunsOutAt = &at
	}
	c.LeftAtRefill = &left
	return c
}
