package mesh

import (
	"crypto/ed25519"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/state"
)

func wgOf(b byte) identity.WGKey {
	var k identity.WGKey
	k[0], k[1] = b, 0x9e
	return k
}

// A relay that belongs to this mesh already holds the network key, so
// disguising a tunnel key from it hides nothing and would only be a way to get
// the two ends out of step.
func TestAMemberRelaySeesTheRealKey(t *testing.T) {
	m := &Mesh{blind: false}
	k := wgOf(1)
	if got := m.relayHandle(k); got != k {
		t.Errorf("a member relay was given %x instead of the tunnel key", got[:6])
	}
}

// The property everything rests on: two devices on the same mesh, neither
// having spoken to the other about it, derive the same handle for the same
// peer. Without this they simply cannot address each other through a blind
// relay, and the failure would be silent — packets forwarded to a tag nobody
// registered.
func TestBothEndsDeriveTheSameTag(t *testing.T) {
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	// Two Mesh values standing in for two devices, sharing only what every
	// member shares: the network key.
	a := &Mesh{blind: true, relayKey: relay.DeriveKey(nk)}
	b := &Mesh{blind: true, relayKey: relay.DeriveKey(nk)}

	peer := wgOf(7)
	if a.relayHandle(peer) != b.relayHandle(peer) {
		t.Fatal("two members derived different handles for one peer, so they could never address each other")
	}
	// And it must not be the tunnel key, or the operator learns exactly what
	// the tag exists to withhold.
	if a.relayHandle(peer) == peer {
		t.Error("the handle is the tunnel key")
	}
	// Distinct peers must not collide onto one handle.
	if a.relayHandle(wgOf(7)) == a.relayHandle(wgOf(8)) {
		t.Error("two peers share a handle")
	}
}

// A different mesh derives unrelated handles for the same device, which is what
// stops two relay operators comparing notes and recognising anybody.
func TestADifferentMeshDerivesUnrelatedTags(t *testing.T) {
	one, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	two, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	a := &Mesh{blind: true, relayKey: relay.DeriveKey(one)}
	b := &Mesh{blind: true, relayKey: relay.DeriveKey(two)}

	peer := wgOf(7)
	if a.relayHandle(peer) == b.relayHandle(peer) {
		t.Error("two meshes derived the same handle, so a device is recognisable across them")
	}
}

// The frame key must never be the mesh's own on a blind relay: that key is
// derived from the network key, and handing it to a stranger is the one thing
// this design exists to avoid.
func TestABlindRelayNeverGetsTheMeshKey(t *testing.T) {
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	mesh := relay.DeriveKey(nk)

	for _, tc := range []struct {
		name string
		key  relay.Key
	}{
		{"open", relay.OpenKey()},
		{"token", relay.TokenKey("a token an operator handed out")},
	} {
		if tc.key == mesh {
			t.Errorf("%s frame key equals the mesh relay key", tc.name)
		}
	}
	// And a member relay does use it, since it is a member.
	if mesh != relay.DeriveKey(nk) {
		t.Error("the mesh relay key is not stable")
	}
}

// End to end against a real blind relay: two members of one mesh, a relay that
// holds neither the network key nor any roster, and a packet that arrives.
//
// This is the claim the whole feature makes, and it is worth testing against
// relay.Server rather than a stand-in because the parts that could go wrong are
// exactly the ones a stand-in would get right by construction — that the tags
// both ends derive are the ones the relay installs, and that a frame written
// under the token key is one the relay can read.
func TestTwoMembersRelayThroughAStranger(t *testing.T) {
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	token := "an operator agreed to carry us"
	frameKey := relay.TokenKey(token)

	// The relay: no roster, no network key, nothing but its own token.
	srv := relay.NewServerWith(frameKey, nil, relay.Options{Blind: true})

	a := &Mesh{blind: true, relayKey: relay.DeriveKey(nk), frameKey: frameKey}
	b := &Mesh{blind: true, relayKey: relay.DeriveKey(nk), frameKey: frameKey}
	aWG, bWG := wgOf(1), wgOf(2)

	aAddr := netip.MustParseAddrPort("198.51.100.1:51820")
	bAddr := netip.MustParseAddrPort("203.0.113.2:51820")
	now := time.Now()

	// Each registers under the handle it derives for itself, and answers the
	// routability challenge the way a client must.
	for _, d := range []struct {
		m    *Mesh
		wg   identity.WGKey
		addr netip.AddrPort
	}{{a, aWG, aAddr}, {b, bWG, bAddr}} {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		tag := d.m.relayHandle(d.wg)
		out, to, send := srv.Handle(relay.EncodeRegister(frameKey, tag, priv, now), d.addr, now)
		if !send || to != d.addr {
			t.Fatal("no routability challenge")
		}
		f, err := relay.Decode(frameKey, out)
		if err != nil || f.Type != relay.TypeChallenge {
			t.Fatalf("expected a challenge: %v", err)
		}
		srv.Handle(relay.EncodeConfirm(frameKey, tag, f.Nonce, priv, now), d.addr, now)
	}

	// A addresses B the way the data plane does: by the handle it derives for
	// B, with no knowledge of what B registered.
	payload := []byte("a wireguard packet would be here")
	frame, err := relay.EncodeForward(frameKey, a.relayHandle(bWG), identity.WGKey{}, payload)
	if err != nil {
		t.Fatal(err)
	}
	out, to, ok := srv.Handle(frame, aAddr, now)
	if !ok {
		t.Fatal("the relay did not forward — the handles the two ends derived did not match")
	}
	if to != bAddr {
		t.Errorf("forwarded to %v, want B at %v", to, bAddr)
	}

	// B receives it, and must be able to work out where to reply.
	got, err := relay.Decode(frameKey, out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("payload arrived as %q", got.Payload)
	}
	// The source the relay filled in must be the handle B would itself derive
	// for A — otherwise B replies into the void.
	if got.Src != b.relayHandle(aWG) {
		t.Error("B cannot address A back: the relay's idea of the sender does not match B's")
	}

	// And the relay learned nothing usable: neither real tunnel key appears in
	// anything it holds.
	if a.relayHandle(aWG) == aWG || a.relayHandle(bWG) == bWG {
		t.Error("a real tunnel key was exposed to the relay")
	}
}

// Several relays are only useful if both ends can still meet. A relay forwards
// between peers registered with it, so if each end picked its own favourite
// they would never find each other — which is why registration goes to all of
// them and only the *sender's* choice varies.
func TestSelectionPrefersALiveRelayInConfiguredOrder(t *testing.T) {
	first := netip.MustParseAddrPort("198.51.100.1:31000")
	second := netip.MustParseAddrPort("198.51.100.2:31000")
	third := netip.MustParseAddrPort("198.51.100.3:31000")
	now := time.Now()

	m := &Mesh{relays: []relayTarget{{addr: first}, {addr: second}, {addr: third}}}

	// Nothing has answered: the operator's first choice stands, so a fresh
	// device is not paralysed waiting for a signal it has never had.
	if got := m.selectRelay(now); !got.ok || got.addr != first {
		t.Fatalf("with nothing live, chose %v, want %v", got.addr, first)
	}

	// Only the third is answering. Order is preserved among the live, so every
	// device given this list makes the same call.
	m.relayLive = map[netip.AddrPort]time.Time{third: now}
	if got := m.selectRelay(now); got.addr != third {
		t.Errorf("chose %v, want the only live relay %v", got.addr, third)
	}

	// The second comes back; it is earlier in the operator's order, so it wins.
	m.relayLive[second] = now
	if got := m.selectRelay(now); got.addr != second {
		t.Errorf("chose %v, want the earliest live relay %v", got.addr, second)
	}

	// And a relay that stopped answering is no longer live.
	m.relayLive = map[netip.AddrPort]time.Time{second: now.Add(-RelayLiveFor - time.Minute)}
	if got := m.selectRelay(now); got.addr != first {
		t.Errorf("a stale relay was still chosen: %v", got.addr)
	}
}

// Registering with every relay on a list is antisocial: these are machines
// other people pay for, and a slot on a relay carrying none of your traffic is
// pure imposition. So the number is bounded, and what it spends the quota on is
// the relay a sender would actually choose plus one spare.
func TestRegistrationIsBoundedAndPrefersLiveRelays(t *testing.T) {
	pins := []netip.AddrPort{
		netip.MustParseAddrPort("198.51.100.1:31000"),
		netip.MustParseAddrPort("198.51.100.2:31000"),
		netip.MustParseAddrPort("198.51.100.3:31000"),
		netip.MustParseAddrPort("198.51.100.4:31000"),
		netip.MustParseAddrPort("198.51.100.5:31000"),
	}
	now := time.Now()
	m := &Mesh{}
	for _, p := range pins {
		m.relays = append(m.relays, relayTarget{addr: p, blind: true})
	}

	// Nothing known yet: a fresh device must still register somewhere, or it is
	// unreachable until something tells it which relays work — and nothing
	// will, because nothing can reach it.
	got := m.registerWith(now)
	if len(got) != maxRelayRegistrations {
		t.Fatalf("a fresh device registered with %d relays, want %d", len(got), maxRelayRegistrations)
	}
	if got[0].addr != pins[0] {
		t.Errorf("started at %v rather than the operator's first choice", got[0])
	}

	// With a late one live, the quota goes to it — that is the relay a sender
	// will choose, so it is the one this device must be reachable on.
	m.relayLive = map[netip.AddrPort]time.Time{pins[3]: now}
	got = m.registerWith(now)
	if len(got) != maxRelayRegistrations {
		t.Fatalf("registered with %d relays, want %d", len(got), maxRelayRegistrations)
	}
	if got[0].addr != pins[3] {
		t.Errorf("did not register with the live relay first: %v", got)
	}
	// And selection agrees with registration, or a device is reachable
	// somewhere it does not send.
	if sel := m.selectRelay(now); sel.addr != got[0].addr {
		t.Errorf("sends via %v but registered first with %v", sel.addr, got[0].addr)
	}

	// Never more than the cap, however many are live.
	for _, p := range pins {
		m.relayLive[p] = now
	}
	if got := m.registerWith(now); len(got) != maxRelayRegistrations {
		t.Errorf("with everything live, registered with %d relays", len(got))
	}
}

// Both kinds at once, which is the ordinary state while moving off a relay of
// your own: keep the VPS listed and try other people's alongside it.
//
// The keys and the handles must differ per relay, and getting that wrong is
// invisible — a frame authenticated under the wrong key is simply dropped at
// the far end, with nothing said by anybody.
func TestMemberAndBlindRelaysCoexist(t *testing.T) {
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	meshKey := relay.DeriveKey(nk)
	blindKey := relay.TokenKey("somebody else's token")

	mine := netip.MustParseAddrPort("198.51.100.1:51820")
	theirs := netip.MustParseAddrPort("203.0.113.9:31760")

	m := &Mesh{
		relayKey: meshKey,
		relays: []relayTarget{
			{addr: mine, key: meshKey},
			{addr: theirs, key: blindKey, blind: true},
		},
	}
	self := wgOf(1)

	// A relay of ours sees the real tunnel key, under the mesh's own key.
	own, ok := m.targetFor(mine)
	if !ok {
		t.Fatal("our own relay was not recognised as configured")
	}
	if own.key != meshKey {
		t.Error("a member relay was given something other than the mesh key")
	}
	if m.handleFor(own, self) != self {
		t.Error("a member relay was given a tag instead of the tunnel key")
	}

	// A stranger's sees neither.
	other, ok := m.targetFor(theirs)
	if !ok {
		t.Fatal("the blind relay was not recognised as configured")
	}
	if other.key == meshKey {
		t.Fatal("a stranger's relay was given the mesh key")
	}
	if h := m.handleFor(other, self); h == self {
		t.Error("a stranger's relay was given the real tunnel key")
	}

	// An address we never configured is treated as a discovered member relay,
	// which is the only kind discovery can find.
	if _, ok := m.targetFor(netip.MustParseAddrPort("192.0.2.1:1")); ok {
		t.Error("an unconfigured address was reported as one of ours")
	}

	// Our own relay comes first, so it is preferred over a stranger's while
	// both are available.
	if got := m.selectRelay(time.Now()); got.addr != mine {
		t.Errorf("preferred %v over our own relay", got.addr)
	}
}

// rosterClaiming builds a roster whose single peer announces these endpoints.
func rosterClaiming(t *testing.T, endpoints ...string) *Roster {
	t.Helper()
	pub := pubOfMesh(t, 9)
	return &Roster{peers: map[string]PeerInfo{
		"peer": {DevicePub: pub, Name: "other", Endpoints: endpoints},
	}}
}

// meshWithRoster is enough of a Mesh to ask what it would announce: candidates()
// consults the prober, so a real one is needed even with nothing in it.
func meshWithRoster(t *testing.T, port uint16, r *Roster) *Mesh {
	t.Helper()
	var k disco.Key
	return &Mesh{
		cfg:    state.Config{ListenPort: port},
		roster: r,
		prober: disco.NewProber(k, make([]byte, 64), func([]byte, netip.AddrPort) error { return nil }),
	}
}

func pubOfMesh(t *testing.T, n byte) ed25519.PublicKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = n
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
}

// Two nodes cannot both be behind one address and port. When a peer already
// claims one of ours, at most one of us is right and neither knows which — so
// announcing it is worse than staying quiet: peers probe it, the wrong node
// answers, the signature check rejects the reply, and the path flaps.
//
// A home router really did grant the same NAT-PMP external port to two machines,
// which is what makes this worth defending against rather than assuming away.
func TestAnAddressAPeerClaimsIsNotAnnounced(t *testing.T) {
	contested := "178.213.45.235:51821"
	m := meshWithRoster(t, 51821, rosterClaiming(t, contested))
	m.mapped = netip.MustParseAddrPort(contested)

	for _, c := range m.candidates() {
		if c == contested {
			t.Fatalf("announced %s while a peer also claims it", contested)
		}
	}
}

// An uncontested mapping is still announced, or the defence would cost every
// node behind a working router its only public address.
func TestAnUncontestedMappingIsStillAnnounced(t *testing.T) {
	mine := "178.213.45.235:51820"
	m := meshWithRoster(t, 51820, rosterClaiming(t, "178.213.45.235:51821"))
	m.mapped = netip.MustParseAddrPort(mine)

	found := false
	for _, c := range m.candidates() {
		if c == mine {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped %s although no peer claims it: %v", mine, m.candidates())
	}
}

// A peer's private address says nothing about ours. Two machines on one LAN
// share a subnet, and treating that as a conflict would strip the LAN address
// from every node in the building — the addresses most likely to work.
func TestAPeersPrivateAddressIsNotAConflict(t *testing.T) {
	m := meshWithRoster(t, 51820, rosterClaiming(t, "192.168.0.209:51820"))
	// candidates() adds local addresses itself; the point is that a private
	// address from a peer never enters the claimed set at all.
	if m.claimedByPeers()["192.168.0.209:51820"] {
		t.Error("a peer's private address was treated as claiming ours")
	}
}
