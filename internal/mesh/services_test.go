package mesh

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// A list from a device on the roster is kept. The roster entry is what makes it
// admissible: it is the outcome of an announce whose credential was checked.
func TestServicesFromAKnownPeerAreKept(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	now := time.Now()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "nas", nil, 1), now)

	m.handleServices(&control.Services{
		Kind: control.KindServices, DevicePub: peer.DevicePub,
		Names: []string{"immich", "jellyfin"}, Timestamp: now.Unix(),
	}, now)

	got := m.Services(now)[hex.EncodeToString(peer.DevicePub)]
	if len(got) != 2 || got[0] != "immich" || got[1] != "jellyfin" {
		t.Errorf("services are %v", got)
	}
}

// A device we have not admitted can put a message on the bus, and nothing of
// it is ever shown.
func TestServicesFromAStrangerAreIgnored(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	stranger, _ := identity.New()
	now := time.Now()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.handleServices(&control.Services{
		Kind: control.KindServices, DevicePub: stranger.DevicePub,
		Names: []string{"immich"}, Timestamp: now.Unix(),
	}, now)

	if len(m.Services(now)) != 0 {
		t.Error("showed a service list from a device that is not on the roster")
	}
}

// A list that arrives before that peer's first announce must survive to be
// shown once the announce lands. Dropping it meant waiting five minutes for
// the next repeat, which after a reconnect is most of the time somebody spends
// wondering why the feature does nothing.
func TestServicesArrivingBeforeTheAnnounceAreKept(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	now := time.Now()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.handleServices(&control.Services{
		Kind: control.KindServices, DevicePub: peer.DevicePub,
		Names: []string{"immich"}, Timestamp: now.Unix(),
	}, now)
	if len(m.Services(now)) != 0 {
		t.Fatal("shown before the peer was admitted")
	}

	m.roster.Apply(newAnnounce(t, peer, "nas", nil, 1), now)
	if got := m.Services(now)[hex.EncodeToString(peer.DevicePub)]; len(got) != 1 {
		t.Errorf("the list was lost while waiting for the announce: %v", got)
	}
}

// A name that cannot be a DNS label is dropped rather than displayed: these are
// rendered as service.device.mesh, and a name that will not resolve is worse
// than no name at all.
func TestServiceNamesAreSanitised(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	now := time.Now()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "nas", nil, 1), now)
	m.handleServices(&control.Services{
		Kind: control.KindServices, DevicePub: peer.DevicePub,
		Names: []string{"Immich", "   ", "home assistant"}, Timestamp: now.Unix(),
	}, now)

	got := m.Services(now)[hex.EncodeToString(peer.DevicePub)]
	for _, n := range got {
		if n != sanitiseName(n) {
			t.Errorf("kept an unsanitised name %q", n)
		}
	}
	if len(got) == 0 {
		t.Error("sanitising dropped everything")
	}
}

// A claim is not permanent. It stops being shown when the peer stops repeating
// it, which is how a service removed from a config disappears from the roster.
func TestServicesGoStale(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	self, _ := identity.New()
	peer, _ := identity.New()
	now := time.Now()

	m := &Mesh{roster: NewRoster(nk, self.DevicePub)}
	m.roster.Apply(newAnnounce(t, peer, "nas", nil, 1), now)
	m.handleServices(&control.Services{
		Kind: control.KindServices, DevicePub: peer.DevicePub,
		Names: []string{"immich"}, Timestamp: now.Unix(),
	}, now)

	if len(m.Services(now.Add(ServicesStale+time.Minute))) != 0 {
		t.Error("a list that stopped being repeated is still being shown")
	}
}

// Off is the default, and the switch is what turns it on: a mesh shared with
// other people should disclose nothing until somebody says so.
func TestNothingIsPublishedUnlessAsked(t *testing.T) {
	m := &Mesh{cfg: state.Config{Services: []string{"immich:2283"}}}
	if err := m.publishServices(time.Now()); err != nil {
		t.Fatalf("publishServices: %v", err)
	}
	// No node, so a publish that got as far as sending would have panicked;
	// reaching here means it stopped at the switch.
}
