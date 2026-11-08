package disco

import (
	"net/netip"
	"testing"
	"time"
)

func TestNATViewVerdicts(t *testing.T) {
	now := time.Now()
	view := func(obs ...[2]string) NATView {
		p := NewProber(testKey(t), newDeviceKey(t), nil)
		for _, o := range obs {
			p.NoteReflexive(netip.MustParseAddrPort(o[0]), o[1], now)
		}
		return p.NATView(now)
	}
	if v := view(); v.Mapping != "" {
		t.Errorf("nothing observed: %q", v.Mapping)
	}
	if v := view([2]string{"203.0.113.5:40000", "a"}); v.Mapping != "unknown" {
		t.Errorf("one observer: %q", v.Mapping)
	}
	if v := view([2]string{"203.0.113.5:40000", "a"}, [2]string{"203.0.113.5:40000", "b"}); v.Mapping != "independent" {
		t.Errorf("two observers agreeing: %q", v.Mapping)
	}
	// The carrier-NAT case: one public IP, a port per destination.
	v := view([2]string{"203.0.113.5:40000", "a"}, [2]string{"203.0.113.5:40123", "b"})
	if v.Mapping != "dependent" || len(v.Observed) != 2 {
		t.Errorf("one IP, two ports: %+v", v)
	}
	// A LAN peer's view beside a public one is not a second mapping.
	if v := view([2]string{"192.168.10.5:51820", "lan"}, [2]string{"203.0.113.5:40000", "a"}); v.Mapping != "unknown" {
		t.Errorf("a LAN view and one public view: %q", v.Mapping)
	}
	// Observations older than reflexiveTTL do not count.
	p := NewProber(testKey(t), newDeviceKey(t), nil)
	p.NoteReflexive(netip.MustParseAddrPort("203.0.113.5:40000"), "a", now.Add(-time.Hour))
	if v := p.NATView(now); v.Mapping != "" {
		t.Errorf("only stale observations: %q", v.Mapping)
	}
}
