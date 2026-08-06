package disco

import (
	"bytes"
	"crypto/ed25519"
	"net/netip"
	"testing"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

func fixture(t *testing.T) (Key, ed25519.PublicKey, TxID) {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("network key: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	tx, err := NewTxID()
	if err != nil {
		t.Fatalf("txid: %v", err)
	}
	return DeriveKey(nk), pub, tx
}

func TestPingRoundTrip(t *testing.T) {
	k, pub, tx := fixture(t)

	pkt, err := EncodePing(k, pub, tx)
	if err != nil {
		t.Fatalf("EncodePing: %v", err)
	}
	if len(pkt) != PacketLen {
		t.Fatalf("ping is %d bytes, want %d", len(pkt), PacketLen)
	}

	m, err := Decode(k, pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if m.Type != TypePing || m.TxID != tx {
		t.Fatalf("decoded %+v", m)
	}
	if string(m.SenderPub[:]) != string(pub) {
		t.Error("sender key did not survive")
	}
}

func TestPongCarriesObservedAddress(t *testing.T) {
	k, pub, tx := fixture(t)

	for _, want := range []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.7:51820"),
		netip.MustParseAddrPort("[2001:db8::1]:41234"),
		netip.MustParseAddrPort("10.91.0.100:1"),
	} {
		pkt, err := EncodePong(k, pub, tx, want)
		if err != nil {
			t.Fatalf("EncodePong: %v", err)
		}
		if len(pkt) != PacketLen {
			t.Fatalf("pong is %d bytes, want %d", len(pkt), PacketLen)
		}
		m, err := Decode(k, pkt)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if m.Observed != want {
			t.Errorf("observed = %s, want %s", m.Observed, want)
		}
	}
}

// Every packet is the same size, whatever its type or contents, so an observer
// cannot even tell a ping from a pong by length.
func TestPacketSizesAreConstant(t *testing.T) {
	k, pub, tx := fixture(t)

	ping, _ := EncodePing(k, pub, tx)
	v4, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("1.2.3.4:1"))
	v6, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("[2001:db8::dead:beef]:65535"))

	for _, pkt := range [][]byte{ping, v4, v6} {
		if len(pkt) != PacketLen {
			t.Fatalf("packet is %d bytes, want a constant %d", len(pkt), PacketLen)
		}
	}
}

// The whole point of encrypting rather than only authenticating: the device
// public key must not appear on the wire, or an on-path observer gets a stable
// identifier that follows the device between networks.
func TestSenderKeyNotOnTheWire(t *testing.T) {
	k, pub, tx := fixture(t)

	ping, _ := EncodePing(k, pub, tx)
	pong, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("203.0.113.9:41234"))

	for name, pkt := range map[string][]byte{"ping": ping, "pong": pong} {
		if bytes.Contains(pkt, pub) {
			t.Errorf("%s carries the sender public key in cleartext", name)
		}
		if bytes.Contains(pkt, tx[:]) {
			t.Errorf("%s carries the transaction id in cleartext", name)
		}
	}
}

// A pong's observed address is topology information and must not be visible.
func TestObservedAddressNotOnTheWire(t *testing.T) {
	k, pub, tx := fixture(t)

	observed := netip.MustParseAddrPort("203.0.113.9:41234")
	pkt, _ := EncodePong(k, pub, tx, observed)

	raw := observed.Addr().As4()
	if bytes.Contains(pkt, raw[:]) {
		t.Error("pong carries the observed address in cleartext")
	}
}

// Two packets with identical contents must differ on the wire, or an observer
// can fingerprint repeated probes.
func TestPacketsAreNotDeterministic(t *testing.T) {
	k, pub, tx := fixture(t)

	a, _ := EncodePing(k, pub, tx)
	b, _ := EncodePing(k, pub, tx)
	if bytes.Equal(a, b) {
		t.Fatal("identical inputs produced an identical packet; nonce is not random")
	}
}

// A node from a different mesh must not be able to inject probes.
func TestForeignKeyRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	other, _, _ := fixture(t)

	pkt, err := EncodePing(k, pub, tx)
	if err != nil {
		t.Fatalf("EncodePing: %v", err)
	}
	if _, err := Decode(other, pkt); err == nil {
		t.Fatal("a foreign disco key authenticated the packet")
	}
}

func TestTamperedPacketRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	pkt, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("203.0.113.7:51820"))

	for _, i := range []int{0, 1, 5, 40, len(pkt) - 1} {
		bad := append([]byte(nil), pkt...)
		bad[i] ^= 0x01
		if _, err := Decode(k, bad); err == nil {
			t.Errorf("accepted a packet with byte %d flipped", i)
		}
	}
}

func TestTruncatedPacketRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	pkt, _ := EncodePing(k, pub, tx)

	for n := 0; n < len(pkt); n++ {
		if _, err := Decode(k, pkt[:n]); err == nil {
			t.Errorf("accepted a %d-byte packet", n)
		}
	}
}

// A pong padded out to a ping's length (or vice versa) must not decode: the
// declared type and the actual length have to agree.
func TestLengthTypeMismatchRejected(t *testing.T) {
	k, pub, tx := fixture(t)

	// With encryption an attacker cannot re-type a packet without the key, so
	// the length/type mismatch this used to guard is no longer reachable.
	// What remains testable is that a wrong-length packet is rejected outright.
	pong, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("1.2.3.4:5"))
	if _, err := Decode(k, append(pong, 0)); err == nil {
		t.Fatal("accepted an over-long packet")
	}
}

// An unknown type can only be produced by someone holding the key, so this
// checks the parser rejects it rather than mis-handling it.
func TestUnknownTypeRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	forged, err := seal(k, Type(99), pub, tx, netip.AddrPort{})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Decode(k, forged); err == nil {
		t.Fatal("accepted an unknown disco type")
	}
}

// The version byte is authenticated as additional data, so tampering with it
// fails decryption rather than being silently accepted.
func TestWrongVersionRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	ping, _ := EncodePing(k, pub, tx)

	bad := append([]byte(nil), ping...)
	bad[0] = 99
	if _, err := Decode(k, bad); err == nil {
		t.Fatal("accepted an unknown disco version")
	}
}

func TestTxIDsAreUnique(t *testing.T) {
	seen := map[TxID]bool{}
	for i := 0; i < 256; i++ {
		tx, err := NewTxID()
		if err != nil {
			t.Fatalf("NewTxID: %v", err)
		}
		if seen[tx] {
			t.Fatal("duplicate transaction id")
		}
		seen[tx] = true
	}
}

func TestDiscoKeyDiffersPerMesh(t *testing.T) {
	a, _ := identity.NewNetworkKey()
	b, _ := identity.NewNetworkKey()
	if DeriveKey(a) == DeriveKey(b) {
		t.Fatal("two meshes derived the same disco key")
	}
}
