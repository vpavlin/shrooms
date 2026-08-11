package mesh

import (
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// issue signs a credential the way the admin tooling does.
func issue(t *testing.T, admin *cred.Admin, auth *cred.Authority,
	id *identity.Identity, serial uint64, now time.Time, life time.Duration) []byte {
	t.Helper()
	c, err := admin.Issue(id.DevicePub, id.WGPub[:], "peer", serial, now, life)
	if err != nil {
		t.Fatal(err)
	}
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signWith(admin, d)
	raw, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// meshFor is the smallest Mesh that can take a renewal: an authority to check
// it against and somewhere to put it. No node, so nothing is relayed — which
// is exactly the part these tests are not about.
func meshFor(t *testing.T, auth *cred.Authority) (*Mesh, *identity.Identity) {
	t.Helper()
	st, err := state.LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Mesh{
		log:       slog.New(slog.DiscardHandler),
		authority: auth,
		st:        st,
		grants:    map[[32]byte]time.Time{},
	}, st.Identity
}

func TestARenewalForThisDeviceIsKept(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	m, id := meshFor(t, auth)
	m.st.Credential = issue(t, admin, auth, id, 1, now, 24*time.Hour)

	renewed := issue(t, admin, auth, id, 2, now, 30*24*time.Hour)
	if err := m.handleGrant(renewed, now); err != nil {
		t.Fatalf("refused a renewal from our own admin: %v", err)
	}
	if got := m.SelfExpiry(); got.Sub(now) < 29*24*time.Hour {
		t.Errorf("credential was not replaced: expires %s, wanted a month out", got)
	}
}

// The one that matters: a credential is public and passes through other
// members' hands on its way here, so an admin signature is the only thing
// between a renewal and a forgery.
func TestARenewalFromAnotherAuthorityIsRefused(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	other, _ := cred.NewAdmin()
	otherAuth, _ := cred.NewAuthority(other.Pub)
	now := time.Now()

	m, id := meshFor(t, auth)
	m.st.Credential = issue(t, admin, auth, id, 1, now, 24*time.Hour)
	before := m.SelfExpiry()

	forged := issue(t, other, otherAuth, id, 2, now, 30*24*time.Hour)
	if err := m.handleGrant(forged, now); err == nil {
		t.Fatal("accepted a credential this mesh did not sign")
	}
	if !m.SelfExpiry().Equal(before) {
		t.Error("a refused credential was stored anyway")
	}
}

// Going backwards would shorten the life of the thing keeping this device on
// the mesh, and a sweep can reissue while an older credential is still in
// flight.
func TestAnOlderRenewalDoesNotReplaceANewerOne(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	m, id := meshFor(t, auth)
	m.st.Credential = issue(t, admin, auth, id, 2, now, 30*24*time.Hour)
	want := m.SelfExpiry()

	if err := m.handleGrant(issue(t, admin, auth, id, 3, now, time.Hour), now); err != nil {
		t.Fatalf("handleGrant: %v", err)
	}
	if !m.SelfExpiry().Equal(want) {
		t.Errorf("a shorter credential replaced a longer one: %s", m.SelfExpiry())
	}
}

// Somebody else's renewal is not ours to keep. It is relayed instead, which
// this Mesh has no node to do — the point here is that it is not stored.
func TestARenewalForAnotherDeviceIsNotStored(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	them, _ := identity.New()
	now := time.Now()

	m, id := meshFor(t, auth)
	m.st.Credential = issue(t, admin, auth, id, 1, now, 24*time.Hour)
	want := m.SelfExpiry()

	if err := m.handleGrant(issue(t, admin, auth, them, 9, now, 30*24*time.Hour), now); err != nil {
		t.Fatalf("handleGrant: %v", err)
	}
	if !m.SelfExpiry().Equal(want) {
		t.Error("stored a credential naming another device")
	}
}
