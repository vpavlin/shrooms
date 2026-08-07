package mesh

import (
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/logos-vpn/internal/waku"
)

func TestHealthTracksConnectionStatus(t *testing.T) {
	h := newHealth()
	now := time.Now()

	if got := h.snapshot().Status; got != "unknown" {
		t.Errorf("initial status = %q, want unknown", got)
	}

	h.observe(waku.Event{JSON: `{"eventType":"connection_status_change","connectionStatus":"Disconnected"}`}, now)
	if got := h.snapshot().Status; got != "Disconnected" {
		t.Errorf("status = %q, want Disconnected", got)
	}
	if h.snapshot().OK(now) {
		t.Error("a disconnected node reported healthy rendezvous")
	}
}

// Undecryptable traffic from other applications on the shard still proves the
// subscription is live, so it must count.
func TestHealthCountsForeignTraffic(t *testing.T) {
	h := newHealth()
	now := time.Now()

	h.observe(waku.Event{JSON: `{"eventType":"connection_status_change","connectionStatus":"Connected"}`}, now)
	h.observe(waku.Event{JSON: `{"eventType":"message_received","messageHash":"0xabc","message":{"payload":[1,2,3],"contentTopic":"/someone-else/1/x/proto"}}`}, now)
	h.setTopics(3)

	if !h.snapshot().OK(now) {
		t.Errorf("healthy node reported a problem: %q", h.snapshot().Problem(now))
	}
}

// The failure that motivated all of this: connected, subscribed, but the shard
// has gone quiet. Tunnels are fine; discovery is not.
func TestHealthDetectsSilentShard(t *testing.T) {
	h := newHealth()
	now := time.Now()

	h.observe(waku.Event{JSON: `{"eventType":"connection_status_change","connectionStatus":"Connected"}`}, now)
	h.observe(waku.Event{JSON: `{"eventType":"message_received","message":{"payload":[1],"contentTopic":"/x/1/y/proto"}}`}, now)
	h.setTopics(3)

	later := now.Add(RendezvousStale + time.Second)
	if h.snapshot().OK(later) {
		t.Error("a shard silent for longer than RendezvousStale reported healthy")
	}
	if h.snapshot().Problem(later) == "" {
		t.Error("unhealthy rendezvous reported no problem string")
	}
}

// A node that has just started has not received anything yet, and that is not
// the same as a broken one. It must still say so rather than claim health.
func TestHealthBeforeFirstMessage(t *testing.T) {
	h := newHealth()
	now := time.Now()
	h.observe(waku.Event{JSON: `{"eventType":"connection_status_change","connectionStatus":"Connected"}`}, now)
	h.setTopics(3)

	if h.snapshot().OK(now) {
		t.Error("reported healthy before any message arrived")
	}
	if got := h.snapshot().Problem(now); got == "" {
		t.Error("no problem string before the first message")
	}
}

// The warning line must state each fact once. An earlier version said the
// condition, the evidence and the consequence twice each, which is how a
// warning trains people to skip it.
func TestHealthMessageDoesNotRepeatItself(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name string
		h    Health
	}{
		{"disconnected", Health{Status: "Disconnected", Topics: 3, LastMessage: now.Add(-19 * time.Second)}},
		{"silent shard", Health{Status: "Connected", Topics: 3, LastMessage: now.Add(-RendezvousStale - time.Minute)}},
		{"nothing yet", Health{Status: "Connected", Topics: 3}},
		{"no topics", Health{Status: "Connected"}},
	}

	for _, c := range cases {
		problem, detail := c.h.Problem(now), c.h.Detail(now)
		if problem == "" {
			t.Errorf("%s: no problem reported for an unhealthy state", c.name)
			continue
		}

		// Nothing in the evidence may restate the condition.
		for _, w := range []string{"connected", "Connected", "topics"} {
			if strings.Contains(problem, w) && strings.Contains(detail, w) {
				t.Errorf("%s: %q appears in both problem (%q) and detail (%q)", c.name, w, problem, detail)
			}
		}

		// The consequence is the caller's to state, once.
		for _, w := range []string{"tunnel", "unaffected", "stalled", "discovery"} {
			if strings.Contains(strings.ToLower(problem+detail), w) {
				t.Errorf("%s: %q leaked into the condition/evidence: %q (%q)", c.name, w, problem, detail)
			}
		}
	}
}

// "0 topics" alongside "not subscribed to any topics" is the same fact twice.
func TestHealthDetailOmitsWhatProblemSaid(t *testing.T) {
	now := time.Now()
	h := Health{Status: "Connected"} // no topics, nothing received

	if got := h.Problem(now); got != "not subscribed to any topics" {
		t.Errorf("problem = %q", got)
	}
	if got := h.Detail(now); strings.Contains(got, "topics") {
		t.Errorf("detail %q repeats the topic count the problem already gave", got)
	}
}
