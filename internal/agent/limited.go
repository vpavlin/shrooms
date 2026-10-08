package agent

// Which sessions cannot work now for want of quota: a Claude Code session when
// the machine's subscription has reached a limit, a pi session when the
// pay-as-you-go key its provider uses has nothing left today. Said on each
// session in the list, with when it comes back, so the apps can set those
// sessions apart instead of the owner finding out by sending a message.

import (
	"slices"
	"strings"
	"time"
)

// Limited is why a session cannot work now, and until when.
type Limited struct {
	Reason string     `json:"reason"`          // "limit reached (5 hours)", "no DIEM left today (venice)"
	Until  *time.Time `json:"until,omitempty"` // when it comes back, if known
}

// windowWords names a plan window as the apps do.
func windowWords(name string) string {
	switch {
	case name == "five_hour":
		return "5 hours"
	case name == "seven_day":
		return "7 days"
	case strings.HasPrefix(name, "seven_day_"):
		return "7 days, " + strings.TrimPrefix(name, "seven_day_")
	}
	return name
}

// planLimited: the subscription refuses, and the window that refuses has not
// reset since. A reading whose window has reset is old news: the limit is
// gone, whatever it said.
func planLimited(l *Limits, now time.Time) *Limited {
	if l == nil || l.Status != "rejected" || l.Overage {
		return nil
	}
	w, ok := l.Windows[l.Window]
	if !ok {
		// No window named: the latest reset among the full ones.
		var until time.Time
		for name, x := range l.Windows {
			if x.Utilization >= 1 && x.ResetsAt.After(until) {
				until, w, ok = x.ResetsAt, x, true
				l = &Limits{Window: name, Windows: l.Windows}
			}
		}
		if !ok {
			return &Limited{Reason: "limit reached"}
		}
	}
	if !w.ResetsAt.After(now) {
		return nil
	}
	until := w.ResetsAt
	return &Limited{Reason: "limit reached (" + windowWords(l.Window) + ")", Until: &until}
}

// creditLimited: the key of a pi session's provider has no daily allowance
// left, and no balance to fall back on.
func creditLimited(provider string, credits []Credit, now time.Time) *Limited {
	for _, c := range credits {
		if c.Error != "" || !slices.Contains(c.Names, provider) {
			continue
		}
		if c.Balances["DIEM"] > 0.001 || c.Balances["USD"] > 0.001 {
			return nil
		}
		if !c.ResetsAt.IsZero() && !c.ResetsAt.After(now) {
			return nil // refilled since the reading
		}
		l := &Limited{Reason: "no DIEM left today (" + provider + ")"}
		if !c.ResetsAt.IsZero() {
			until := c.ResetsAt
			l.Until = &until
		}
		return l
	}
	return nil
}

// limitedFor: whether a session of this harness and model cannot work now.
func limitedFor(harness, model string, plan *Limits, credits []Credit, now time.Time) *Limited {
	switch harness {
	case "claude":
		return planLimited(plan, now)
	case "pi":
		provider, _, ok := strings.Cut(model, "/")
		if !ok {
			return nil
		}
		return creditLimited(provider, credits, now)
	}
	return nil
}
