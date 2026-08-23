package control

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

func fixture(t *testing.T) (identity.NetworkKey, *identity.Identity, *Announce) {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("network key: %v", err)
	}
	id, err := identity.New()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	a := &Announce{
		Kind:      KindAnnounce,
		DevicePub: id.DevicePub,
		WGPub:     id.WGPub[:],
		Name:      "home-nas",
		Endpoints: []string{"203.0.113.4:51820", "[2001:db8::1]:51820"},
		Seq:       1,
		Timestamp: time.Now().Unix(),
	}
	return nk, id, a
}

func TestSealOpenRoundTrip(t *testing.T) {
	nk, id, a := fixture(t)

	raw, err := Seal(nk, 100, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := OpenAnnounce(nk, 100, raw, time.Now())
	if err != nil {
		t.Fatalf("OpenAnnounce: %v", err)
	}

	if got.Name != a.Name || got.Seq != a.Seq {
		t.Errorf("round trip changed the message: %+v", got)
	}
	if len(got.Endpoints) != 2 || got.Endpoints[0] != a.Endpoints[0] {
		t.Errorf("endpoints wrong: %v", got.Endpoints)
	}
}

// Fixed-size padding is what makes "came online" indistinguishable from a
// steady-state heartbeat.
func TestCiphertextSizeIsConstant(t *testing.T) {
	nk, id, a := fixture(t)

	var size int
	for i, name := range []string{"a", "a-much-longer-device-name-here", "x"} {
		a.Name = name
		a.Endpoints = make([]string, i) // vary the content too
		for j := range a.Endpoints {
			a.Endpoints[j] = "203.0.113.9:51820"
		}
		raw, err := Seal(nk, 1, id.DevicePriv, a)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if size == 0 {
			size = len(raw)
		} else if len(raw) != size {
			t.Fatalf("ciphertext size varies with content: %d vs %d", len(raw), size)
		}
	}
}

func TestWrongNetworkKeyCannotOpen(t *testing.T) {
	nk, id, a := fixture(t)
	raw, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	other, _ := identity.NewNetworkKey()
	if _, err := OpenAnnounce(other, 1, raw, time.Now()); err == nil {
		t.Fatal("a foreign network key decrypted the message")
	}
}

func TestWrongEpochCannotOpen(t *testing.T) {
	nk, id, a := fixture(t)
	raw, err := Seal(nk, 5, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenAnnounce(nk, 6, raw, time.Now()); err == nil {
		t.Fatal("the wrong epoch key decrypted the message")
	}
}

func TestOpenWindowFindsNeighbouringEpoch(t *testing.T) {
	nk, id, a := fixture(t)
	raw, err := Seal(nk, 41, id.DevicePriv, a) // peer's clock is an epoch behind
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenAnnounceWindow(nk, []int64{41, 42, 43}, raw, time.Now()); err != nil {
		t.Fatalf("OpenAnnounceWindow: %v", err)
	}
	if _, err := OpenAnnounceWindow(nk, []int64{50, 51}, raw, time.Now()); err == nil {
		t.Fatal("opened outside the window")
	}
}

// The signature must be bound to the key named inside the message, or anyone
// holding the network key could impersonate any device.
func TestForgedSignatureRejected(t *testing.T) {
	nk, _, a := fixture(t)

	attacker, err := identity.New()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	// Attacker holds the network key and signs with their own device key, but
	// claims the victim's DevicePub.
	victim, _ := identity.New()
	a.DevicePub = victim.DevicePub

	raw, err := Seal(nk, 1, attacker.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenAnnounce(nk, 1, raw, time.Now()); err == nil {
		t.Fatal("accepted a message signed by a different key than it claims")
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	nk, id, a := fixture(t)
	raw, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	if _, err := OpenAnnounce(nk, 1, raw, time.Now()); err == nil {
		t.Fatal("accepted tampered ciphertext")
	}
}

func TestStaleTimestampRejected(t *testing.T) {
	nk, id, a := fixture(t)
	a.Timestamp = time.Now().Add(-3 * MaxClockSkew).Unix()
	raw, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenAnnounce(nk, 1, raw, time.Now()); err == nil {
		t.Fatal("accepted a stale message")
	}
}

func TestShortCiphertextRejected(t *testing.T) {
	nk, _, _ := fixture(t)
	for _, raw := range [][]byte{{}, make([]byte, 5), make([]byte, 23)} {
		if _, err := OpenAnnounce(nk, 1, raw, time.Now()); err == nil {
			t.Errorf("accepted a %d-byte ciphertext", len(raw))
		}
	}
}

func TestOversizedMessageRejected(t *testing.T) {
	nk, id, a := fixture(t)
	for i := 0; i < 200; i++ {
		a.Endpoints = append(a.Endpoints, "203.0.113.200:51820")
	}
	if _, err := Seal(nk, 1, id.DevicePriv, a); err == nil {
		t.Fatal("sealed a message larger than the padded size")
	}
}

func TestBadKeyLengthsRejected(t *testing.T) {
	nk, id, a := fixture(t)
	a.WGPub = []byte{1, 2, 3}
	raw, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenAnnounce(nk, 1, raw, time.Now()); err == nil {
		t.Fatal("accepted a bad wireguard key length")
	}
}

// --- replay guard ---

func TestReplayGuardRejectsRewind(t *testing.T) {
	g := NewReplayGuard()
	pub, _, _ := ed25519.GenerateKey(nil)
	a := &Announce{DevicePub: pub, Seq: 5}

	if !g.Accept(a) {
		t.Fatal("first announce rejected")
	}
	if g.Accept(&Announce{DevicePub: pub, Seq: 5}) {
		t.Error("accepted a repeated sequence number")
	}
	if g.Accept(&Announce{DevicePub: pub, Seq: 4}) {
		t.Error("accepted a rewound sequence number — endpoint rollback is possible")
	}
	if !g.Accept(&Announce{DevicePub: pub, Seq: 6}) {
		t.Error("rejected a valid advance")
	}
}

func TestReplayGuardIsPerDevice(t *testing.T) {
	g := NewReplayGuard()
	a, _, _ := ed25519.GenerateKey(nil)
	b, _, _ := ed25519.GenerateKey(nil)

	if !g.Accept(&Announce{DevicePub: a, Seq: 100}) {
		t.Fatal("first announce from A rejected")
	}
	// B's low sequence number must not be judged against A's.
	if !g.Accept(&Announce{DevicePub: b, Seq: 1}) {
		t.Fatal("device B judged against device A's sequence")
	}
}

func TestReplayGuardForget(t *testing.T) {
	g := NewReplayGuard()
	pub, _, _ := ed25519.GenerateKey(nil)
	g.Accept(&Announce{DevicePub: pub, Seq: 9})
	g.Forget(pub)
	if _, ok := g.Seq(pub); ok {
		t.Fatal("Forget did not drop the device")
	}
	if !g.Accept(&Announce{DevicePub: pub, Seq: 1}) {
		t.Fatal("after Forget, a fresh low sequence should be accepted")
	}
}

// The endpoint budget, measured rather than assumed.
//
// An announce is padded to a fixed size and Seal refuses anything larger, so
// every byte a new field adds is taken from the endpoints — and the sender
// trims silently from the end. A node that quietly drops to one endpoint is
// unreachable on its LAN and reads as a network fault, so the cost of adding a
// field belongs in a test rather than in a commit message.
//
// The numbers here are what fits today. If a change moves them, that is the
// change's real price and it should be looked at deliberately.
func TestEndpointBudget(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	devPub, devPriv, _ := ed25519.GenerateKey(nil)
	credRaw, err := cred.IssueFor(admin, auth, devPub, bytes.Repeat([]byte{2}, 32),
		"home-server", 1787464532, time.Now(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var nk identity.NetworkKey
	copy(nk[:], bytes.Repeat([]byte{7}, 32))
	const boot = "/ip4/203.0.113.10/tcp/60000/p2p/16Uiu2HAm56qiyCnUQpoCiEGNj84d3rftQ9a1pVFruHhnWsSNyGRc"

	maxEndpoints := func(withCred bool, bootAddr string) int {
		for n := 0; n < 64; n++ {
			a := Announce{
				Kind: KindAnnounce, DevicePub: devPub,
				WGPub:     bytes.Repeat([]byte{2}, 32),
				Name:      "home-server",
				Seq:       18446744073709551615,
				Timestamp: time.Now().Unix(),
				Relay:     true, Boot: bootAddr,
			}
			for i := 0; i <= n; i++ {
				a.Endpoints = append(a.Endpoints, fmt.Sprintf("178.213.45.2%02d:51820", i))
			}
			if withCred {
				a.Credential = credRaw
			}
			if _, err := Seal(nk, 1, devPriv, a); errors.Is(err, ErrTooLarge) {
				return n
			}
		}
		return 64
	}
	for _, c := range []struct {
		what     string
		withCred bool
		boot     string
		want     int
	}{
		{"no credential, no boot", false, "", 20},
		{"credential, no boot", true, "", 9},
		// The tightest real case, and the one to watch: a Core relay carrying
		// both. Live nodes advertise four endpoints, so this has no slack.
		{"credential and boot", true, boot, 4},
	} {
		if got := maxEndpoints(c.withCred, c.boot); got != c.want {
			t.Errorf("%s: %d endpoints fit, expected %d — a field was added or "+
				"removed; check what it costs the nodes that carry both",
				c.what, got, c.want)
		}
	}
}
