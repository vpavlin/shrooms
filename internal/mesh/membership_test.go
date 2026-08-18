package mesh

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

func announceFor(t *testing.T, id *identity.Identity, c []byte) *control.Announce {
	t.Helper()
	return &control.Announce{
		Kind: control.KindAnnounce, DevicePub: id.DevicePub, WGPub: id.WGPub[:],
		Name: "peer", Seq: 1, Timestamp: time.Now().Unix(), Credential: c,
	}
}

// Both schemes run at once, which is the whole migration story: a mesh with no
// admin keys behaves exactly as it does today.
func TestWithoutAnAuthorityEveryPeerIsAdmitted(t *testing.T) {
	id, _ := identity.New()
	m := &Mesh{}
	if err := m.checkMembership(announceFor(t, id, nil), time.Now()); err != nil {
		t.Errorf("a mesh without admin keys refused a peer: %v", err)
	}
}

func TestWithAnAuthorityACredentialIsRequired(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	id, _ := identity.New()
	now := time.Now()
	m := &Mesh{authority: auth}

	// No credential at all.
	if err := m.checkMembership(announceFor(t, id, nil), now); err == nil {
		t.Error("admitted a peer with no credential")
	}

	// A genuine one, for this device.
	c, err := admin.Issue(id.DevicePub, id.WGPub[:], "peer", 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signWith(admin, d)
	raw, _ := c.MarshalBinary()
	if err := m.checkMembership(announceFor(t, id, raw), now); err != nil {
		t.Errorf("refused a validly credentialled peer: %v", err)
	}

	// Expired.
	if err := m.checkMembership(announceFor(t, id, raw), now.Add(2*time.Hour)); err == nil {
		t.Error("admitted a peer whose credential had expired")
	}
}

// A credential holds nothing secret and every mesh member can read one: it
// rides inside an announce encrypted to the mesh, not to a recipient. Binding
// it to the key that signed the announce is what stops one member replaying
// another member's membership.
func TestACopiedCredentialDoesNotAdmitAnotherDevice(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	member, _ := identity.New()
	thief, _ := identity.New()
	now := time.Now()
	m := &Mesh{authority: auth}

	c, _ := admin.Issue(member.DevicePub, member.WGPub[:], "member", 1, now, time.Hour)
	c.MeshID = auth.ID()
	d, _ := c.Digest()
	c.Sig = signWith(admin, d)
	raw, _ := c.MarshalBinary()

	// The thief announces with its own keys and the member's credential.
	if err := m.checkMembership(announceFor(t, thief, raw), now); err == nil {
		t.Error("a copied credential admitted a different device")
	}

	// And swapping only the tunnel key is refused too, or a member could point
	// its membership at someone else's tunnel.
	swapped := announceFor(t, member, raw)
	swapped.WGPub = thief.WGPub[:]
	if err := m.checkMembership(swapped, now); err == nil {
		t.Error("a credential admitted an announce naming a different tunnel key")
	}
}

// A credential from an authority this mesh does not trust is not membership,
// however well formed it is.
func TestAnotherMeshsCredentialIsRefused(t *testing.T) {
	ours, _ := cred.NewAdmin()
	theirs, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(ours.Pub)
	id, _ := identity.New()
	now := time.Now()

	c, _ := theirs.Issue(id.DevicePub, id.WGPub[:], "peer", 1, now, time.Hour)
	raw, _ := c.MarshalBinary()

	m := &Mesh{authority: auth}
	if err := m.checkMembership(announceFor(t, id, raw), now); err == nil {
		t.Error("admitted a peer credentialled by another mesh")
	}
}

func signWith(a *cred.Admin, d [32]byte) []byte { return ed25519.Sign(a.Priv, d[:]) }

// Revoke is reachable over the control socket, so what it refuses matters as
// much as what it accepts: the socket says who may ask, and the signature
// inside says whether it is honoured.
//
// Only the refusals are exercised here. Accepting one publishes to the bus,
// which needs a live rendezvous node.
func TestRevokeRefusesWhatThisMeshDidNotSign(t *testing.T) {
	ours, _ := cred.NewAdmin()
	theirs, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(ours.Pub)
	id, _ := identity.New()

	m := &Mesh{authority: auth}

	if err := m.Revoke([]byte("not a revocation")); err == nil {
		t.Error("accepted rubbish as a revocation")
	}

	r, err := theirs.Revoke(id.DevicePub, 1, time.Time{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.MeshID = auth.ID() // claims to be ours; the signature is not
	d, _ := r.Digest()
	r.Sig = signWith(theirs, d)
	raw, _ := r.MarshalBinary()
	if err := m.Revoke(raw); err == nil {
		t.Error("accepted a revocation signed by another mesh's admin")
	}

	// And a mesh with no authority has nothing to revoke against, so it must
	// say so rather than quietly dropping a device on anyone's say-so.
	if err := (&Mesh{}).Revoke(raw); err == nil {
		t.Error("a mesh with no admin keys accepted a revocation")
	}
}
