package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// Only a node worth bootstrapping from publishes an address, and each condition
// is load-bearing (ADR-031).
//
// Tested through bootAddr's own preconditions rather than a live node: the peer
// id needs an FFI call, and what matters here is which nodes decline to
// publish, not what the string looks like when they do.
func TestOnlyACoreRelayWithAPinnedPortPublishes(t *testing.T) {
	base := state.Config{
		Mode:         "Core",
		Relay:        true,
		DeliveryPort: 39777,
		Advertise:    []string{"128.140.55.128:51820"},
	}

	for _, tc := range []struct {
		name string
		edit func(c *state.Config)
	}{
		{"an Edge node has no gossip to share", func(c *state.Config) { c.Mode = "Edge" }},
		{"a non-relay is probably not reachable", func(c *state.Config) { c.Relay = false }},
		{"an unpinned port changes on restart", func(c *state.Config) { c.DeliveryPort = 0 }},
		{"no public address to name", func(c *state.Config) { c.Advertise = nil }},
	} {
		cfg := base
		tc.edit(&cfg)
		m := &Mesh{cfg: cfg, prober: nil}
		if got := m.bootAddrFor(cfg, time.Time{}); got != "" {
			t.Errorf("%s: published %q", tc.name, got)
		}
	}
}

// A public address is used and a private one is refused: an address only this
// LAN can reach is useless to members, who are mostly not on it.
func TestOnlyRoutableAddressesAreOffered(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"128.140.55.128:51820", true},
		{"192.168.0.152:51820", false},
		{"10.0.0.1:51820", false},
		{"127.0.0.1:51820", false},
		{"0.0.0.0:51820", false},
	} {
		cfg := state.Config{Mode: "Core", Relay: true, DeliveryPort: 1, Advertise: []string{tc.addr}}
		m := &Mesh{cfg: cfg}
		got := m.publicIPFrom(cfg, time.Time{}).IsValid()
		if got != tc.want {
			t.Errorf("%s: routable=%v, want %v", tc.addr, got, tc.want)
		}
	}
}
