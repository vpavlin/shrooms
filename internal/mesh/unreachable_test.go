package mesh

import (
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// The diagnosis must fire only when all three facts hold, because each alone is
// ordinary and saying so would be noise.
//
// It exists because the combination cost somebody a whole day: peers appearing
// normally, handshakes retrying forever, and a laptop announcing two perfectly
// good addresses that a firewall would not let anything reach.
func TestUnreachableIsOnlySaidWhenItIsCertain(t *testing.T) {
	// A node with an address to give, a peer that is up, and nothing ever
	// received. Anything less than that must stay quiet.
	fresh := func() *Mesh {
		return &Mesh{
			log:     slog.New(slog.DiscardHandler),
			cfg:     state.Config{ListenPort: 51820, Advertise: []string{"203.0.113.4:51820"}},
			started: time.Now().Add(-unreachableFor - time.Minute),
		}
	}

	t.Run("too early to tell", func(t *testing.T) {
		m := fresh()
		m.started = time.Now()
		if m.unreachable(time.Now(), []string{"203.0.113.4:51820"}, true) {
			t.Error("accused the network before discovery could even finish")
		}
	})

	t.Run("something has arrived", func(t *testing.T) {
		m := fresh()
		m.heardAnything = true
		if m.unreachable(time.Now(), []string{"203.0.113.4:51820"}, true) {
			t.Error("called a node unreachable after it had received a packet")
		}
	})

	t.Run("nothing to announce is a different fault", func(t *testing.T) {
		m := fresh()
		if m.unreachable(time.Now(), nil, true) {
			t.Error("blamed a firewall for having no address to give")
		}
	})

	t.Run("no peer online proves nothing", func(t *testing.T) {
		m := fresh()
		if m.unreachable(time.Now(), []string{"203.0.113.4:51820"}, false) {
			t.Error("blamed a firewall when no peer was even online")
		}
	})
}

// The case it exists for: everything points at a closed door.
func TestUnreachableIsSaidWhenAllThreeHold(t *testing.T) {
	m := &Mesh{
		log:     slog.New(slog.DiscardHandler),
		cfg:     state.Config{ListenPort: 51820},
		started: time.Now().Add(-unreachableFor - time.Minute),
	}
	if !m.unreachable(time.Now(), []string{"203.0.113.4:51820"}, true) {
		t.Error("announcing an address, a peer online, nothing ever received — and it said nothing")
	}
}

// Said once, not every few seconds: the probe ticker runs constantly and a
// warning that repeats is a log nobody reads.
func TestUnreachableIsSaidOnce(t *testing.T) {
	m := &Mesh{
		log:     slog.New(slog.DiscardHandler),
		cfg:     state.Config{ListenPort: 51820},
		started: time.Now().Add(-unreachableFor - time.Minute),
	}
	addrs := []string{"203.0.113.4:51820"}

	if !m.unreachable(time.Now(), addrs, true) {
		t.Fatal("did not say it the first time")
	}
	m.saidUnreachable = true
	if m.unreachable(time.Now(), addrs, true) {
		t.Error("said it again after it had already been said")
	}
}
