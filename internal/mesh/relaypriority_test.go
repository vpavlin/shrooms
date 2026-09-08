package mesh

import (
	"net/netip"
	"testing"
	"time"
)

// Which relay wins, when there is more than one answer.
//
// The order is: a live configured MEMBER relay, then a live DISCOVERED member
// relay, then a live configured BLIND one, then — only when nothing is
// answering at all — the first thing in the config.
//
// The last two are what changed on 2026-09-08. selectRelay used to return a
// configured relay unconditionally, live or not, and never reached discovery
// while anything was configured. So a blind relay left in a config outranked a
// member relay that was up and carrying traffic, forever.
//
// That cost most of a day across three machines. The laptop pushed 143 KB into
// a blind relay that returned nothing while vps sat there as a live member
// relay with working tunnels to both ends; and a phone pinned to that same
// blind relay could not reach k11, which had registered with vps — a relay
// forwards only between peers registered with IT, so two devices on different
// relays never meet.

// live marks a configured relay as having answered a routability challenge.
func live(m *Mesh, addr netip.AddrPort, when time.Time) {
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	if m.relayLive == nil {
		m.relayLive = map[netip.AddrPort]time.Time{}
	}
	m.relayLive[addr] = when
}

func pin(m *Mesh, addr string, blind bool) netip.AddrPort {
	ap := netip.MustParseAddrPort(addr)
	m.relays = append(m.relays, relayTarget{addr: ap, key: m.relayKey, blind: blind})
	return ap
}

// The case that was broken: a configured blind relay that has gone quiet, and a
// live member relay on the mesh. Discovery must win, immediately — nothing is
// working, so there is nothing to protect by waiting.
func TestDeadBlindRelayDoesNotHideALiveDiscoveredOne(t *testing.T) {
	f := newRelayFixture(t)
	pin(f.m, "222.167.212.15:31760", true) // never answers

	got := f.m.selectRelay(f.now)
	if !got.ok {
		t.Fatal("no relay selected")
	}
	if got.addr != f.relayAddr {
		t.Errorf("relay = %v, want the discovered member relay %v", got.addr, f.relayAddr)
	}
}

// A live configured MEMBER relay is the operator naming one of their own. It
// beats discovery, which is the property that lets everyone agree without
// negotiating.
func TestLiveConfiguredMemberBeatsDiscovery(t *testing.T) {
	f := newRelayFixture(t)
	own := pin(f.m, "203.0.113.77:51820", false)
	live(f.m, own, f.now)

	if got := f.m.selectRelay(f.now); got.addr != own {
		t.Errorf("relay = %v, want the configured member relay %v", got.addr, own)
	}
}

// A live blind relay holds the line until the discovered member relay has been
// usable for RelaySwitchAfter. Both ends must move together, and a relay only
// forwards between peers registered with it — so switching the instant a
// candidate appears is how two devices end up on different relays.
func TestLiveBlindRelayHeldUntilTheMemberOneIsSteady(t *testing.T) {
	f := newRelayFixture(t)
	stranger := pin(f.m, "222.167.212.15:31760", true)
	live(f.m, stranger, f.now)

	// First sight of the discovered relay: keep what works.
	if got := f.m.selectRelay(f.now); got.addr != stranger {
		t.Errorf("relay = %v, want to stay on the working blind relay %v", got.addr, stranger)
	}

	// Still inside the window.
	soon := f.now.Add(RelaySwitchAfter / 2)
	live(f.m, stranger, soon)
	if got := f.m.selectRelay(soon); got.addr != stranger {
		t.Errorf("switched after %v, before RelaySwitchAfter", RelaySwitchAfter/2)
	}

	// Steady long enough, and ours wins.
	//
	// The clock is wound back rather than time wound forward: everything else
	// here — the peer being online, its path being probed, the blind relay
	// answering — is measured against the same `now`, and advancing far enough
	// to pass RelaySwitchAfter would expire all three and test their timeouts
	// instead of this one.
	f.m.relayMu.Lock()
	f.m.relaySince = f.relayAddr
	f.m.relaySinceAt = f.now.Add(-RelaySwitchAfter - time.Second)
	f.m.relayMu.Unlock()

	if got := f.m.selectRelay(f.now); got.addr != f.relayAddr {
		t.Errorf("relay = %v, want the member relay %v once steady", got.addr, f.relayAddr)
	}
}

// A discovered relay that drops out and returns serves its waiting period
// again, rather than inheriting credit for the time it was gone.
func TestFlappingDiscoveredRelayRestartsTheClock(t *testing.T) {
	f := newRelayFixture(t)
	stranger := pin(f.m, "222.167.212.15:31760", true)
	live(f.m, stranger, f.now)
	f.m.selectRelay(f.now) // start the clock

	// Long enough that it would switch, if it stayed.
	f.m.relayMu.Lock()
	f.m.relaySince = f.relayAddr
	f.m.relaySinceAt = f.now.Add(-RelaySwitchAfter - time.Second)
	f.m.relayMu.Unlock()

	// But it vanishes: the roster forgets the relay peer.
	roster := f.m.roster
	f.m.roster = NewRoster(f.m.nk, make([]byte, 32))
	if got := f.m.selectRelay(f.now); got.addr != stranger {
		t.Fatalf("relay = %v, want the blind relay while nothing is discovered", got.addr)
	}

	// And comes back. The credit it had built up is gone with it, so it waits
	// again rather than switching the instant it reappears — which is the whole
	// point of the delay, since the other end is making the same decision.
	f.m.roster = roster
	if got := f.m.selectRelay(f.now); got.addr != stranger {
		t.Errorf("relay = %v, want the clock to restart on a returning relay", got.addr)
	}
}

// Nothing answers anywhere: keep the old last-resort behaviour rather than
// reporting no relay at all, since a configured address is still the operator's
// best guess.
func TestNothingLiveFallsBackToTheFirstConfigured(t *testing.T) {
	m := bareMesh(t)
	first := pin(m, "198.51.100.1:31760", true)
	pin(m, "198.51.100.2:31760", true)

	got := m.selectRelay(time.Now())
	if !got.ok || got.addr != first {
		t.Errorf("relay = %+v, want the first configured %v", got, first)
	}
}

// A node with no relays configured and none discovered has none, which is what
// every direct-path mesh looks like.
func TestNoRelaysAtAll(t *testing.T) {
	m := bareMesh(t)
	if got := m.selectRelay(time.Now()); got.ok {
		t.Errorf("relay = %+v, want none", got)
	}
}

// bareMesh is a mesh whose roster knows nobody, so nothing can be discovered.
func bareMesh(t *testing.T) *Mesh {
	t.Helper()
	f := newRelayFixture(t)
	f.m.roster = NewRoster(f.m.nk, make([]byte, 32))
	return f.m
}
