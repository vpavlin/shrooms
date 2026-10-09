package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

// Inside a cage (SHROOMS_AGENT_PROXY), the shrooms tools reach the machines
// and their agents only through the cage's socket (ADR-044).
func TestInACageTheToolsGoThroughItsSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "proxy.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/machines" {
			json.NewEncoder(w).Encode([]map[string]string{{"name": "laptop", "addr": "fd00::1"}, {"name": "pi5", "addr": "fd00::2"}})
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"task":{"id":"proj:x","status":{"state":"TASK_STATE_WORKING"}}}}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("SHROOMS_AGENT_PROXY", sock)
	t.Setenv("SHROOMS_AGENT_SESSION", "boxed")

	c := newA2AClient("/nonexistent/daemon.sock")
	if _, err := c.send("pi5/proj", "review this", "", false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "POST /peer/fd00::2/a2a/proj"
	for _, s := range seen {
		if s == want {
			return
		}
	}
	t.Fatalf("a message from a cage did not go through its socket as %q: %q", want, seen)
}
