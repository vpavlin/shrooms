package agent

import (
	"strings"
	"testing"
	"time"
)

func TestASessionOutOfQuotaIsSaidToBe(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	back := now.Add(2 * time.Hour)
	rejected := &Limits{Status: "rejected", Window: "five_hour", Windows: map[string]LimitWindow{
		"five_hour": {Utilization: 1, ResetsAt: back}, "seven_day": {Utilization: 0.6, ResetsAt: now.Add(72 * time.Hour)}}}

	l := limitedFor("claude", "claude-opus-5", rejected, nil, now)
	if l == nil || l.Until == nil || !l.Until.Equal(back) || l.Reason != "limit reached (5 hours)" {
		t.Fatalf("a Claude Code session on a machine at its limit: %+v", l)
	}
	if l := limitedFor("claude", "", rejected, nil, back.Add(time.Minute)); l != nil {
		t.Errorf("still limited after the window reset: %+v", l)
	}
	allowed := &Limits{Status: "allowed_warning", Window: "seven_day", Windows: rejected.Windows}
	if l := limitedFor("claude", "", allowed, nil, now); l != nil {
		t.Errorf("limited while allowed: %+v", l)
	}
	overage := *rejected
	overage.Overage = true
	if l := limitedFor("claude", "", &overage, nil, now); l != nil {
		t.Errorf("limited while running on overage: %+v", l)
	}
	// The plan is Claude Code's: a pi session is not held by it.
	if l := limitedFor("pi", "venice/glm-5", rejected, nil, now); l != nil {
		t.Errorf("a pi session held by Claude Code's limit: %+v", l)
	}

	refill := now.Add(12 * time.Hour)
	spent := []Credit{{Provider: "venice", Names: []string{"venice"}, Balances: map[string]float64{"DIEM": 0, "USD": -0.03}, ResetsAt: refill}}
	l = limitedFor("pi", "venice/glm-5", nil, spent, now)
	if l == nil || l.Until == nil || !l.Until.Equal(refill) || !strings.Contains(l.Reason, "no DIEM left today") {
		t.Fatalf("a pi session whose key is spent: %+v", l)
	}
	if l := limitedFor("pi", "ollama/qwen3", nil, spent, now); l != nil {
		t.Errorf("a session of another provider held by the spent key: %+v", l)
	}
	left := []Credit{{Provider: "venice", Names: []string{"venice"}, Balances: map[string]float64{"DIEM": 2.5}, ResetsAt: refill}}
	if l := limitedFor("pi", "venice/glm-5", nil, left, now); l != nil {
		t.Errorf("limited with allowance left: %+v", l)
	}
	if l := limitedFor("pi", "venice/glm-5", nil, spent, refill.Add(time.Minute)); l != nil {
		t.Errorf("still limited after the refill: %+v", l)
	}
}
