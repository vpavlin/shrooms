package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/invite"
)

// Joining a SECOND mesh from the command line.
//
// The daemon has been able to do this since ADR-015 — joinAnother merges into
// the config under the lock, and /join has served it all along. The CLI refused
// before it ever asked: any daemon that was not waiting for its first mesh got
// "stop it and remove its config to join another one", whatever --mesh said.
//
// So this is the regression test for a flag that was accepted, plumbed through
// two layers, and never reached the code that implements it. The phone could do
// it (Mobile.JoinAnotherWithInvite) while the CLI could not, which is how it
// stayed hidden.

// runningDaemon serves a daemon that already has a mesh: /status says it is not
// waiting, /join records what it was asked, /restart accepts.
func runningDaemon(t *testing.T, seen *map[string]any, joinStatus int) string {
	t.Helper()
	// Short path: unix sockets have a ~100 byte limit and t.TempDir() under a
	// long test name exceeds it.
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
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(statusPayload{Name: "vps", Waiting: false})
	})
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var in map[string]any
		json.Unmarshal(body, &in)
		*seen = in
		if joinStatus/100 != 2 {
			http.Error(w, "this device is already in a mesh labelled \"home\"", joinStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"mesh": in["mesh"], "name": in["name"],
			"overlay": "fd00::1", "prefix": "fd00::/64",
			"credential": true, "serial": 7, "expires": "2026-12-01T00:00:00Z",
		})
	})
	mux.HandleFunc("/restart", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func aToken(t *testing.T) string {
	t.Helper()
	s, err := invite.New()
	if err != nil {
		t.Fatal(err)
	}
	return s.String()
}

// The bug: --mesh named a second mesh and the CLI refused instead of asking.
func TestJoinWithALabelReachesARunningDaemon(t *testing.T) {
	var seen map[string]any
	sock := runningDaemon(t, &seen, http.StatusOK)

	if err := cmdJoinInvite(aToken(t), []string{
		"--socket", sock, "--mesh", "home", "--relay", "--name", "vps",
	}); err != nil {
		t.Fatalf("join refused: %v", err)
	}
	if seen == nil {
		t.Fatal("the daemon was never asked")
	}
	if seen["mesh"] != "home" {
		t.Errorf("mesh = %v, want home", seen["mesh"])
	}
	if seen["relay"] != true {
		t.Errorf("relay = %v, want true", seen["relay"])
	}
	if seen["name"] != "vps" {
		t.Errorf("name = %v, want vps", seen["name"])
	}
}

// --port is the one flag that must NOT be forwarded when it was not typed: its
// default is the port the first mesh listens on, and pinning it here would give
// two meshes one socket. Sending 0 is how "work it out" is spelled.
func TestJoinDoesNotPinAPortNobodyAskedFor(t *testing.T) {
	var seen map[string]any
	sock := runningDaemon(t, &seen, http.StatusOK)

	if err := cmdJoinInvite(aToken(t), []string{"--socket", sock, "--mesh", "home"}); err != nil {
		t.Fatalf("join refused: %v", err)
	}
	if got := seen["port"]; got != float64(0) {
		t.Errorf("port = %v, want 0 — an unrequested port was pinned", got)
	}

	// Typed, so it is meant, even when it is the default.
	if err := cmdJoinInvite(aToken(t), []string{
		"--socket", sock, "--mesh", "home", "--port", "51830", "--advertise", "1.2.3.4:51830",
	}); err != nil {
		t.Fatalf("join refused: %v", err)
	}
	if got := seen["port"]; got != float64(51830) {
		t.Errorf("port = %v, want 51830", got)
	}
	if got := seen["advertise"]; got != "1.2.3.4:51830" {
		t.Errorf("advertise = %v, want the endpoint that was given", got)
	}
}

// Without a label there is still nothing to do, and the message has to say what
// would work — the old one said "stop it and remove its config", which is the
// advice that had people destroying a working mesh to add one.
func TestJoinWithoutALabelSaysHowToAddOne(t *testing.T) {
	var seen map[string]any
	sock := runningDaemon(t, &seen, http.StatusOK)

	err := cmdJoinInvite(aToken(t), []string{"--socket", sock})
	if err == nil {
		t.Fatal("a join with no label was accepted")
	}
	if !strings.Contains(err.Error(), "--mesh") {
		t.Errorf("error %q does not mention --mesh", err)
	}
	if seen != nil {
		t.Error("the daemon was asked to join anyway")
	}
}

// "default" is not a second mesh: it already names the one written in the
// single-mesh config form, and two meshes answering to it is a device that
// cannot say which one it means.
func TestDefaultIsNotAnAdditionalMesh(t *testing.T) {
	for _, label := range []string{"", "default"} {
		if additionalMesh(label) {
			t.Errorf("additionalMesh(%q) = true", label)
		}
	}
	for _, label := range []string{"home", "office"} {
		if !additionalMesh(label) {
			t.Errorf("additionalMesh(%q) = false", label)
		}
	}
}

// The daemon's refusal is the caller's error, not a stack trace: it is the one
// that knows the config, so what it says is what should be printed.
func TestJoinReportsWhatTheDaemonRefused(t *testing.T) {
	var seen map[string]any
	sock := runningDaemon(t, &seen, http.StatusBadRequest)

	err := cmdJoinInvite(aToken(t), []string{"--socket", sock, "--mesh", "home"})
	if err == nil || !strings.Contains(err.Error(), "already in a mesh labelled") {
		t.Fatalf("got %v, want the daemon's own message", err)
	}
}
