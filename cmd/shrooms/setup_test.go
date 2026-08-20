package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/state"
)

// Creating a mesh is one command. It used to be three — init, admin init, admin
// issue — and every one of them was a chance to end up with a config that
// names an authority nobody holds, or an authority no config trusts.
func TestInitMintsAndEnrols(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shrooms.toml")
	stateDir := filepath.Join(dir, "state")
	adminDir := filepath.Join(dir, "admin")

	quiet(t)
	withStdin(t, "hunter2 hunter2\nhunter2 hunter2\n")
	if err := cmdInit([]string{
		"--name", "laptop",
		"--config", cfgPath,
		"--state", stateDir,
		"--admin-dir", adminDir,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := cfg.Authority()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("init wrote no admin_keys; the mesh trusts nobody")
	}
	if len(auth.Keys) != 2 {
		t.Fatalf("authority has %d keys, want the signing key and a recovery key", len(auth.Keys))
	}

	// The point of folding these together: the device that ran init is already
	// a member, and can prove it to the mesh it just created.
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Credential) == 0 {
		t.Fatal("init issued no credential; this device could not join its own mesh")
	}
	c, err := cred.UnmarshalCredential(st.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
		t.Fatalf("own credential does not verify: %v", err)
	}
	if c.MeshID != auth.ID() {
		t.Errorf("credential names mesh %s, config trusts %s", c.MeshID, auth.ID())
	}
	if c.Name != "laptop" {
		t.Errorf("credential names %q, want laptop", c.Name)
	}

	// The admin key stays encrypted at rest: init read a passphrase.
	raw, err := os.ReadFile(filepath.Join(adminDir, "admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var af adminFile
	if err := json.Unmarshal(raw, &af); err != nil {
		t.Fatal(err)
	}
	if !af.Encrypted {
		t.Error("admin key was written in the clear")
	}
}

// --no-admin is for joining a mesh that already exists — minting an authority
// there would create a second mesh nobody asked for.
func TestInitNoAdmin(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shrooms.toml")
	adminDir := filepath.Join(dir, "admin")

	quiet(t)
	if err := cmdInit([]string{
		"--name", "laptop",
		"--config", cfgPath,
		"--state", filepath.Join(dir, "state"),
		"--admin-dir", adminDir,
		"--no-admin",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(adminDir, "admin.json")); !os.IsNotExist(err) {
		t.Error("--no-admin minted an authority anyway")
	}
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AdminKeys) != 0 {
		t.Error("--no-admin wrote admin_keys")
	}
}

// quiet swallows what init prints. A recovery key scrolling past in CI output
// is noise at best and a bad habit at worst.
func quiet(t *testing.T) {
	t.Helper()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = null
	t.Cleanup(func() {
		os.Stdout = old
		null.Close()
	})
}

// withStdin points the shared reader at a script for the duration of a test.
func withStdin(t *testing.T, script string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(script); err != nil {
		t.Fatal(err)
	}
	w.Close()

	oldFile, oldReader := os.Stdin, stdinReader
	os.Stdin, stdinReader = r, nil
	t.Cleanup(func() {
		r.Close()
		os.Stdin, stdinReader = oldFile, oldReader
	})
}

// The three outcomes of nudging a daemon need three different instructions, and
// the wrong one is worse than none.
//
// This is the bug the first person outside the project hit: init wrote the
// config, the nudge did not land, and the advice printed was
// "sudo systemctl enable --now shrooms" — which on an already-running service
// does nothing at all. Following it exactly left a config, a daemon still
// waiting, and no sign of which was wrong. The invite then failed with
// "404 page not found".
//
// Driven over a real unix socket, because that is what nudgeDaemon dials and a
// TCP stand-in would exercise a path the daemon never uses.
func TestNudgeOutcomesAreDistinguished(t *testing.T) {
	t.Run("waiting daemon accepts the reload", func(t *testing.T) {
		sock := serveWaiting(t, true)
		if !nudgeDaemon(sock) {
			t.Error("a daemon that accepted /reload was not treated as nudged")
		}
	})

	t.Run("waiting daemon refuses the reload", func(t *testing.T) {
		sock := serveWaiting(t, false)
		if nudgeDaemon(sock) {
			t.Error("a daemon that refused /reload was reported as nudged")
		}
		// And it is still reachable and still waiting, which is what tells
		// reportNext to say "restart" rather than "enable --now".
		st, err := fetchStatus(sock)
		if err != nil || !st.Waiting {
			t.Errorf("status after a refused reload: %+v, err %v", st, err)
		}
	})

	t.Run("no daemon", func(t *testing.T) {
		if nudgeDaemon(filepath.Join(t.TempDir(), "absent.sock")) {
			t.Error("an absent daemon was reported as nudged")
		}
	})
}

// serveWaiting runs a daemon with no mesh on a unix socket and returns its path.
func serveWaiting(t *testing.T, acceptReload bool) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(statusPayload{Waiting: true})
	})
	mux.HandleFunc("/reload", func(w http.ResponseWriter, _ *http.Request) {
		if !acceptReload {
			http.Error(w, "not now", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}
