package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A daemon reads its mesh set at startup, so removing an entry from the config
// leaves the interface up and the mesh running until something restarts it.
//
// This is the same disagreement `init` used to produce in the other direction —
// config and daemon holding different ideas of which meshes exist — and it was
// reported as "I removed it and it is still there". Ending with an instruction
// to restart by hand is what made that possible.

// fakeDaemon serves /status and /restart on a unix socket, and reports whether
// a restart was ever asked for.
func fakeDaemon(t *testing.T, restartOK bool) (sock string, restarted *atomic.Bool) {
	t.Helper()
	restarted = &atomic.Bool{}

	// Short path: a unix socket has a ~100 byte limit and t.TempDir() under a
	// long test name can exceed it.
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Not waiting: this daemon is already carrying meshes, which is the
		// case where a reload cannot help and only a restart will.
		w.Write([]byte(`{"waiting":false,"meshes":[]}`))
	})
	mux.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		restarted.Store(true)
		if !restartOK {
			// What the daemon answers when nothing would start it again.
			http.Error(w, "nothing would restart me", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock, restarted
}

func TestRemovingAMeshRestartsARunningDaemon(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)
	sock, restarted := fakeDaemon(t, true)

	out := captureStdout(t, func() {
		if err := cmdMeshRemove([]string{
			"--config", cfgPath, "--socket", sock, "--yes", "office",
		}); err != nil {
			t.Fatal(err)
		}
	})

	if !restarted.Load() {
		t.Fatal("the daemon was never asked to restart, so the interface stays up")
	}
	if !strings.Contains(out, "restarting") {
		t.Errorf("nothing said the daemon was restarting; got:\n%s", out)
	}
	// The instruction to do it by hand is wrong once it has been done.
	if strings.Contains(out, "Restart the daemon to drop") {
		t.Errorf("told the user to restart a daemon that restarted itself:\n%s", out)
	}
}

// A daemon that refuses — nothing would bring it back — must not be reported as
// having dropped the interface, because it has not.
func TestRemovingAMeshSaysSoWhenTheDaemonRefuses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)
	sock, restarted := fakeDaemon(t, false)

	out := captureStdout(t, func() {
		if err := cmdMeshRemove([]string{
			"--config", cfgPath, "--socket", sock, "--yes", "office",
		}); err != nil {
			t.Fatal(err)
		}
	})

	if !restarted.Load() {
		t.Fatal("never asked")
	}
	if !strings.Contains(out, "still up") {
		t.Errorf("a refused restart was not reported as leaving it up:\n%s", out)
	}
	if strings.Contains(out, "is restarting") {
		t.Errorf("claimed a restart that was refused:\n%s", out)
	}
}

// No daemon at all is the normal case for a config on a machine that is not
// running one, and it must still remove the mesh.
func TestRemovingAMeshWithNoDaemonStillRemoves(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)

	out := captureStdout(t, func() {
		if err := cmdMeshRemove([]string{
			"--config", cfgPath, "--socket", filepath.Join(dir, "absent.sock"),
			"--yes", "office",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Removed") {
		t.Errorf("did not report the removal:\n%s", out)
	}
}
