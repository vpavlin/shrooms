package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	own := localServer(Handler(slog.New(slog.DiscardHandler), laptop, func(netip.Addr) string { return "" }))
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
	// By default it is held for the owner, not started and not refused
	// (ADR-070): a request in the apps, naming the cage as its agent knows it.
	_, out := do("POST", "/peer/fd00::2/a2a/proj", rpc("SendMessage", lie))
	if !strings.Contains(out, taskAuthRequired) {
		t.Fatalf("a session outside a cage did not hold a caged agent's task for its owner: %s", out)
	}
	proj, _ := r.m.Get("proj")
	if asks := proj.Info().TaskRequests; len(asks) != 1 || asks[0].From != "pi5/boxed" {
		t.Fatalf("the held task is not offered to the owner: %+v", asks)
	}

	// Its owner lets it: the held task goes on.
	resp, err := http.Post(r.remote.URL+"/v1/sessions/proj/settings", "application/json", strings.NewReader(`{"accept_caged_from":["pi5/boxed"]}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("accept_caged_from not set: %v %v", err, resp)
	}
	resp.Body.Close()
	ts := r.m.Tasks("proj")
	if len(ts) != 1 || !strings.Contains(ts[0].From, "(laptop/boxed, in a cage)") || ts[0].State == taskAuthRequired {
		t.Fatalf("the task does not name its caged asker as its agent knows it, or is still held: %+v %s", ts, out)
	}
	if asks := proj.Info().TaskRequests; len(asks) != 0 {
		t.Errorf("an allowed sender's task is still a request: %+v", asks)
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

// A caged sender the owner declines: its held tasks are rejected, its asker
// can see why, and nothing of it is remembered, so its next task asks again.
// A sender the owner allowed is not held; anyone else still is (ADR-070).
func TestTheOwnerDecidesOnEachCage(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("publisher", t.TempDir())
	pub, _ := m.Get("publisher")

	held, err := m.SubmitHeld(pub, "m1", "laptop (laptop/fileware, in a cage)", "laptop/fileware", "publish this", "Publish", nil)
	if err != nil || held.State != taskAuthRequired {
		t.Fatalf("not held: %+v %v", held, err)
	}
	if n := m.DeclineCaged(pub, "laptop/fileware", "phone"); n != 1 {
		t.Fatalf("declined %d tasks, want 1", n)
	}
	got, _ := m.tasks.get(held.ID)
	if got.State != taskRejected || !strings.Contains(got.Summary, "declined by the owner") {
		t.Errorf("a declined task: %+v", got)
	}
	if pub.AllowsCaged("laptop/fileware") {
		t.Error("declining remembered the sender as allowed")
	}

	if err := pub.SetAcceptCagedFrom([]string{"laptop/fileware"}, "phone"); err != nil {
		t.Fatal(err)
	}
	if !pub.AllowsCaged("laptop/fileware") || pub.AllowsCaged("laptop/other") || pub.AllowsCaged("pi5/fileware") {
		t.Error("the allow list names one cage, and only it")
	}
	if err := pub.SetAcceptCagedFrom([]string{"laptop/*"}, "phone"); err != nil || !pub.AllowsCaged("laptop/other") {
		t.Errorf("MACHINE/* does not allow that machine's cages: %v", err)
	}
	if err := pub.SetAcceptCagedFrom([]string{"fileware"}, "phone"); err == nil {
		t.Error("an entry with no machine was accepted")
	}

	// Kept across a restart.
	again := newTestManager(t, m.dir)
	p2, ok := again.Get("publisher")
	if !ok || !p2.AllowsCaged("laptop/other") {
		t.Error("the allow list did not survive a restart")
	}
}

// A caged session may publish, as itself, on its own machine (ADR-079).
func TestCageMayPublish(t *testing.T) {
	if !proxyAllowed(http.MethodPost, "/v1/sessions/x/publish", "") {
		t.Error("publish refused to a cage")
	}
	if proxyAllowed(http.MethodGet, "/v1/sessions/x/publish", "") || proxyAllowed(http.MethodPost, "/v1/sessions/x/publish/more", "") {
		t.Error("more than publish allowed")
	}
}
