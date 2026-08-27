package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/cred"
)

// One card minting on two machines produces two meshes with ONE mesh id.
//
// The account is chosen from what the local machine has already used, and the
// card records nothing — so two machines that have never minted both choose
// zero, derive the same admin key, and get the same id. The meshes have
// different network keys, so they cannot reach each other; and the same id, so
// a credential or a revocation issued for either verifies against both.
//
// A file authority cannot do this: its key is random.

func authorityWithKey(t *testing.T, b byte) (*cred.Authority, string) {
	t.Helper()
	// 33 bytes: a compressed secp256k1 point, which is what a card key is.
	raw := make([]byte, 33)
	raw[0], raw[1] = 0x02, b
	auth, err := cred.NewAuthority(raw)
	if err != nil {
		t.Fatal(err)
	}
	return auth, b32.EncodeToString(raw)
}

func statusWithMesh(t *testing.T, label, authorityID string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"meshes": []map[string]any{{"label": label, "authority_id": authorityID}},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// The realistic case: this node is already on a mesh minted from this card
// somewhere else, so the daemon knows the id that is about to be duplicated.
func TestMintRefusesAnIdThisNodeAlreadyKnows(t *testing.T) {
	auth, _ := authorityWithKey(t, 0x11)
	sock := statusWithMesh(t, "home", auth.ID().String())

	err := refuseDuplicateAuthority(sock, auth, 0)
	if err == nil {
		t.Fatal("minted a second mesh with an id this node is already on")
	}
	for _, want := range []string{"same id", "home", "network keys", "account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A different account is a different key, so it must not be refused.
func TestMintAllowsADifferentAccount(t *testing.T) {
	known, _ := authorityWithKey(t, 0x11)
	fresh, _ := authorityWithKey(t, 0x22)
	sock := statusWithMesh(t, "home", known.ID().String())

	if err := refuseDuplicateAuthority(sock, fresh, 1); err != nil {
		t.Fatalf("refused a genuinely different authority: %v", err)
	}
}

// With no daemon there is nothing to check against, and minting on a machine
// running nothing is normal. It must not become an error.
func TestMintProceedsWithNoDaemon(t *testing.T) {
	auth, _ := authorityWithKey(t, 0x33)
	if err := refuseDuplicateAuthority(filepath.Join(t.TempDir(), "absent"), auth, 0); err != nil {
		t.Fatalf("refused to mint because no daemon was running: %v", err)
	}
}

// And a mesh with no authority at all reports no id, which must not match.
func TestMintIgnoresMeshesWithNoAuthority(t *testing.T) {
	auth, _ := authorityWithKey(t, 0x44)
	sock := statusWithMesh(t, "keyless", "")
	if err := refuseDuplicateAuthority(sock, auth, 0); err != nil {
		t.Fatalf("an authority-less mesh matched: %v", err)
	}
}
