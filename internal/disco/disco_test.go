package disco

import (
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
	if len(pkt) != PingLen {
		t.Fatalf("ping is %d bytes, want %d", len(pkt), PingLen)
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
		if len(pkt) != PongLen {
			t.Fatalf("pong is %d bytes, want %d", len(pkt), PongLen)
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

// Every ping is the same size and every pong is the same size, so an observer
// learns nothing from length.
func TestPacketSizesAreConstant(t *testing.T) {
	k, pub, tx := fixture(t)

	a, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("1.2.3.4:1"))
	b, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("[2001:db8::dead:beef]:65535"))
	if len(a) != len(b) {
		t.Fatalf("pong size varies with address family: %d vs %d", len(a), len(b))
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

	pong, _ := EncodePong(k, pub, tx, netip.MustParseAddrPort("1.2.3.4:5"))
	// Re-mac a pong body claiming to be a ping.
	body := append([]byte(nil), pong[:len(pong)-macLen]...)
	body[1] = byte(TypePing)
	forged := append(body, mac(k, body)...)

	if _, err := Decode(k, forged); err == nil {
		t.Fatal("accepted a ping whose length is a pong's")
	}
}

func TestUnknownTypeRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	ping, _ := EncodePing(k, pub, tx)

	body := append([]byte(nil), ping[:len(ping)-macLen]...)
	body[1] = 99
	forged := append(body, mac(k, body)...)

	if _, err := Decode(k, forged); err == nil {
		t.Fatal("accepted an unknown disco type")
	}
}

func TestWrongVersionRejected(t *testing.T) {
	k, pub, tx := fixture(t)
	ping, _ := EncodePing(k, pub, tx)

	body := append([]byte(nil), ping[:len(ping)-macLen]...)
	body[0] = 99
	forged := append(body, mac(k, body)...)

	if _, err := Decode(k, forged); err == nil {
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
