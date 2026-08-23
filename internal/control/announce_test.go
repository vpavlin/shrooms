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
// trims silently from the end. Endpoints are ordered by usefulness, so those
// bytes are paid in LAN and local addresses: the ones two machines on the same
// wifi need to find each other. That cost belongs in a test rather than in a
// commit message.
//
// Measure a candidate field THROUGH Seal, by adding the field. Simulating it by
// padding a string puts the bytes in the inner JSON, which the envelope
// base64-encodes a second time, and the answer comes out about a third too
// pessimistic — which is how a proposed field was once talked out of on numbers
// that were wrong.
//
// The numbers here are what fits today. If a change moves them, that is the
// change's real price and it should be looked at deliberately.
func TestEndpointBudget(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	devPub, devPriv, _ := ed25519.GenerateKey(nil)
	// A version 2 credential, carrying a sealing key, because that is what a
	// device will actually announce once this is deployed.
	credRaw, err := cred.IssueFor(admin, auth, devPub, bytes.Repeat([]byte{2}, 32),
		bytes.Repeat([]byte{3}, 32), "home-server", 1787464532, time.Now(), 30*24*time.Hour)
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
	// Both framings, because the second is where this is going and the gap
	// between them is the argument for flipping emitCompact.
	for _, c := range []struct {
		what     string
		withCred bool
		boot     string
		want     int // legacy framing
		compact  int
	}{
		{"no credential, no boot", false, "", 20, 31},
		{"credential, no boot", true, "", 7, 19},
		// The tightest real case, and the one to watch: a Core relay carrying
		// both. Live nodes advertise four endpoints, so under the legacy
		// framing this has no slack at all — measure any new field against this
		// row, through Seal rather than by padding a string, or the envelope's
		// second base64 pass makes the answer about a third too pessimistic.
		{"credential and boot", true, boot, 3, 14},
	} {
		emitCompact = false
		legacy := maxEndpoints(c.withCred, c.boot)
		emitCompact = true
		compact := maxEndpoints(c.withCred, c.boot)
		emitCompact = false

		if legacy != c.want {
			t.Errorf("%s: %d endpoints fit under the legacy framing, expected %d "+
				"— a field was added or removed; check what it costs the nodes "+
				"that carry both", c.what, legacy, c.want)
		}
		if compact != c.compact {
			t.Errorf("%s: %d endpoints fit under the compact framing, expected %d",
				c.what, compact, c.compact)
		}
	}
}

// Readers must accept both framings, or moving the senders is a flag day and a
// node that cannot parse an announce goes deaf.
func TestBothEnvelopeFramingsRoundTrip(t *testing.T) {
	nk, id, a := fixture(t)
	for _, compact := range []bool{false, true} {
		emitCompact = compact
		sealed, err := Seal(nk, 1, id.DevicePriv, a)
		if err != nil {
			t.Fatalf("compact=%v: seal: %v", compact, err)
		}
		got, err := OpenAnnounce(nk, 1, sealed, time.Now())
		if err != nil {
			t.Fatalf("compact=%v: open: %v", compact, err)
		}
		if got.Name != a.Name || len(got.Endpoints) != len(a.Endpoints) {
			t.Errorf("compact=%v: round trip lost content", compact)
		}
	}
	emitCompact = false
}

// A tampered body must still be caught under the compact framing: the signature
// is moved, not dropped.
func TestCompactFramingStillVerifies(t *testing.T) {
	nk, id, a := fixture(t)
	emitCompact = true
	defer func() { emitCompact = false }()

	sealed, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatal(err)
	}
	// Forge one signed by somebody else, framed compactly.
	other, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	forged, err := Seal(nk, 1, other.DevicePriv, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAnnounce(nk, 1, forged, time.Now()); err == nil {
		t.Error("accepted an announce signed by a key it does not name")
	}
	if _, err := OpenAnnounce(nk, 1, sealed, time.Now()); err != nil {
		t.Errorf("rejected a good one: %v", err)
	}
}

// What the compact framing is for. The legacy framing is JSON around a body
// that is itself JSON, so the body is base64-encoded a second time on the way
// out, and the signature with it.
func TestCompactFramingBuysEndpoints(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	devPub, devPriv, _ := ed25519.GenerateKey(nil)
	// A version 2 credential, carrying a sealing key, because that is what a
	// device will actually announce once this is deployed.
	credRaw, err := cred.IssueFor(admin, auth, devPub, bytes.Repeat([]byte{2}, 32),
		bytes.Repeat([]byte{3}, 32), "home-server", 1787464532, time.Now(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var nk identity.NetworkKey
	copy(nk[:], bytes.Repeat([]byte{7}, 32))
	const boot = "/ip4/203.0.113.10/tcp/60000/p2p/16Uiu2HAm56qiyCnUQpoCiEGNj84d3rftQ9a1pVFruHhnWsSNyGRc"

	maxEnd := func() int {
		for n := 0; n < 64; n++ {
			a := Announce{
				Kind: KindAnnounce, DevicePub: devPub,
				WGPub: bytes.Repeat([]byte{2}, 32), Name: "home-server",
				Seq: 1 << 62, Timestamp: time.Now().Unix(),
				Relay: true, Boot: boot, Credential: credRaw,
			}
			for i := 0; i <= n; i++ {
				a.Endpoints = append(a.Endpoints, fmt.Sprintf("178.213.45.2%02d:51820", i))
			}
			if _, err := Seal(nk, 1, devPriv, a); errors.Is(err, ErrTooLarge) {
				return n
			}
		}
		return 64
	}
	emitCompact = false
	legacy := maxEnd()
	emitCompact = true
	compact := maxEnd()
	emitCompact = false

	// The tight case — a Core relay carrying a credential and a bootstrap
	// address — is where the budget actually bites.
	if compact <= legacy {
		t.Errorf("compact framing bought nothing: %d endpoints vs %d", compact, legacy)
	}
	t.Logf("Core relay endpoints: %d legacy -> %d compact (+%d)", legacy, compact, compact-legacy)
}

// Generation zero must derive exactly the key it always did.
//
// A golden vector, because this is the compatibility promise the whole rollout
// rests on: a mesh that has never rotated keeps reading and writing as before,
// so senders and readers can be updated in any order. If this value ever moves,
// every node in the field goes deaf at once — and nothing else in the test
// suite would notice, because both sides of every other test would move
// together.
func TestGenerationZeroDerivationIsPinned(t *testing.T) {
	var nk identity.NetworkKey
	copy(nk[:], bytes.Repeat([]byte{0xab}, 32))
	const want = "f8e599d6e9adbf10ada887c613016a66a90623ae7f9d12624bf4b951daac2663"
	if got := fmt.Sprintf("%x", epochKey(nk, 12345, nil)); got != want {
		t.Errorf("generation zero derivation changed:\n got  %s\n want %s\n"+
			"every existing node reads announces with the old value", got, want)
	}
	// An empty non-nil generation is still generation zero.
	if got := fmt.Sprintf("%x", epochKey(nk, 12345, []byte{})); got != want {
		t.Error("an empty generation was not treated as generation zero")
	}
}

// A generation must actually change the key, or rotation is decoration.
func TestAGenerationChangesTheKey(t *testing.T) {
	var nk identity.NetworkKey
	copy(nk[:], bytes.Repeat([]byte{0xab}, 32))
	zero := epochKey(nk, 12345, nil)
	a := epochKey(nk, 12345, bytes.Repeat([]byte{1}, 32))
	b := epochKey(nk, 12345, bytes.Repeat([]byte{2}, 32))

	if bytes.Equal(zero, a) {
		t.Error("a generation secret did not change the key")
	}
	if bytes.Equal(a, b) {
		t.Error("two different generations derived the same key")
	}
}

// The point of the whole exercise: a holder of the network key who does not
// have the current generation cannot read what is sealed under it.
func TestTheNetworkKeyAloneCannotOpenARotatedMesh(t *testing.T) {
	nk, id, a := fixture(t)
	gen := bytes.Repeat([]byte{4}, 32)
	rotated := NewKeyring(nk, gen)

	sealed, err := rotated.Seal(1, id.DevicePriv, a)
	if err != nil {
		t.Fatal(err)
	}
	// The revoked device: it kept nk, and it is at generation zero.
	if _, err := OpenAnnounce(nk, 1, sealed, time.Now()); err == nil {
		t.Error("the network key alone opened an announce from a rotated mesh")
	}
	// A member on the wrong generation fares no better.
	if _, err := NewKeyring(nk, bytes.Repeat([]byte{5}, 32)).OpenAnnounce(1, sealed, time.Now()); err == nil {
		t.Error("the wrong generation opened it")
	}
	// The current one works.
	if _, err := rotated.OpenAnnounce(1, sealed, time.Now()); err != nil {
		t.Errorf("the current generation could not open it: %v", err)
	}
}
