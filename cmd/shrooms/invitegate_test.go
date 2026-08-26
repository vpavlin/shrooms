package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	"crypto/ed25519"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/invite"
)

// A gate that only ever says yes is not a gate. Each case below is a way in
// that has to be closed, and the reason it exists is on the case.
//
// The thing being protected is the mesh's network key: /invite/reply publishes
// it, nothing downstream can check it, and on a mesh with no admin keys it IS
// membership.

// cardSigner is an admin key of the shape a Keycard presents: a compressed
// secp256k1 point, and signatures as r‖s.
//
// A real card is not needed to test this gate and would make the test one
// nobody runs. What IS needed is the right key TYPE — an earlier version of
// this file used cred.NewAdmin, which is ed25519, so CardOnly was false and
// every case below was refused at the first condition. Six tests passed and
// none of them tested what they claimed to.
type cardSigner struct {
	priv *secp256k1.PrivateKey
	pub  []byte
}

func newCardSigner(t *testing.T) *cardSigner {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return &cardSigner{priv: priv, pub: priv.PubKey().SerializeCompressed()}
}

func (c *cardSigner) Public() ed25519.PublicKey { return c.pub }

func (c *cardSigner) SignDigest(d [32]byte) ([]byte, error) {
	sig := ecdsa.Sign(c.priv, d[:])
	r, s := sig.R(), sig.S()
	rb, sb := r.Bytes(), s.Bytes()
	out := make([]byte, 0, 64)
	out = append(out, rb[:]...)
	return append(out, sb[:]...), nil
}

// cardAuthority mints an authority whose only key is a card's.
func cardAuthority(t *testing.T) (*cardSigner, *cred.Authority) {
	t.Helper()
	signer := newCardSigner(t)
	auth, err := cred.NewAuthority(signer.pub)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CardOnly() {
		t.Fatal("the fixture is not a card key, so the tests below prove nothing")
	}
	return signer, auth
}

func joiner(t *testing.T) (dev ed25519.PublicKey, wg []byte) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, bytes.Repeat([]byte{7}, 32)
}

// The mesh has no admin keys at all: an invite there hands over the network
// key, which IS membership, and nothing is signed for anybody to check.
func TestGroupMayNotReplyWithoutAnAuthority(t *testing.T) {
	s, _ := invite.New()
	h := &fakeHolder{auth: nil}
	err := groupMayReply(h, s, []byte("anything"))
	if err == nil {
		t.Fatal("a mesh with no admin keys let a non-root caller hand over the network key")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("the refusal should say what would work: %v", err)
	}
}

// A file admin key lives in a user's session, and a socket-group caller may be
// that user — so "the admin signed it" and "the caller could have signed it"
// are the same question. Only a card separates them.
func TestGroupMayNotReplyWhenTheAdminKeyIsAFile(t *testing.T) {
	fileAdmin, err := cred.NewAdmin() // ed25519: a file in somebody's session
	if err != nil {
		t.Fatal(err)
	}
	auth, err := cred.NewAuthority(fileAdmin.Pub)
	if err != nil {
		t.Fatal(err)
	}
	if auth.CardOnly() {
		t.Fatal("an ed25519 admin key was taken for a card")
	}
	s, _ := invite.New()
	h := &fakeHolder{auth: auth}
	if err := groupMayReply(h, s, []byte("anything")); err == nil {
		t.Fatal("a file-key mesh let a non-root caller reply")
	}
}

// gateFixture builds the one arrangement that SHOULD pass, so each test below
// can break exactly one thing about it.
func gateFixture(t *testing.T) (*fakeHolder, invite.Secret, []byte, *cardSigner, *cred.Authority, ed25519.PublicKey, []byte) {
	t.Helper()
	admin, auth := cardAuthority(t)
	dev, wg := joiner(t)
	raw, err := cred.IssueFor(admin, auth, dev, wg, nil, "phone", 0, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := invite.New()
	h := &fakeHolder{auth: auth, admitDev: dev, admitWG: wg, admitKnown: true}
	return h, s, raw, admin, auth, dev, wg
}

// Replying with no credential at all hands over the network key on its own,
// which is the exact thing being gated.
func TestGroupMayNotReplyWithNoCredential(t *testing.T) {
	h, s, _, _, _, _, _ := gateFixture(t)
	if err := groupMayReply(h, s, nil); err == nil {
		t.Fatal("a reply carrying no credential was allowed")
	}
}

// The attack this whole gate exists for: anybody can mint a token and walk
// their own device through the exchange, then offer a credential issued to
// somebody else. It names another device, and must be refused.
func TestGroupMayNotReplyWithACredentialForAnotherDevice(t *testing.T) {
	h, s, _, admin, auth, _, _ := gateFixture(t)
	other, otherWG := joiner(t)
	elsewhere, err := cred.IssueFor(admin, auth, other, otherWG, nil, "laptop", 0, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = groupMayReply(h, s, elsewhere)
	if err == nil {
		t.Fatal("a credential for a different device was accepted")
	}
	if !strings.Contains(err.Error(), "different device") {
		t.Errorf("the refusal should name the reason: %v", err)
	}
}

// A credential naming the right device under somebody else's tunnel key is its
// own kind of wrong, and the device key alone would not catch it.
func TestGroupMayNotReplyWithACredentialForAnotherTunnelKey(t *testing.T) {
	h, s, _, admin, auth, dev, _ := gateFixture(t)
	swapped, err := cred.IssueFor(admin, auth, dev, bytes.Repeat([]byte{9}, 32),
		nil, "phone", 0, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := groupMayReply(h, s, swapped); err == nil {
		t.Fatal("a credential naming another tunnel key was accepted")
	}
}

// Signed by a key that is not this mesh's authority.
func TestGroupMayNotReplyWithACredentialFromAnotherMesh(t *testing.T) {
	h, s, _, _, _, dev, wg := gateFixture(t)
	stranger, strangerAuth := cardAuthority(t)
	foreign, err := cred.IssueFor(stranger, strangerAuth, dev, wg, nil, "phone", 0, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := groupMayReply(h, s, foreign); err == nil {
		t.Fatal("a credential signed by another mesh's admin was accepted")
	}
}

// This daemon is not the one holding the exchange, so it cannot know which
// device the credential should name — and must not guess.
func TestGroupMayNotReplyForAnExchangeThisDaemonIsNotHolding(t *testing.T) {
	h, s, raw, _, _, _, _ := gateFixture(t)
	h.admitKnown = false
	if err := groupMayReply(h, s, raw); err == nil {
		t.Fatal("replied for an exchange this daemon never held")
	}
}

// Garbage in the credential field must be refused as garbage, not treated as
// absent and not panicked over.
func TestGroupMayNotReplyWithAnUnreadableCredential(t *testing.T) {
	h, s, _, _, _, _, _ := gateFixture(t)
	if err := groupMayReply(h, s, []byte("not a credential")); err == nil {
		t.Fatal("unreadable bytes were accepted as a credential")
	}
}

// The case that has to work, and the one every test above would still pass
// without: a card-signed credential, for the device this exchange is admitting,
// on a card-only mesh, from the daemon holding it.
//
// A gate that refuses everything satisfies every refusal test in this file.
func TestGroupMayReplyWithTheRightCredential(t *testing.T) {
	h, s, raw, _, _, _, _ := gateFixture(t)
	if err := groupMayReply(h, s, raw); err != nil {
		t.Fatalf("the arrangement this whole design is for was refused: %v", err)
	}
}

// And the boundary that stops it being a permanent grant: once the credential
// has expired it proves the admin approved something once, not now.
func TestGroupMayNotReplyWithAnExpiredCredential(t *testing.T) {
	admin, auth := cardAuthority(t)
	dev, wg := joiner(t)
	// Issued far enough in the past that it is over, using the same call the
	// real path uses rather than hand-building one.
	raw, err := cred.IssueFor(admin, auth, dev, wg, nil, "phone", 0,
		time.Now().Add(-48*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := invite.New()
	h := &fakeHolder{auth: auth, admitDev: dev, admitWG: wg, admitKnown: true}
	if err := groupMayReply(h, s, raw); err == nil {
		t.Fatal("an expired credential was accepted")
	}
}

// A caller the kernel will not vouch for is not a group member; it is a
// question this daemon cannot answer, and both invite endpoints must say so
// rather than fall through to parsing the body.
//
// This regressed the moment they stopped being wrapped in requireRoot: a
// malformed request answered 400, an error about syntax where the truthful one
// is about identity.
func TestInviteEndpointsRefuseAnUnidentifiedCaller(t *testing.T) {
	mux := http.NewServeMux()
	inviteHandlers(mux, "", only(&fakeHolder{}))

	for _, path := range []string{"/invite/hold", "/invite/reply"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"token":"x"}`))
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s answered %d to an unidentified caller, want 403", path, rec.Code)
		}
	}
}
