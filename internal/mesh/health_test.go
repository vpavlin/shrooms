package mesh

import (
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/waku"
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

func connChange(peer, event string) waku.Event {
	return waku.Event{JSON: `{"eventType":"connection_change","peerId":"` + peer + `","peerEvent":"` + event + `"}`}
}

func statusChange(s string) waku.Event {
	return waku.Event{JSON: `{"eventType":"connection_status_change","connectionStatus":"` + s + `"}`}
}

// The failure that cost an hour: logos.dev moved to cluster 3 while our preset
// said 2, so every peer connected, disagreed on metadata, and hung up. The
// reason never leaves nwaku's own log, so all our tooling could say was "not
// connected to any fleet peers" — true, and pointing at the wrong culprit.
//
// The churn pattern is the recoverable signal.
func TestHealthDetectsClusterMismatchPattern(t *testing.T) {
	h := newHealth()
	now := time.Now()

	for i, p := range []string{"16UpeerA", "16UpeerB", "16UpeerC", "16UpeerD"} {
		at := now.Add(time.Duration(i) * time.Second)
		h.observe(connChange(p, "EventConnected"), at)
		h.observe(connChange(p, "EventDisconnected"), at.Add(300*time.Millisecond))
	}
	h.observe(statusChange("Disconnected"), now)
	h.setTopics(3)

	got := h.snapshot().Problem(now)
	if !strings.Contains(got, "cluster_id") || !strings.Contains(got, "preset") {
		t.Errorf("problem = %q, want it to name preset/cluster_id as the likely cause", got)
	}
}

// A peer that stayed connected for a while and then dropped is an ordinary
// disconnect, not a refusal. Counting those would fire the mismatch warning at
// every node that has simply been running a while.
func TestHealthDoesNotCountNormalDisconnects(t *testing.T) {
	h := newHealth()
	now := time.Now()

	for i, p := range []string{"16UpeerA", "16UpeerB", "16UpeerC", "16UpeerD"} {
		at := now.Add(time.Duration(i) * time.Minute)
		h.observe(connChange(p, "EventConnected"), at)
		h.observe(connChange(p, "EventDisconnected"), at.Add(ShortLived+time.Second))
	}
	h.observe(statusChange("Disconnected"), now)

	if c := h.snapshot().Churn; c != 0 {
		t.Errorf("churn = %d after only long-lived sessions, want 0", c)
	}
	if got := h.snapshot().Problem(now); strings.Contains(got, "cluster_id") {
		t.Errorf("mismatch warning fired on ordinary disconnects: %q", got)
	}
}

// Once some peer accepts us the mismatch theory is dead. Without this the
// warning would stick to a node that had recovered.
func TestHealthChurnResetsOnConnected(t *testing.T) {
	h := newHealth()
	now := time.Now()

	for _, p := range []string{"16UpeerA", "16UpeerB", "16UpeerC"} {
		h.observe(connChange(p, "EventConnected"), now)
		h.observe(connChange(p, "EventDisconnected"), now.Add(100*time.Millisecond))
	}
	if h.snapshot().Churn < ChurnThreshold {
		t.Fatalf("churn = %d, expected the threshold to be reached", h.snapshot().Churn)
	}

	h.observe(statusChange("Connected"), now)
	if c := h.snapshot().Churn; c != 0 {
		t.Errorf("churn = %d after reaching Connected, want 0", c)
	}
}

// An unmatched disconnect (connect never seen, e.g. from before we started)
// must not be counted or crash.
func TestHealthIgnoresUnmatchedDisconnect(t *testing.T) {
	h := newHealth()
	now := time.Now()
	h.observe(connChange("16Uunknown", "EventDisconnected"), now)
	if c := h.snapshot().Churn; c != 0 {
		t.Errorf("churn = %d from an unmatched disconnect, want 0", c)
	}
}

// The failure both other checks miss: the plane is up, thousands of other
// applications' messages are arriving on the shard, and not one of ours has
// been opened in a quarter of an hour.
func TestSilentPlaneIsNoticed(t *testing.T) {
	now := time.Now()
	h := Health{
		Status:       "Connected",
		LastMessage:  now.Add(-2 * time.Second),
		LastAnnounce: now.Add(-30 * time.Minute),
	}
	if h.OK(now) != true {
		t.Fatal("this test is about a plane OK calls healthy")
	}
	if !h.Silent(now) {
		t.Error("a plane carrying nothing of ours was not noticed")
	}
}

// A node that has never heard a peer is not deaf, it is alone. Restarting it
// would achieve nothing, so it must not be mistaken for the case above.
func TestALonelyNodeIsNotSilent(t *testing.T) {
	now := time.Now()
	h := Health{Status: "Connected", LastMessage: now.Add(-time.Second)}
	if h.Silent(now) {
		t.Error("a node that never had a peer was called deaf")
	}
}

// And a plane that has genuinely stopped is the other check's business: this
// one must not also fire, or the two would race to restart for the same fault.
func TestADeadPlaneIsNotSilent(t *testing.T) {
	now := time.Now()
	h := Health{
		Status:       "Disconnected",
		LastMessage:  now.Add(-time.Hour),
		LastAnnounce: now.Add(-time.Hour),
	}
	if h.Silent(now) {
		t.Error("a dead plane was reported as merely silent")
	}
}
