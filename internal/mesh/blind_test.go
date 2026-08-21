package mesh

import (
	"crypto/ed25519"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
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
