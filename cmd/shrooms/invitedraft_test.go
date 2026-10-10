package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/invite"
)

func serveDrafts(t *testing.T, h inviteHolder) *http.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	inviteHandlers(mux, "", only(h))
	inviteDraftHandlers(mux, "", only(h), func() string { return "198.51.100.7:60000" })
	srv := &http.Server{Handler: mux, ConnContext: withPeerCred}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socketClient(sock, 10*time.Second)
}

func postJSON(t *testing.T, c *http.Client, path string, in any, out any) int {
	t.Helper()
	b, _ := json.Marshal(in)
	resp, err := c.Post("http://unix"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// What Basecamp does with a card (ADR-050): mint, hold, draft, have the card
// sign the digest somewhere else, and reply with the draft and the signature.
// The daemon finishes the credential and publishes it, verified.
func TestInviteWithASignatureMadeElsewhere(t *testing.T) {
	card, _ := secp256k1.GeneratePrivateKey()
	auth, err := cred.NewAuthority(ed25519.PublicKey(card.PubKey().SerializeCompressed()))
	if err != nil {
		t.Fatal(err)
	}
	dev, wg, seal, eph := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32),
		bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{3}, 32)
	h := &fakeHolder{auth: auth, req: &invite.Request{DevicePub: dev, WGPub: wg, SealPub: seal, EphPub: eph,
		Name: "kitchen-pi", Timestamp: time.Now().Unix()}}
	c := serveDrafts(t, h)

	var minted struct {
		Token, Grouped, URI string
		QR                  []string
	}
	if code := postJSON(t, c, "/invite/new", map[string]string{}, &minted); code != 200 {
		t.Fatalf("/invite/new: %d", code)
	}
	secret, err := invite.Parse(minted.Token)
	if err != nil {
		t.Fatalf("minted token does not parse: %v", err)
	}
	if len(minted.QR) < 21 || len(minted.QR[0]) != len(minted.QR) {
		t.Errorf("no square QR came with the invite: %d rows", len(minted.QR))
	}
	if !strings.Contains(minted.URI, "198.51.100.7") {
		t.Errorf("the URI does not carry the boot address: %s", minted.URI)
	}

	req, err := holdInvite(c, secret, time.Minute)
	if err != nil || req == nil {
		t.Fatalf("hold: %v %v", req, err)
	}

	var draft struct {
		Draft, Digest string
		AdminKeys     []string `json:"admin_keys"`
		CardOnly      bool     `json:"card_only"`
	}
	if code := postJSON(t, c, "/invite/draft", map[string]string{
		"device_pub": req.DevicePub, "wg_pub": req.WGPub, "seal_pub": req.SealPub, "name": req.Name,
	}, &draft); code != 200 {
		t.Fatalf("/invite/draft: %d", code)
	}
	if !draft.CardOnly || len(draft.AdminKeys) != 1 || draft.AdminKeys[0] != b32.EncodeToString(auth.Keys[0]) {
		t.Fatalf("draft did not name the card's key: %+v", draft)
	}

	// An unsigned draft is refused outright, whoever sends it.
	if code := postJSON(t, c, "/invite/reply", map[string]string{"token": minted.Token, "eph_pub": req.EphPub,
		"name": req.Name, "credential": draft.Draft}, nil); code != http.StatusBadRequest {
		t.Fatalf("an unsigned draft was published: %d", code)
	}

	digest, _ := hex.DecodeString(draft.Digest)
	der := ecdsa.Sign(card, digest).Serialize() // the form keycard-qt hands over
	if code := postJSON(t, c, "/invite/reply", map[string]string{"token": minted.Token, "eph_pub": req.EphPub,
		"name": req.Name, "credential": draft.Draft, "signature": hex.EncodeToString(der)}, nil); code != http.StatusNoContent {
		t.Fatalf("/invite/reply with a signature: %d", code)
	}
	got, err := cred.UnmarshalCredential(h.gotCredential)
	if err != nil {
		t.Fatal(err)
	}
	if err := cred.VerifyBy(auth, got, time.Now()); err != nil {
		t.Fatalf("the published credential does not verify: %v", err)
	}
	if !bytes.Equal(got.DevicePub, dev) || got.Name != "kitchen-pi" {
		t.Fatalf("published for the wrong device: %+v", got)
	}

	// A signature by some other key is refused before anything is published.
	h.gotCredential = nil
	other, _ := secp256k1.GeneratePrivateKey()
	bad := ecdsa.Sign(other, digest).Serialize()
	if code := postJSON(t, c, "/invite/reply", map[string]string{"token": minted.Token, "eph_pub": req.EphPub,
		"name": req.Name, "credential": draft.Draft, "signature": hex.EncodeToString(bad)}, nil); code != http.StatusBadRequest {
		t.Fatalf("a foreign signature was taken: %d", code)
	}
	if h.gotCredential != nil {
		t.Fatal("a refused reply still published")
	}
}

// A mesh with no admin keys has nothing to sign; the draft says so rather
// than failing, and the app replies with no credential.
func TestDraftForAMeshWithNoAuthority(t *testing.T) {
	c := serveDrafts(t, &fakeHolder{})
	var out struct {
		NoAuthority bool `json:"no_authority"`
	}
	if code := postJSON(t, c, "/invite/draft", map[string]string{"device_pub": "00", "wg_pub": "00"}, &out); code != 200 || !out.NoAuthority {
		t.Fatalf("got %d %+v", code, out)
	}
}
