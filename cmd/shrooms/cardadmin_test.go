package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/mesh"
)

type fakeTarget struct {
	auth    *cred.Authority
	members []mesh.Member
	granted [][]byte
	revoked [][]byte
}

func (f *fakeTarget) Authority() *cred.Authority { return f.auth }
func (f *fakeTarget) Members() []mesh.Member     { return f.members }
func (f *fakeTarget) Grant(b []byte) error       { f.granted = append(f.granted, b); return nil }
func (f *fakeTarget) Revoke(b []byte) error      { f.revoked = append(f.revoked, b); return nil }

// The grant and revoke endpoints' gate, on its own: root as before, the
// socket group only with a card-signed draft on a card-only mesh.
func asGroup(t *testing.T) {
	t.Helper()
	isRootCaller = func(*http.Request) bool { return false }
	t.Cleanup(func() { isRootCaller = callerIsRoot })
}

func TestTheGroupRenewsAndRevokesOnlyWithACard(t *testing.T) {
	card, _ := secp256k1.GeneratePrivateKey()
	auth, _ := cred.NewAuthority(ed25519.PublicKey(card.PubKey().SerializeCompressed()))
	now := time.Now()
	k := bytes.Repeat([]byte{1}, 32)
	f := &fakeTarget{auth: auth, members: []mesh.Member{
		{DevicePub: k, WGPub: k, SealPub: k, Name: "due", NotAfter: now.Add(3 * 24 * time.Hour)},
		{DevicePub: bytes.Repeat([]byte{2}, 32), WGPub: k, Name: "fine", NotAfter: now.Add(25 * 24 * time.Hour)},
	}}
	mux := http.NewServeMux()
	cardAdminHandlers(mux, func(string) adminTarget { return f })
	asGroup(t)

	// Drafts for who is due, and only them.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, identified("POST", "/renew/draft", strings.NewReader(`{}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"due"`) || strings.Contains(w.Body.String(), `"fine"`) {
		t.Fatalf("renew drafts: %d %s", w.Code, w.Body)
	}

	// The readAdminBody gate, as /grant uses it.
	finish := func(d, s []byte) ([]byte, error) { return cred.Finish(auth, d, s, now) }
	c, dg, _ := cred.Draft(auth, k, k, k, "due", 0, now, cred.DefaultLife)
	draft, _ := c.MarshalBinary()
	sig := ecdsa.Sign(card, dg[:]).Serialize()
	body := `{"draft":"` + b64(draft) + `","signature":"` + hex.EncodeToString(sig) + `"}`
	if out, code, err := readAdminBody(httptest.NewRequest("POST", "/grant", strings.NewReader(body)), auth, finish); err != nil {
		t.Fatalf("a card-signed draft from the group was refused: %d %v", code, err)
	} else if cred.IsDraft(out) {
		t.Fatal("came back unsigned")
	}
	// A bare blob from the group: refused, whatever it holds.
	if _, code, _ := readAdminBody(httptest.NewRequest("POST", "/grant", strings.NewReader(b64(draft))), auth, finish); code != http.StatusForbidden {
		t.Fatalf("a bare blob from the group was not refused: %d", code)
	}
	// A file-key mesh: the group may not, even with a valid signature.
	fileAuth, _ := cred.NewAuthority(bytes.Repeat([]byte{3}, 32))
	if _, code, _ := readAdminBody(httptest.NewRequest("POST", "/grant", strings.NewReader(body)), fileAuth, finish); code != http.StatusForbidden {
		t.Fatalf("the group signed for a file-key mesh: %d", code)
	}
	// And members are not listed to the group on a file-key mesh.
	f.auth = fileAuth
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, identified("POST", "/renew/draft", strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("renew drafts on a file-key mesh went to the group: %d", w.Code)
	}
	f.auth = auth

	// Revocation drafts.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, identified("POST", "/revoke/draft", strings.NewReader(`{"device_pub":"`+hex.EncodeToString(k)+`"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"digest"`) {
		t.Fatalf("revoke draft: %d %s", w.Code, w.Body)
	}
}

// identified gives a request the peer credentials the socket would.
func identified(method, path string, body *strings.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	return r.WithContext(context.WithValue(r.Context(), peerCredKey{}, &syscall.Ucred{Uid: 4242}))
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
