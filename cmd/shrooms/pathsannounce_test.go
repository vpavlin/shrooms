package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `shrooms paths` printed one mesh's announced addresses above peers from every
// mesh, because the daemon reported it as a node-wide fact.
//
// It cost an hour on 2026-08-26: a phone would not connect on the `kc` mesh,
// `we announce 192.168.0.151:51821` was read as kc's endpoint, and kc was
// listening on 51824. The output was not wrong about anything it said — it was
// answering a different question than the one being asked of it.

// statusSocket serves one fixed status payload.
func statusSocket(t *testing.T, body string) string {
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
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestPathsAnnouncesPerMesh(t *testing.T) {
	sock := statusSocket(t, `{
	  "meshes": [
	    {"label":"default","announced":["192.168.0.151:51821"],"peers":1},
	    {"label":"kc","announced":["192.168.0.151:51824"],"peers":1}
	  ]
	}`)

	out := captureStdout(t, func() {
		if err := cmdPaths([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})

	// Both ports, each under its own label. The bug was that only one appeared
	// and nothing said which mesh it belonged to.
	for _, want := range []string{"default", "51821", "kc", "51824"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "51821") > strings.Index(out, "kc") {
		t.Errorf("default's address printed under kc's label:\n%s", out)
	}
}

// A mesh with nothing to announce is the case worth reading, so it must say so
// per mesh rather than being an absent line.
func TestPathsSaysWhichMeshCannotBeDialled(t *testing.T) {
	sock := statusSocket(t, `{
	  "meshes": [
	    {"label":"default","announced":["192.168.0.151:51821"],"peers":1},
	    {"label":"kc","peers":1}
	  ]
	}`)

	out := captureStdout(t, func() {
		if err := cmdPaths([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "no peer can dial this node on this mesh") {
		t.Errorf("a mesh announcing nothing was not called out:\n%s", out)
	}
}

// An older daemon reports one node-wide list. Printing it unqualified above
// several meshes is the original bug, so it has to be marked as partial.
func TestPathsMarksNodeWideAnnounceAsPartial(t *testing.T) {
	sock := statusSocket(t, `{
	  "announced": ["192.168.0.151:51821"],
	  "meshes": [
	    {"label":"default","peers":1},
	    {"label":"kc","peers":1}
	  ]
	}`)

	out := captureStdout(t, func() {
		if err := cmdPaths([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "not all 2") {
		t.Errorf("an old daemon's single list was not marked partial:\n%s", out)
	}
}
