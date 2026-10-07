package disco

import (
	"net/netip"
	"testing"
	"time"
)

// Paths were never dropped, so a phone that had left Wi-Fi hours earlier was
// still listed at its Wi-Fi addresses. One that has not answered for
// PathForget goes on the peer's next probe; one that answered recently stays.
func TestAPathUnansweredForAnHourIsForgotten(t *testing.T) {
	p := newBareProber(t)
	now := time.Now()
	old := netip.MustParseAddrPort("192.168.7.36:51820")
	recent := netip.MustParseAddrPort("[2001:db8::1c]:51820")
	pathAt(p, "pixel", old, 89*time.Millisecond, now.Add(-PathForget))
	pathAt(p, "pixel", recent, 89*time.Millisecond, now.Add(-10*time.Minute))

	p.Probe("pixel", nil, now)

	paths := p.Paths("pixel")
	if len(paths) != 1 || paths[0].Addr != recent {
		t.Errorf("after an hour unanswered, paths = %v, want only %s", paths, recent)
	}
}
