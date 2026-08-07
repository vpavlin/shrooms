package mesh

import (
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
