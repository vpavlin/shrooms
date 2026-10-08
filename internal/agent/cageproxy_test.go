package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

// A caged session on the laptop asks pi5's "proj" for work, through its own
// agent's socket (ADR-044): what passes, what does not, who it is said to be,
// and what the receiver decides.
func TestACageReachesAgentsOnlyThroughItsOwn(t *testing.T) {
	r := newA2A(t) // pi5's agent, with its session "proj"
	laptop := newTestManager(t, t.TempDir())
	laptop.Create("boxed", t.TempDir())
	boxed, _ := laptop.Get("boxed")
	self, pi5 := netip.MustParseAddr("fd00::1"), netip.MustParseAddr("fd00::2")
	laptop.Machines = func() ([]Machine, error) { return []Machine{{"laptop", self}, {"pi5", pi5}}, nil }
	own := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), laptop, func(netip.Addr) string { return "" }))
	t.Cleanup(own.Close)
	laptop.agentAt = func(a netip.Addr) string {
		if a == pi5 {
			return r.remote.URL
		}
		return own.URL
	}
	if err := laptop.startCageProxy(boxed, "shrooms-boxed-abc123"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.stopCageProxy("shrooms-boxed-abc123") })
	sock := filepath.Join(laptop.proxyDir("shrooms-boxed-abc123"), "proxy.sock")
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	do := func(method, path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, "http://cage"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	rpc := func(method string, params any) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	}

	if code, out := do("GET", "/machines", nil); code != 200 || !strings.Contains(out, `"pi5"`) {
		t.Fatalf("the machines are not offered to the cage: %d %s", code, out)
	}

	// It claims to be another session; its agent says who it is.
	lie := send("c1", "review this", false)
	lie["message"].(map[string]any)["metadata"] = map[string]any{"shrooms/from": "laptop/not-caged"}
	_, out := do("POST", "/peer/fd00::2/a2a/proj", rpc("SendMessage", lie))
	if !strings.Contains(out, "takes no tasks from caged agents") {
		t.Fatalf("a session outside a cage took a task from a caged agent by default: %s", out)
	}

	// Its owner lets it.
	resp, err := http.Post(r.remote.URL+"/v1/sessions/proj/settings", "application/json", strings.NewReader(`{"accept_caged":true}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("accept_caged not set: %v %v", err, resp)
	}
	resp.Body.Close()
	_, out = do("POST", "/peer/fd00::2/a2a/proj", rpc("SendMessage", lie))
	ts := r.m.Tasks("proj")
	if len(ts) != 1 || !strings.Contains(ts[0].From, "(laptop/boxed, in a cage)") {
		t.Fatalf("the task does not name its caged asker as its agent knows it: %+v %s", ts, out)
	}
	if !strings.Contains(out, `"shrooms/caged":true`) {
		t.Errorf("the task does not say its asker is caged: %s", out)
	}
	if code, _ := do("POST", "/peer/fd00::2/a2a/proj", rpc("GetTask", map[string]any{"id": ts[0].ID})); code != 200 {
		t.Errorf("a cage cannot follow its task: %d", code)
	}

	// What a cage may not do: make, change or delete sessions, cancel, or
	// finish another machine's tasks.
	for _, x := range []struct{ method, path string }{
		{"POST", "/peer/fd00::2/v1/sessions"},
		{"POST", "/peer/fd00::2/v1/sessions/proj/settings"},
		{"DELETE", "/peer/fd00::2/v1/sessions/proj"},
		{"POST", "/peer/fd00::1/v1/sessions"},
		{"POST", "/peer/fd00::2/v1/tasks/" + ts[0].ID},
	} {
		if code, _ := do(x.method, x.path, map[string]any{"name": "escaped", "dir": "/tmp"}); code != http.StatusForbidden {
			t.Errorf("%s %s from a cage: %d, want 403", x.method, x.path, code)
		}
	}
	if code, _ := do("POST", "/peer/fd00::2/a2a/proj", rpc("CancelTask", map[string]any{"id": ts[0].ID})); code != http.StatusForbidden {
		t.Errorf("a cage cancelled a task: %d", code)
	}
	if _, ok := laptop.Get("escaped"); ok {
		t.Fatal("a cage made a session on its own machine")
	}

	// And an agent refuses a caged request to change settings, however it
	// arrives.
	req, _ := http.NewRequest("POST", r.remote.URL+"/v1/sessions/proj/settings", strings.NewReader(`{"auto_approve":true}`))
	req.Header.Set(cagedHeader, "boxed")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("settings changed by a caged request: %v %v", err, resp)
	}
}

func TestWhoTakesTasksFromCagedAgents(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("free", t.TempDir())
	free, _ := m.Get("free")
	if free.AcceptsCaged() {
		t.Error("a session outside a cage takes tasks from caged agents by default")
	}
	free.cage = &Cage{}
	if !free.AcceptsCaged() {
		t.Error("a caged session refuses caged agents by default")
	}
	free.SetAcceptCaged(false, "phone")
	if free.AcceptsCaged() || free.Info().AcceptCaged {
		t.Error("the owner's no is not kept")
	}
}
