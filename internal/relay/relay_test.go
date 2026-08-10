package relay

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

func testKey(t *testing.T) Key {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("network key: %v", err)
	}
	return DeriveKey(nk)
}

func wgKey(b byte) identity.WGKey {
	var k identity.WGKey
	for i := range k {
		k[i] = b
	}
	return k
}

func TestRegisterRoundTrip(t *testing.T) {
	k := testKey(t)
	self := wgKey(1)

	f, err := Decode(k, EncodeRegister(k, self))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if f.Type != TypeRegister || f.Key != self {
		t.Fatalf("decoded %+v", f)
	}
}

func TestForwardRoundTrip(t *testing.T) {
	k := testKey(t)
	dst, src := wgKey(2), wgKey(3)
	payload := []byte("an opaque wireguard packet")

	raw, err := EncodeForward(k, dst, src, payload)
	if err != nil {
		t.Fatalf("EncodeForward: %v", err)
	}
	f, err := Decode(k, raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if f.Type != TypeForward || f.Key != dst || f.Src != src {
		t.Fatalf("decoded %+v", f)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("payload = %q", f.Payload)
	}
}

// A relay only serves its own mesh; frames keyed to another network must not
// authenticate, or it becomes an open reflector.
func TestForeignKeyRejected(t *testing.T) {
	k, other := testKey(t), testKey(t)
	raw, _ := EncodeForward(k, wgKey(2), wgKey(3), []byte("x"))
	if _, err := Decode(other, raw); err == nil {
		t.Fatal("a foreign relay key authenticated the frame")
	}
}

func TestTamperedHeaderRejected(t *testing.T) {
	k := testKey(t)
	raw, _ := EncodeForward(k, wgKey(2), wgKey(3), []byte("payload"))

	// Flip a bit in the destination key: an attacker redirecting traffic.
	bad := append([]byte(nil), raw...)
	bad[1] ^= 0x01
	if _, err := Decode(k, bad); err == nil {
		t.Fatal("accepted a frame with a tampered destination")
	}
}

func TestTruncatedRejected(t *testing.T) {
	k := testKey(t)
	raw, _ := EncodeForward(k, wgKey(2), wgKey(3), []byte("payload"))
	for _, n := range []int{0, 1, 10, forwardHeaderLen - 1} {
		if _, err := Decode(k, raw[:n]); err == nil {
			t.Errorf("accepted a %d-byte frame", n)
		}
	}
}

// --- server ---

func TestServerForwardsBetweenRegisteredPeers(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	now := time.Now()

	a, b := wgKey(0xaa), wgKey(0xbb)
	addrA := netip.MustParseAddrPort("203.0.113.1:51820")
	addrB := netip.MustParseAddrPort("203.0.113.2:51820")

	s.Handle(EncodeRegister(k, a), addrA, now)
	s.Handle(EncodeRegister(k, b), addrB, now)

	payload := []byte("wireguard bytes")
	frame, _ := EncodeForward(k, b, identity.WGKey{}, payload)

	out, to, ok := s.Handle(frame, addrA, now)
	if !ok {
		t.Fatal("relay did not forward between two registered peers")
	}
	if to != addrB {
		t.Fatalf("forwarded to %s, want %s", to, addrB)
	}

	f, err := Decode(k, out)
	if err != nil {
		t.Fatalf("Decode forwarded frame: %v", err)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Error("payload was altered in transit")
	}
	// The relay fills in the source from who actually registered that address,
	// so a client cannot forge who a packet appears to come from.
	if f.Src != a {
		t.Errorf("src = %x, want %x", f.Src[:4], a[:4])
	}
}

// A sender that has not registered has no verified identity, so its traffic
// must not be forwarded — otherwise anyone holding the network key could inject
// packets attributed to an arbitrary peer.
func TestUnregisteredSenderDropped(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	now := time.Now()

	b := wgKey(0xbb)
	s.Handle(EncodeRegister(k, b), netip.MustParseAddrPort("203.0.113.2:51820"), now)

	frame, _ := EncodeForward(k, b, identity.WGKey{}, []byte("x"))
	if _, _, ok := s.Handle(frame, netip.MustParseAddrPort("198.51.100.9:1234"), now); ok {
		t.Fatal("forwarded traffic from an unregistered sender")
	}
}

func TestUnknownDestinationDropped(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	now := time.Now()

	a := wgKey(0xaa)
	addrA := netip.MustParseAddrPort("203.0.113.1:51820")
	s.Handle(EncodeRegister(k, a), addrA, now)

	frame, _ := EncodeForward(k, wgKey(0xcc), identity.WGKey{}, []byte("x"))
	if _, _, ok := s.Handle(frame, addrA, now); ok {
		t.Fatal("forwarded to an unregistered destination")
	}
}

// A NAT rebinding silently invalidates a mapping, so a stale one must expire
// rather than black-hole traffic.
func TestStaleRegistrationExpires(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	start := time.Now()

	a, b := wgKey(0xaa), wgKey(0xbb)
	addrA := netip.MustParseAddrPort("203.0.113.1:51820")
	s.Handle(EncodeRegister(k, a), addrA, start)
	s.Handle(EncodeRegister(k, b), netip.MustParseAddrPort("203.0.113.2:51820"), start)

	frame, _ := EncodeForward(k, b, identity.WGKey{}, []byte("x"))
	if _, _, ok := s.Handle(frame, addrA, start.Add(RegistrationTTL+time.Second)); ok {
		t.Fatal("forwarded to a peer whose registration had expired")
	}
}

// The relay records where a packet came from, never an address the client
// claims — a client behind NAT does not know its own external address, and one
// that lied could redirect another peer's traffic to itself.
func TestRegistrationUsesObservedAddress(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	now := time.Now()

	a, b := wgKey(0xaa), wgKey(0xbb)
	observed := netip.MustParseAddrPort("203.0.113.77:40000")

	s.Handle(EncodeRegister(k, a), netip.MustParseAddrPort("203.0.113.1:51820"), now)
	s.Handle(EncodeRegister(k, b), observed, now)

	frame, _ := EncodeForward(k, b, identity.WGKey{}, []byte("x"))
	_, to, ok := s.Handle(frame, netip.MustParseAddrPort("203.0.113.1:51820"), now)
	if !ok || to != observed {
		t.Fatalf("forwarded to %s, want the observed address %s", to, observed)
	}
}

func TestServerStats(t *testing.T) {
	k := testKey(t)
	s := NewServer(k)
	now := time.Now()

	s.Handle(EncodeRegister(k, wgKey(1)), netip.MustParseAddrPort("203.0.113.1:1"), now)
	s.Handle([]byte("garbage"), netip.MustParseAddrPort("203.0.113.9:1"), now)

	peers, registered, _, dropped := s.Stats()
	if peers != 1 || registered != 1 {
		t.Errorf("peers=%d registered=%d, want 1/1", peers, registered)
	}
	if dropped != 1 {
		t.Errorf("dropped=%d, want 1", dropped)
	}
}
