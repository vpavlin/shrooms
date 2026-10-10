package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// notes catches what a worker's agent sends the asker's agent.
type notes struct {
	mu   sync.Mutex
	got  []string // "path text"
	srv  *httptest.Server
	seen chan struct{}
}

func catchNotes(t *testing.T, m *Manager) *notes {
	n := &notes{seen: make(chan struct{}, 10)}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Text string }
		json.NewDecoder(r.Body).Decode(&b)
		n.mu.Lock()
		n.got = append(n.got, r.URL.Path+" "+b.Text)
		n.mu.Unlock()
		n.seen <- struct{}{}
	}))
	t.Cleanup(n.srv.Close)
	m.agentAt = func(netip.Addr) string { return n.srv.URL }
	return n
}

func (n *notes) wait(t *testing.T, d time.Duration) (string, bool) {
	t.Helper()
	select {
	case <-n.seen:
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.got[len(n.got)-1], true
	case <-time.After(d):
		return "", false
	}
}

// A task that needs its asker's answer tells the asker, in its own session:
// before, tasks sat blocked on agents that never looked (2026-10-10).
func TestABlockedTaskTellsItsAsker(t *testing.T) {
	r := newA2A(t)
	n := catchNotes(t, r.m)
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-b", "do the thing", false))
	if code := update(t, r.local.URL, "proj:m-b", "blocked", "which branch, main or dev?"); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	got, ok := n.wait(t, 5*time.Second)
	if !ok {
		t.Fatal("the asker was not told")
	}
	if !strings.HasPrefix(got, "/v1/sessions/jimmy/messages ") || !strings.Contains(got, "needs your answer") ||
		!strings.Contains(got, "which branch, main or dev?") || !strings.Contains(got, `task "proj:m-b"`) {
		t.Errorf("note: %q", got)
	}
}

// The result goes to an asker that did not wait for it, and not to one that
// did: a note starts a turn, and that costs.
func TestTheResultGoesToAnAskerThatDidNotWait(t *testing.T) {
	r := newA2A(t)
	n := catchNotes(t, r.m)
	// The asker waits (a blocking send) while the worker finishes.
	replied := make(chan rpcResp, 1)
	go func() { replied <- call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-w", "wait for me", true)) }()
	for !r.m.awaited("proj:m-w") {
		time.Sleep(10 * time.Millisecond)
	}
	update(t, r.local.URL, "proj:m-w", "done", "waited for")
	if got := <-replied; got.Result.Task.Status.State != taskCompleted {
		t.Fatalf("the waiting asker got %+v", got.Result.Task.Status)
	}
	if got, ok := n.wait(t, 500*time.Millisecond); ok {
		t.Errorf("an asker that waited was told anyway: %q", got)
	}
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-n", "tell me later", false))
	update(t, r.local.URL, "proj:m-n", "done", "here it is")
	got, ok := n.wait(t, 5*time.Second)
	if !ok || !strings.Contains(got, "completed") || !strings.Contains(got, "here it is") || !strings.Contains(got, "task_ack") {
		t.Errorf("an asker that did not wait: %q %v", got, ok)
	}
}

// The apps' three answers to a blocked task, over /v1 (Basecamp's core
// forwards nothing else): answer it, remind the asker, call it off.
func TestABlockedTaskCanBeAnsweredNudgedOrCancelled(t *testing.T) {
	r := newA2A(t)
	n := catchNotes(t, r.m)
	post := func(path, body string) int {
		resp, err := http.Post(r.remote.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-a", "start", false))
	update(t, r.local.URL, "proj:m-a", "blocked", "which one?")
	n.wait(t, 5*time.Second)

	if code := post("/v1/tasks/proj:m-a/nudge", ""); code != http.StatusOK {
		t.Errorf("nudge: %d", code)
	}
	if got, ok := n.wait(t, 5*time.Second); !ok || !strings.Contains(got, "which one?") {
		t.Errorf("the nudge did not reach the asker: %q", got)
	}

	// The answer is delivered when the session is free: the task's own turn ends first.
	s, _ := r.m.Get("proj")
	for deadline := time.Now().Add(20 * time.Second); s.Info().State != Idle && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if code := post("/v1/tasks/proj:m-a/answer", `{"text":"the second"}`); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	ev := waitFor(t, s, 0, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "the second")
	})
	if !strings.Contains(string(ev.Data), "answered by pi5.default, in place of the asker") {
		t.Errorf("the answer does not say whose it is: %s", ev.Data)
	}
	if code := post("/v1/tasks/proj:m-a/answer", `{"text":""}`); code != http.StatusBadRequest {
		t.Errorf("an empty answer: %d", code)
	}

	if code := post("/v1/tasks/proj:m-a/cancel", ""); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	if ts := r.m.Tasks("proj"); len(ts) != 1 || ts[0].State != taskCanceled {
		t.Errorf("not cancelled: %+v", ts)
	}
	// A person's task has no agent to remind.
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", map[string]any{
		"message": map[string]any{"messageId": "m-p", "role": "ROLE_USER", "parts": []any{map[string]any{"text": "from a phone"}}}})
	if code := post("/v1/tasks/proj:m-p/nudge", ""); code != http.StatusConflict {
		t.Errorf("nudged nobody: %d", code)
	}
}

// A cage finishes its own session's tasks and nothing more: the new task
// routes are not for it.
func TestACageOnlyUpdatesTasks(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/tasks/proj:m1": true, "/v1/tasks/proj:m1/cancel": false, "/v1/tasks/proj:m1/answer": false,
		"/v1/tasks/proj:m1/ack": false, "/v1/tasks/": false, "/v1/tasksx": false,
	} {
		if isTaskUpdate(path) != want || proxyAllowed(http.MethodPost, path, "") != want {
			t.Errorf("%s: want %v", path, want)
		}
	}
}
