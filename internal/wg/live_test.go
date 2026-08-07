package wg

import (
	"testing"
	"time"
)

// A peer that handshaked once and then vanished must not read as connected.
// This is the state you are in when you go looking at status, so it is exactly
// when a false "up" does the most damage.
func TestStaleHandshakeIsNotLive(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name string
		age  time.Duration
		live bool
	}{
		{"fresh", 5 * time.Second, true},
		{"one rekey ago", 100 * time.Second, true},
		{"rekey just triggered", 166 * time.Second, true},
		// The band that used to produce false alarms: rekeying starts at 165s,
		// so a healthy tunnel sits here regularly.
		{"mid-rekey", RejectAfter + time.Second, true},
		{"still retrying", RejectAfter + RekeyAttemptTime - time.Second, true},
		{"past every retry", LiveWindow + time.Second, false},
		{"long gone", time.Hour, false},
	}

	for _, c := range cases {
		p := PeerStat{LastHandshake: now.Add(-c.age)}
		if !p.Handshaked() {
			t.Fatalf("%s: Handshaked() false for a non-zero handshake", c.name)
		}
		if got := p.Live(now); got != c.live {
			t.Errorf("%s (%s old): Live = %v, want %v", c.name, c.age, got, c.live)
		}
	}
}

func TestNeverHandshakedIsNotLive(t *testing.T) {
	var p PeerStat
	if p.Handshaked() {
		t.Error("a zero handshake time reported as handshaked")
	}
	if p.Live(time.Now()) {
		t.Error("a peer that never handshaked reported as live")
	}
}
