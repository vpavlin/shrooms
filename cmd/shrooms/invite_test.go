package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/invite"
)

// fakeHolder stands in for the mesh: everything the enrolment endpoints do
// except own a rendezvous node.
type fakeHolder struct {
	req     *invite.Request
	held    invite.Secret
	holdErr error

	gotSecret     invite.Secret
	gotEph        []byte
	gotName       string
	gotCredential []byte
	replyErr      error
}

func (f *fakeHolder) HoldInvite(ctx context.Context, s invite.Secret) (*invite.Request, error) {
	f.held = s
	if f.holdErr != nil {
		return nil, f.holdErr
	}
	if f.req == nil {
		<-ctx.Done() // nobody redeems it
		return nil, ctx.Err()
	}
	return f.req, nil
}

func (f *fakeHolder) ReplyInvite(s invite.Secret, req *invite.Request, credential []byte) error {
	f.gotSecret, f.gotEph, f.gotName, f.gotCredential = s, req.EphPub, req.Name, credential
	return f.replyErr
}

// only is a node with one mesh, which answers to any label — including the
// empty one an older CLI sends.
func only(h inviteHolder) func(string) inviteHolder {
	return func(string) inviteHolder { return h }
}

// serveHolder runs the real handlers on a real unix socket, so the test covers
// the wire contract between `shrooms invite` and the daemon rather than a
// hand-written imitation of it.
func serveHolder(t *testing.T, h inviteHolder) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	inviteHandlers(mux, only(h))
	// ConnContext as the daemon sets it, so the tests exercise the peer
	// credential check rather than routing around it.
	srv := &http.Server{Handler: mux, ConnContext: withPeerCred}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// The exchange the daemon and the CLI split between them: the daemon holds the
// topic and publishes, the CLI signs. Neither can admit a device alone, which
// is the reason for splitting it in the first place.
func TestInviteOverTheControlSocket(t *testing.T) {
	secret, err := invite.New()
	if err != nil {
		t.Fatal(err)
	}
	devPub := bytes.Repeat([]byte{1}, 32)
	wgPub := bytes.Repeat([]byte{2}, 32)
	ephPub := bytes.Repeat([]byte{3}, 32)

	h := &fakeHolder{req: &invite.Request{
		DevicePub: devPub, WGPub: wgPub, EphPub: ephPub,
		Name: "phone", Timestamp: time.Now().Unix(),
	}}
	client := socketClient(serveHolder(t, h), 10*time.Second)

	req, err := holdInvite(client, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if req == nil {
		t.Fatal("no request returned")
	}
	if h.held != secret {
		t.Error("the daemon was asked to hold a different token")
	}
	if req.Name != "phone" || req.DevicePub != hex.EncodeToString(devPub) || req.WGPub != hex.EncodeToString(wgPub) {
		t.Errorf("request came back wrong: %+v", req)
	}

	credential := bytes.Repeat([]byte{7}, 300)
	if err := replyInvite(client, secret, req.EphPub, "phone", credential); err != nil {
		t.Fatal(err)
	}
	if h.gotSecret != secret {
		t.Error("the reply named a different token")
	}
	if !bytes.Equal(h.gotEph, ephPub) {
		t.Error("the reply lost the joiner's ephemeral key; the response would be unreadable")
	}
	if !bytes.Equal(h.gotCredential, credential) {
		t.Error("the credential did not survive the round trip")
	}
	if h.gotName != "phone" {
		t.Errorf("name = %q", h.gotName)
	}
}

// An invite nobody uses is not a failure — it expires, and the CLI has to be
// able to tell that apart from the daemon refusing.
func TestInviteExpiryIsNotAnError(t *testing.T) {
	secret, _ := invite.New()
	client := socketClient(serveHolder(t, &fakeHolder{}), 10*time.Second)

	// ttl_s below a second floors to zero, which the daemon reads as "use the
	// default"; a second is the shortest that actually expires quickly.
	req, err := holdInvite(client, secret, time.Second)
	if err != nil {
		t.Fatalf("expiry reported as an error: %v", err)
	}
	if req != nil {
		t.Error("a request appeared out of nowhere")
	}
}

// The socket is group-readable so `shrooms status` needs no root. Changing the
// mesh is a different thing, and must not come with it.
func TestMutatingEndpointsRefuseStrangers(t *testing.T) {
	mux := http.NewServeMux()
	inviteHandlers(mux, only(&fakeHolder{}))

	// A handler reached with no credentials on the context — what a caller the
	// kernel would not vouch for looks like — must be refused.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/invite/hold", strings.NewReader(`{"token":"x"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("an unidentified caller got %d, want 403", rec.Code)
	}
}

func TestInviteRefusalIsReported(t *testing.T) {
	secret, _ := invite.New()
	client := socketClient(serveHolder(t, &fakeHolder{holdErr: errors.New("that invite is already open")}), 10*time.Second)

	if _, err := holdInvite(client, secret, time.Minute); err == nil {
		t.Error("a refused invite looked like success")
	}
}

// A mesh with no authority still issues invites — they move the network key
// without putting it on a screen — and the reply simply carries no credential.
// A label naming a mesh this node has not joined must be refused, not silently
// served by whichever mesh happened to be first — that would enrol a device
// into the wrong network.
func TestInviteForAnUnknownMeshIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	inviteHandlers(mux, func(label string) inviteHolder {
		if label == "home" {
			return &fakeHolder{}
		}
		return nil
	})
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ConnContext: withPeerCred}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	secret, _ := invite.New()
	if _, err := holdInviteOn(socketClient(sock, 5*time.Second), secret, time.Second, "elsewhere"); err == nil {
		t.Error("held an invite for a mesh this node has not joined")
	}
}

func TestInviteWithoutACredential(t *testing.T) {
	secret, _ := invite.New()
	h := &fakeHolder{}
	client := socketClient(serveHolder(t, h), 10*time.Second)

	if err := replyInvite(client, secret, hex.EncodeToString(bytes.Repeat([]byte{3}, 32)), "vps", nil); err != nil {
		t.Fatal(err)
	}
	if len(h.gotCredential) != 0 {
		t.Errorf("sent %d bytes of credential for a mesh with no admin keys", len(h.gotCredential))
	}
}
