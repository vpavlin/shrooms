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
// selectRelay used to return a configured relay unconditionally, live or not,
// and never reached discovery while anything was configured. So a blind relay
// somebody pointed at months ago outranked a member relay that was up and
// carrying traffic, for as long as the line stayed in the file. That cost most
// of a day across three machines: the laptop pushed 143 KB into a dead blind
// relay while vps sat there working, and a phone pinned to that same relay
// could not reach k11, which had registered with vps. A relay forwards only
// between peers registered with IT, so two devices on different relays never
// meet.

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

// bareMesh is a mesh whose roster knows nobody, so nothing can be discovered.
func bareMesh(t *testing.T) *Mesh {
	t.Helper()
	f := newRelayFixture(t)
	f.m.roster = NewRoster(f.m.nk, make([]byte, 32))
	return f.m
}

// The case that was broken: a configured blind relay that has gone quiet, and a
// live member relay on the mesh.
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
// beats discovery, which is what lets everyone agree without negotiating.
func TestLiveConfiguredMemberBeatsDiscovery(t *testing.T) {
	f := newRelayFixture(t)
	own := pin(f.m, "203.0.113.77:51820", false)
	live(f.m, own, f.now)

	if got := f.m.selectRelay(f.now); got.addr != own {
		t.Errorf("relay = %v, want the configured member relay %v", got.addr, own)
	}
}

// What the reordering is FOR, in Vaclav's words: keep a blind relay configured
// as a fallback and still use the real one.
//
// Immediately, with no waiting period. The first version of this made the
// upgrade wait for the discovered relay to prove itself for a couple of
// minutes, which read as prudent and was not. Both ends move to the same relay
// by the same rule and converge within a probe cycle, so the disagreement being
// avoided lasts seconds — while sitting on a relay the peer is not registered
// with lasts forever. On a phone it never resolved at all: a backgrounded VPN
// service loses a probed path for longer than disco.PathFresh often enough that
// the timer kept restarting, and the upgrade never happened.
func TestLiveMemberRelayBeatsALiveBlindOneAtOnce(t *testing.T) {
	f := newRelayFixture(t)
	stranger := pin(f.m, "222.167.212.15:31760", true)
	live(f.m, stranger, f.now)

	if got := f.m.selectRelay(f.now); got.addr != f.relayAddr {
		t.Errorf("relay = %v, want the discovered member relay %v straight away",
			got.addr, f.relayAddr)
	}
}

// The hysteresis, in the direction it belongs: a discovered relay whose path
// has just gone stale keeps being used rather than handing traffic to a
// stranger's on one missed pong. Dropping at once is what makes a device
// alternate between the two.
func TestAStaleDiscoveredRelayIsHeldBeforeFallingBack(t *testing.T) {
	f := newRelayFixture(t)
	stranger := pin(f.m, "222.167.212.15:31760", true)
	live(f.m, stranger, f.now)

	if got := f.m.selectRelay(f.now); got.addr != f.relayAddr {
		t.Fatalf("relay = %v, want the member relay first", got.addr)
	}

	// Its path goes stale: nobody is announcing any more.
	f.m.roster = NewRoster(f.m.nk, make([]byte, 32))

	within := f.now.Add(RelayHold / 2)
	live(f.m, stranger, within)
	if got := f.m.selectRelay(within); got.addr != f.relayAddr {
		t.Errorf("relay = %v, want to hold the member relay for RelayHold", got.addr)
	}

	// Gone long enough to believe it: the configured relay takes over.
	after := f.now.Add(RelayHold + time.Second)
	live(f.m, stranger, after)
	if got := f.m.selectRelay(after); got.addr != stranger {
		t.Errorf("relay = %v, want the blind relay %v once the member one is really gone",
			got.addr, stranger)
	}
}

// A discovered relay that comes back inside the hold window is simply still in
// use. There is nothing to re-earn, which is the point of holding it.
func TestDiscoveredRelayReturningInsideTheHoldIsUninterrupted(t *testing.T) {
	f := newRelayFixture(t)
	stranger := pin(f.m, "222.167.212.15:31760", true)
	live(f.m, stranger, f.now)
	f.m.selectRelay(f.now)

	roster := f.m.roster
	f.m.roster = NewRoster(f.m.nk, make([]byte, 32))
	gone := f.now.Add(RelayHold / 4)
	live(f.m, stranger, gone)
	if got := f.m.selectRelay(gone); got.addr != f.relayAddr {
		t.Fatalf("relay = %v, want the member relay held", got.addr)
	}

	f.m.roster = roster
	if got := f.m.selectRelay(f.now); got.addr != f.relayAddr {
		t.Errorf("relay = %v, want the member relay still", got.addr)
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
