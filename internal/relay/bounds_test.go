package relay

import (
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

func key(n byte) identity.WGKey {
	var k identity.WGKey
	k[0] = n
	k[1] = 0xaa
	return k
}

func addr(n int) netip.AddrPort {
	return netip.AddrPortFrom(netip.MustParseAddr("198.51.100.1"), uint16(20000+n))
}

// The keys are chosen by whoever registers and nothing capped the table, so one
// member could invent millions of them — each held for a full TTL — until the
// relay ran out of memory. Registering a key the table already has must still
// work, or a cap would lock out the devices the relay exists to carry.
func TestRegistrationsAreCapped(t *testing.T) {
	s := NewServer(Key{}, nil)
	now := time.Now()

	s.mu.Lock()
	for i := 0; i < MaxRegistrations+50; i++ {
		s.registerLocked(key(byte(i%251)), addr(i), now)
	}
	held := len(s.peers)
	s.mu.Unlock()

	if held > MaxRegistrations {
		t.Errorf("table holds %d registrations, cap is %d", held, MaxRegistrations)
	}
}

// One address holds one key. A member with a single socket could otherwise
// accumulate entries without limit, which is what made the table unbounded in
// the first place.
func TestOneAddressHoldsOneKey(t *testing.T) {
	s := NewServer(Key{}, nil)
	now := time.Now()
	from := addr(1)

	s.mu.Lock()
	s.registerLocked(key(1), from, now)
	s.registerLocked(key(2), from, now)
	_, first := s.peers[key(1)]
	_, second := s.peers[key(2)]
	s.mu.Unlock()

	if first {
		t.Error("the first key survived after the same address registered another")
	}
	if !second {
		t.Error("the second registration from that address was not recorded")
	}
}

// A device that moves — a phone changing network, a NAT rebinding — re-registers
// the same key from a new address. The old reverse entry must go with it, or a
// later forward resolves a source that has moved away.
func TestReRegisteringFromANewAddressLeavesNothingBehind(t *testing.T) {
	s := NewServer(Key{}, nil)
	now := time.Now()
	old, new_ := addr(1), addr(2)

	s.mu.Lock()
	s.registerLocked(key(7), old, now)
	s.registerLocked(key(7), new_, now)
	_, stale := s.byAddr[old]
	got := s.byAddr[new_]
	n := len(s.peers)
	s.mu.Unlock()

	if stale {
		t.Error("the old address still maps to a key")
	}
	if got != key(7) {
		t.Error("the new address does not map to the key that registered from it")
	}
	if n != 1 {
		t.Errorf("one device left %d registrations", n)
	}
}

// Expiry has to clear both directions, or byAddr keeps naming a key that no
// longer exists and a forward resolves a source that is gone.
func TestExpiryClearsBothDirections(t *testing.T) {
	s := NewServer(Key{}, nil)
	now := time.Now()

	s.mu.Lock()
	s.registerLocked(key(3), addr(3), now.Add(-2*RegistrationTTL))
	s.expireLocked(now)
	peers, rev := len(s.peers), len(s.byAddr)
	s.mu.Unlock()

	if peers != 0 || rev != 0 {
		t.Errorf("after expiry: %d peers, %d reverse entries; want 0 and 0", peers, rev)
	}
}

// The source lookup on every forwarded packet used to scan the whole table.
// With the table capped at 512 that is bounded, but it is still per packet;
// the reverse index makes it a lookup, and this asserts it answers correctly
// rather than that it is fast.
func TestSourceLookupUsesTheReverseIndex(t *testing.T) {
	s := NewServer(Key{}, nil)
	now := time.Now()

	s.mu.Lock()
	for i := 0; i < 100; i++ {
		s.registerLocked(key(byte(i)), addr(i), now)
	}
	s.mu.Unlock()

	got, ok := s.lookupByAddr(addr(42), now)
	if !ok || got != key(42) {
		t.Errorf("lookupByAddr(addr 42) = %v, %v; want the key registered there", got, ok)
	}
	if _, ok := s.lookupByAddr(addr(9999), now); ok {
		t.Error("an address that never registered resolved to a key")
	}
	// A registration past its TTL must not answer, even though the reverse
	// entry is still there until the next sweep.
	if _, ok := s.lookupByAddr(addr(42), now.Add(2*RegistrationTTL)); ok {
		t.Error("a stale registration answered a source lookup")
	}
}

// A refused registration must be countable, because "is this relay turning my
// device away?" is the question asked of a relay that is up and carrying
// nothing — and it had no answer: the counter existed and was reported nowhere.
func TestRefusalsAreCounted(t *testing.T) {
	// owns says no to everything, which is what a relay does for a device whose
	// announce it has not seen (ADR-029).
	s := NewServer(Key{}, func([]byte, identity.WGKey) bool { return false })
	now := time.Now()

	before := s.Stats().Refused
	s.Handle(EncodeRegister(Key{}, key(1), regKey(t), now),
		netip.MustParseAddrPort("203.0.113.1:1"), now)

	if got := s.Stats().Refused; got != before+1 {
		t.Errorf("refused went %d -> %d, want one more", before, got)
	}
	if s.Stats().Peers != 0 {
		t.Error("a refused registration was installed anyway")
	}
}
