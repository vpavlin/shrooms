package relay

import (
	"crypto/ed25519"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

// blindServer is a relay open to anyone, which is the configuration the whole
// design has to survive.
func blindServer(t *testing.T, opts Options) (*Server, Key) {
	t.Helper()
	opts.Blind = true
	opts.Open = true
	k := OpenKey()
	// No owns function, deliberately: a blind relay has no roster to ask. That
	// is the constraint, not a shortcut taken by the test.
	return NewServerWith(k, nil, opts), k
}

// registered reports whether a handle is installed, and where.
func registered(s *Server, h identity.WGKey) (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.peers[h]
	return r.addr, ok
}

// The reason an open forwarder is normally dangerous is reflection: point it at
// a third party and it sends traffic there for you. A registration must
// therefore install nothing until the registrant proves it receives at the
// address it gave.
func TestABlindRelayInstallsNothingBeforeTheAddressAnswers(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, to, send := s.Handle(EncodeRegister(k, wg, priv, now), here, now)
	if _, ok := registered(s, wg); ok {
		t.Fatal("a bare registration was installed without any proof of routability")
	}
	if !send {
		t.Fatal("the relay sent no challenge")
	}
	if to != here {
		t.Errorf("the challenge went to %v, not to the address registering (%v)", to, here)
	}
	f, err := Decode(k, out)
	if err != nil || f.Type != TypeChallenge {
		t.Fatalf("expected a challenge, got %v (%v)", f, err)
	}

	// Echo it, and only now does the mapping exist.
	if _, _, send := s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), here, now); send {
		t.Error("the confirm produced a reply, which it should not")
	}
	at, ok := registered(s, wg)
	if !ok {
		t.Fatal("a confirmed registration was not installed")
	}
	if at != here {
		t.Errorf("installed against %v, want %v", at, here)
	}
}

// The attack the challenge exists to stop: an attacker registers a victim's
// address so the relay floods the victim with forwarded traffic. The nonce goes
// to the address being registered, so the attacker never sees it.
func TestABlindRelayCannotBePointedAtAThirdParty(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	victim := netip.MustParseAddrPort("203.0.113.9:9")
	now := time.Now()

	// The attacker registers from its own socket; the relay answers there,
	// which is the point — it will not take the victim's address on trust.
	attacker := netip.MustParseAddrPort("198.51.100.66:51820")
	out, to, _ := s.Handle(EncodeRegister(k, wg, priv, now), attacker, now)
	if to == victim {
		t.Fatal("the relay sent a challenge to an address it was merely told about")
	}

	// And guessing is the only way in. A confirm with the wrong nonce, from the
	// victim's address or any other, installs nothing.
	f, _ := Decode(k, out)
	var wrong [NonceLen]byte
	wrong[0] = f.Nonce[0] ^ 0xff
	s.Handle(EncodeConfirm(k, wg, wrong, priv, now), attacker, now)
	if _, ok := registered(s, wg); ok {
		t.Fatal("a wrong nonce installed a registration")
	}
	// The real nonce from a *different* address must not work either, since
	// the pending entry is keyed by the address that has to answer.
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), victim, now)
	if at, ok := registered(s, wg); ok {
		t.Fatalf("a confirm from an unchallenged address installed %v", at)
	}
}

// First claim wins. Once a device holds a handle nobody else can take it, which
// is what a blind relay has instead of a roster.
func TestFirstClaimKeepsTheHandle(t *testing.T) {
	s, k := blindServer(t, Options{})
	mine, wg := deviceAndKey(t, 1)
	theirs, _ := deviceAndKey(t, 2)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	elsewhere := netip.MustParseAddrPort("198.51.100.77:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, mine, now), here, now)
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, mine, now), here, now)

	// Another device, correctly signing for itself, claiming the same handle.
	// It cannot even get a challenge: the claim is refused before that.
	before := s.Stats().Refused
	if _, _, send := s.Handle(EncodeRegister(k, wg, theirs, now), elsewhere, now); send {
		t.Error("a second device was challenged for a handle it does not own")
	}
	if s.Stats().Refused == before {
		t.Error("the second claim was not counted as refused")
	}
	if at, _ := registered(s, wg); at != here {
		t.Errorf("the handle moved to %v", at)
	}
}

// A registration refreshes every few tens of seconds. Challenging each time
// would triple the cost of holding one open, and answers a question this
// address has already answered.
func TestRefreshingAnUnchangedMappingNeedsNoChallenge(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, priv, now), here, now)
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), here, now)

	later := now.Add(30 * time.Second)
	if _, _, send := s.Handle(EncodeRegister(k, wg, priv, later), here, later); send {
		t.Error("an unchanged refresh was challenged again")
	}
	if _, ok := registered(s, wg); !ok {
		t.Error("the refresh dropped the registration")
	}
}

// A device that moves — a phone changing network, a NAT rebinding — is a new
// binding and must prove the new address before traffic follows it there.
func TestMovingToANewAddressIsChallengedAgain(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	first := netip.MustParseAddrPort("198.51.100.10:51820")
	second := netip.MustParseAddrPort("198.51.100.11:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, priv, now), first, now)
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), first, now)

	out2, to, send := s.Handle(EncodeRegister(k, wg, priv, now), second, now)
	if !send || to != second {
		t.Fatal("moving address produced no challenge")
	}
	if at, _ := registered(s, wg); at != first {
		t.Error("the mapping moved before the new address answered")
	}
	f2, _ := Decode(k, out2)
	s.Handle(EncodeConfirm(k, wg, f2.Nonce, priv, now), second, now)
	if at, _ := registered(s, wg); at != second {
		t.Errorf("the mapping did not follow to %v", second)
	}
}

// The table cap alone is not enough once a relay is open: one host with a range
// of ports can fill it, and every entry it takes is a device the relay exists
// to carry and now cannot.
func TestOneSourceCannotTakeTheTable(t *testing.T) {
	s, k := blindServer(t, Options{MaxPerSource: 2})
	now := time.Now()

	installed := 0
	for i := 0; i < 6; i++ {
		priv, wg := deviceAndKey(t, byte(i+1))
		from := netip.AddrPortFrom(netip.MustParseAddr("198.51.100.5"), uint16(30000+i))
		out, _, send := s.Handle(EncodeRegister(k, wg, priv, now), from, now)
		if !send {
			continue // refused before a challenge, which is the cap working
		}
		f, _ := Decode(k, out)
		s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), from, now)
		if _, ok := registered(s, wg); ok {
			installed++
		}
	}
	if installed > 2 {
		t.Errorf("one source holds %d registrations, cap was 2", installed)
	}
	// And a different source is unaffected.
	priv, wg := deviceAndKey(t, 99)
	other := netip.MustParseAddrPort("203.0.113.5:51820")
	out, _, send := s.Handle(EncodeRegister(k, wg, priv, now), other, now)
	if !send {
		t.Fatal("a different source was refused because of somebody else's usage")
	}
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), other, now)
	if _, ok := registered(s, wg); !ok {
		t.Error("a different source could not register")
	}
}

// admit runs the full exchange, for tests about what happens afterwards.
func admit(t *testing.T, s *Server, k Key, n byte, from netip.AddrPort, now time.Time) identity.WGKey {
	t.Helper()
	priv, wg := deviceAndKey(t, n)
	out, _, send := s.Handle(EncodeRegister(k, wg, priv, now), from, now)
	if !send {
		t.Fatal("no challenge")
	}
	f, err := Decode(k, out)
	if err != nil {
		t.Fatal(err)
	}
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), from, now)
	if _, ok := registered(s, wg); !ok {
		t.Fatal("registration did not install")
	}
	return wg
}

// The operator's bandwidth is what is left to abuse once reflection is closed,
// so a ceiling has to actually stop traffic — and be visible as throttling
// rather than as the relay looking broken.
func TestForwardingRespectsTheOperatorsCeiling(t *testing.T) {
	// A tiny allowance, and no burst beyond one second's worth.
	s, k := blindServer(t, Options{BytesPerSecond: 4096, BurstSeconds: 1})
	now := time.Now()

	a := netip.MustParseAddrPort("198.51.100.10:51820")
	b := netip.MustParseAddrPort("203.0.113.10:51820")
	src := admit(t, s, k, 1, a, now)
	dst := admit(t, s, k, 2, b, now)
	_ = src

	payload := make([]byte, 1024)
	sent, blocked := 0, 0
	for i := 0; i < 20; i++ {
		frame, err := EncodeForward(k, dst, identity.WGKey{}, payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := s.Handle(frame, a, now); ok {
			sent++
		} else {
			blocked++
		}
	}
	if blocked == 0 {
		t.Error("the ceiling never refused a packet")
	}
	if sent == 0 {
		t.Error("the ceiling refused everything, which is a broken relay rather than a limited one")
	}
	if st := s.Stats(); st.Throttled == 0 {
		t.Error("throttling was not counted, so an operator cannot tell it apart from a fault")
	}
	// And it recovers: the bucket refills, so this is a limit rather than a
	// permanent cut-off.
	later := now.Add(2 * time.Second)
	frame, _ := EncodeForward(k, dst, identity.WGKey{}, payload)
	if _, _, ok := s.Handle(frame, a, later); !ok {
		t.Error("the bucket did not refill")
	}
}

// The tag is what keeps the operator from learning anything: both ends of a
// mesh derive the same one, and it reveals nothing about the tunnel key.
func TestTagsAgreeAcrossTheMeshAndHideTheKey(t *testing.T) {
	meshKey := testKey(t)
	otherMesh := testKey(t)
	wg := wgKey(7)

	at := netip.MustParseAddrPort("198.51.100.1:31760")
	other := netip.MustParseAddrPort("203.0.113.9:31760")
	if Tag(meshKey, at, wg) != Tag(meshKey, at, wg) {
		t.Fatal("a tag is not stable, so two devices could not address each other")
	}
	if Tag(meshKey, at, wg) == wg {
		t.Fatal("the tag is the tunnel key, which defeats the point")
	}
	// A second mesh sees an unrelated value.
	if Tag(meshKey, at, wg) == Tag(otherMesh, at, wg) {
		t.Fatal("two meshes derive the same tag, so operators could correlate devices")
	}
	// And so does a second *relay*, which is the property the comments claimed
	// before it was true. Without the address in the derivation a device wears
	// one handle everywhere, and two operators comparing notes can link it —
	// and by extension its whole mesh — across their relays.
	if Tag(meshKey, at, wg) == Tag(meshKey, other, wg) {
		t.Fatal("the same device has one tag on every relay; operators can correlate it")
	}
	if Tag(meshKey, at, wg) == Tag(meshKey, at, wgKey(8)) {
		t.Fatal("two devices collide on one tag")
	}
}

// An open relay's key is public by construction. That is intended, but it must
// be true rather than accidentally true, or the design rests on a secret that
// is not one.
func TestAnOpenRelayKeyIsReproducible(t *testing.T) {
	if OpenKey() != OpenKey() {
		t.Fatal("OpenKey is not deterministic, so clients could not agree on it")
	}
	if TokenKey("a") == TokenKey("b") {
		t.Fatal("different tokens produce the same key")
	}
	if TokenKey("a") == OpenKey() {
		t.Fatal("a token produces the open key, so a token would grant nothing")
	}
}

// A stale confirm must not install anything, or a captured exchange is
// replayable for as long as the attacker likes.
func TestAStaleConfirmIsRefused(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, priv, now), here, now)
	f, _ := Decode(k, out)

	// Answered after the challenge has expired. Well past, because a challenge
	// carries an era rather than a deadline and is accepted in that era and the
	// one before — so the window is up to twice cookieEra.
	late := now.Add(ChallengeTTL + time.Second)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, late), here, late)
	if _, ok := registered(s, wg); ok {
		t.Error("an expired challenge was still answerable")
	}
}

// A confirm signed by a device other than the one that registered must fail,
// or the signature on the register frame buys nothing.
func TestAConfirmBySomebodyElseIsRefused(t *testing.T) {
	s, k := blindServer(t, Options{})
	mine, wg := deviceAndKey(t, 1)
	theirs, _ := deviceAndKey(t, 2)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, mine, now), here, now)
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, theirs, now), here, now)
	if _, ok := registered(s, wg); ok {
		t.Error("a confirm signed by a different device installed the registration")
	}
}

// A mesh-member relay must behave exactly as it did: same one-step
// registration, no challenge, no change for anybody already running one.
func TestAMemberRelayIsUnchanged(t *testing.T) {
	k := testKey(t)
	s := NewServer(k, nil)
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	if _, _, send := s.Handle(EncodeRegister(k, wg, priv, now), here, now); send {
		t.Error("a member relay issued a challenge")
	}
	if _, ok := registered(s, wg); !ok {
		t.Error("a member relay did not install a registration in one step")
	}
}

// A device on an ed25519 key it does not hold cannot register at all. Checked
// on the blind path specifically, since that is the one with no roster behind
// it.
func TestAForgedSignatureNeverReachesTheTable(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	frame := EncodeRegister(k, wg, priv, now)
	// Corrupt the signature, then repair the MAC so only the signature is
	// wrong — otherwise this tests the MAC and not the thing it claims to.
	sigAt := 1 + keyLen + stampLen + devicePubLen
	frame[sigAt] ^= 0xff
	copy(frame[sigAt+sigLen:], mac(k, frame[:sigAt+sigLen]))

	if _, _, send := s.Handle(frame, here, now); send {
		t.Error("a forged registration was challenged")
	}
	if _, ok := registered(s, wg); ok {
		t.Error("a forged registration was installed")
	}
	_ = ed25519.PublicKey(nil)
}

// A device that moves to a new source port is challenged again and, on
// answering, refreshes the entry it already had rather than creating one. The
// relay must not mistake that for an unanswered challenge.
//
// It did. The first version of the statistics inferred answered challenges from
// the count of *new* registrations, so every phone changing network and every
// run of the probe inflated a figure meant to mean "something cannot receive".
// A relay answering every challenge put to it reported dozens outstanding.
func TestAnsweredChallengesAreCountedNotInferred(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	now := time.Now()

	// Register, then re-register from a series of new ports, as a device that
	// keeps changing network does.
	for i := 0; i < 4; i++ {
		from := netip.AddrPortFrom(netip.MustParseAddr("198.51.100.10"), uint16(40000+i))
		out, _, send := s.Handle(EncodeRegister(k, wg, priv, now), from, now)
		if !send {
			t.Fatalf("attempt %d was not challenged", i)
		}
		f, err := Decode(k, out)
		if err != nil {
			t.Fatal(err)
		}
		s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), from, now)
	}

	st := s.Stats()
	if st.Challenged != 4 {
		t.Errorf("issued %d challenges, want 4", st.Challenged)
	}
	if st.Confirmed != st.Challenged {
		t.Errorf("answered %d of %d challenges; every one was answered",
			st.Confirmed, st.Challenged)
	}
	// And only one device is registered throughout: these were refreshes.
	if st.Peers != 1 {
		t.Errorf("holding %d registrations, want 1", st.Peers)
	}
	if st.Registered != 1 {
		t.Errorf("counted %d new registrations, want 1 — the rest were refreshes", st.Registered)
	}
}

// A register's signature covers the frame, not the address it arrived from. So
// one keypair and one signed register can be replayed from as many spoofed
// sources as an attacker's network permits — and on an open relay the frame key
// is public, so nothing has to be stolen first.
//
// The first version allocated a pending-challenge entry per arrival, held for
// the challenge lifetime. This asserts the relay now keeps nothing.
func TestASpoofedRegisterFloodCostsNoMemory(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	now := time.Now()
	frame := EncodeRegister(k, wg, priv, now)

	for i := 0; i < 5000; i++ {
		from := netip.AddrPortFrom(
			netip.AddrFrom4([4]byte{198, 51, byte(i / 256), byte(i % 256)}),
			uint16(30000+i%1000))
		s.Handle(frame, from, now)
	}

	// Nothing is registered — none of them answered — and, more to the point,
	// nothing is being held on their behalf.
	s.mu.Lock()
	peers := len(s.peers)
	sources := len(s.perSource)
	s.mu.Unlock()
	if peers != 0 {
		t.Errorf("%d registrations from unanswered challenges", peers)
	}
	if sources != 0 {
		t.Errorf("%d source entries held for addresses that never confirmed", sources)
	}
}

// The cookie has to be bound to what it is a challenge about, or an attacker
// collects one for a handle they own and answers it for somebody else's.
func TestAChallengeCannotBeReusedForAnotherHandle(t *testing.T) {
	s, k := blindServer(t, Options{})
	mine, myWG := deviceAndKey(t, 1)
	victimWG := wgKey(9)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	now := time.Now()

	out, _, send := s.Handle(EncodeRegister(k, myWG, mine, now), here, now)
	if !send {
		t.Fatal("no challenge for our own handle")
	}
	f, err := Decode(k, out)
	if err != nil {
		t.Fatal(err)
	}
	// Answer it naming a different handle.
	s.Handle(EncodeConfirm(k, victimWG, f.Nonce, mine, now), here, now)
	if _, ok := registered(s, victimWG); ok {
		t.Error("a challenge issued for one handle installed another")
	}
}

// And bound to the address, so a cookie collected at one cannot be replayed
// from another — which is the reflection defence itself.
func TestAChallengeCannotBeReplayedFromElsewhere(t *testing.T) {
	s, k := blindServer(t, Options{})
	priv, wg := deviceAndKey(t, 1)
	here := netip.MustParseAddrPort("198.51.100.10:51820")
	elsewhere := netip.MustParseAddrPort("203.0.113.9:9")
	now := time.Now()

	out, _, _ := s.Handle(EncodeRegister(k, wg, priv, now), here, now)
	f, _ := Decode(k, out)
	s.Handle(EncodeConfirm(k, wg, f.Nonce, priv, now), elsewhere, now)
	if at, ok := registered(s, wg); ok {
		t.Errorf("a challenge from %v was answered from %v and installed %v", here, elsewhere, at)
	}
}

// Answers to registers and probes are small, but a relay that emits them
// without limit spends an operator's uplink for free.
func TestControlAnswersAreRateLimited(t *testing.T) {
	// A tiny total, so the twentieth given to control is tinier still.
	s, k := blindServer(t, Options{BytesPerSecond: 2000, BurstSeconds: 1})
	priv, wg := deviceAndKey(t, 1)
	now := time.Now()

	answered, refused := 0, 0
	for i := 0; i < 200; i++ {
		from := netip.AddrPortFrom(netip.MustParseAddr("198.51.100.5"), uint16(30000+i))
		if _, _, send := s.Handle(EncodeRegister(k, wg, priv, now), from, now); send {
			answered++
		} else {
			refused++
		}
	}
	if refused == 0 {
		t.Error("the relay answered every register with no ceiling on the total")
	}
	if answered == 0 {
		t.Error("the relay answered none, which is a broken relay rather than a limited one")
	}
	if s.Stats().Throttled == 0 {
		t.Error("throttling was not counted, so an operator cannot see it")
	}
}
