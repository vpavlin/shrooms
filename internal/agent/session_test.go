package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T, state string) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := NewManager(ctx, slog.New(slog.DiscardHandler), state, fakeClaudeBin(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, in := range m.List() {
			if s, ok := m.Get(in.Name); ok {
				s.stop()
			}
		}
	})
	return m
}

// waitFor follows a session's events until one matches, or fails.
func waitFor(t *testing.T, s *Session, after uint64, match func(Event) bool) Event {
	t.Helper()
	backlog, ch := s.Since(after)
	defer s.Unsubscribe(ch)
	for _, e := range backlog {
		if match(e) {
			return e
		}
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed while waiting")
			}
			if match(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out; session is %+v", s.Info())
		}
	}
}

func claudeType(e Event) string {
	var h struct{ Type, Subtype string }
	json.Unmarshal(e.Data, &h)
	if h.Subtype != "" {
		return h.Type + "/" + h.Subtype
	}
	return h.Type
}

func assistantText(e Event) string {
	var m struct {
		Type    string
		Message struct {
			Content []struct{ Type, Text string }
		}
	}
	json.Unmarshal(e.Data, &m)
	if e.Kind != "claude" || m.Type != "assistant" {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Message.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// A turn goes in, Claude Code's answer comes back as numbered events, and the
// conversation id is kept so the session can be resumed.
func TestATurnRoundTrips(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	if _, err := m.Create("proj", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("proj")
	if err := s.Send("hello", "nothing"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })

	got, _ := s.Since(0)
	var kinds []string
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Errorf("event %d has seq %d: numbering must have no gaps", i, e.Seq)
		}
		kinds = append(kinds, e.Kind+":"+claudeType(e))
	}
	if got[0].Kind != "message" || got[0].By != "nothing" {
		t.Errorf("the turn was not recorded as sent by the device: %+v", got[0])
	}
	if !strings.Contains(strings.Join(kinds, " "), "claude:system/init") {
		t.Errorf("events %v", kinds)
	}
	var echoed bool
	for _, e := range got {
		echoed = echoed || assistantText(e) == "echo: hello"
	}
	if !echoed {
		t.Errorf("no echo in %v", kinds)
	}
	if in := s.Info(); in.State != Idle {
		t.Errorf("state after the result: %s", in.State)
	}
	reg, _ := os.ReadFile(filepath.Join(state, "sessions.json"))
	if !strings.Contains(string(reg), "fake-session-1") {
		t.Errorf("the conversation id was not saved: %s", reg)
	}
}

// The reason this exists: a tool that needs permission waits for an answer
// from a device, and the answer decides what happens.
func TestAPromptIsAnsweredFromADevice(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) {
			m := newTestManager(t, t.TempDir())
			m.Create("proj", t.TempDir())
			s, _ := m.Get("proj")
			if err := s.Send("run ls -la", "nothing"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
			if in := s.Info(); in.State != Waiting || in.Pending != 1 {
				t.Fatalf("while a prompt waits the session is %+v", in)
			}

			if err := s.Answer("req-1", allow, "not on a Friday", nil, "nothing"); err != nil {
				t.Fatal(err)
			}
			end := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
			got, _ := s.Since(0)
			var texts []string
			var answered bool
			for _, e := range got {
				if t := assistantText(e); t != "" {
					texts = append(texts, t)
				}
				answered = answered || (e.Kind == "answer" && e.By == "nothing")
			}
			if !answered {
				t.Error("the answer was not recorded with the device that gave it")
			}
			want := "done"
			if !allow {
				want = "denied: not on a Friday"
			}
			if !strings.Contains(strings.Join(texts, "|"), want) {
				t.Errorf("after answering allow=%v the model said %v, want %q", allow, texts, want)
			}
			if in := s.Info(); in.Pending != 0 || in.State != Idle {
				t.Errorf("after the turn ended (seq %d) the session is %+v", end.Seq, in)
			}
			if err := s.Answer("req-1", true, "", nil, "nothing"); err == nil {
				t.Error("the same prompt could be answered twice")
			}
		})
	}
}

// A stopped session comes back as the same conversation.
func TestAStoppedSessionResumesItsConversation(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("one", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })

	s.stop()
	stopped := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "stopped" })
	if s.Info().Running {
		t.Fatal("still running after stop")
	}

	s.Send("two", "")
	init := waitFor(t, s, stopped.Seq, func(e Event) bool { return claudeType(e) == "system/init" })
	var h struct {
		SessionID string `json:"session_id"`
		Resumed   bool   `json:"resumed"`
	}
	json.Unmarshal(init.Data, &h)
	if !h.Resumed || h.SessionID != "fake-session-1" {
		t.Errorf("the second process did not resume the conversation: %+v", h)
	}
}

// A process that ends while a prompt waits takes the prompt with it; listing
// it would invite an answer that goes nowhere.
func TestAPromptDiesWithItsProcess(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("run rm -rf /", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	s.stop()
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "stopped" })
	if in := s.Info(); in.Pending != 0 || in.State != Idle {
		t.Errorf("after the process ended the session is %+v", in)
	}
	if err := s.Answer("req-1", true, "", nil, ""); err == nil {
		t.Error("a prompt of a dead process could be answered")
	}
}

// Interrupt ends a turn that would otherwise run on.
func TestInterruptEndsATurn(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("slow", "")
	time.Sleep(100 * time.Millisecond)
	if err := s.Interrupt(""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/error_during_execution" })
}

// A process stuck in a turn — deaf to an interrupt, as one is on a dropped
// connection — is restarted on its own, on the same conversation, and takes
// the next message.
func TestRestartEndsAStuckProcess(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	m.Create("other", t.TempDir())
	s, _ := m.Get("proj")
	other, _ := m.Get("other")
	other.Send("hello", "")
	waitFor(t, other, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	s.Send("hang", "")
	first := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "system/init" })
	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- s.Restart("phone.home") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the restart waited on a process that does not end")
	}
	r := waitFor(t, s, first.Seq, func(e Event) bool { return e.Kind == "restarted" })
	if r.By != "phone.home" {
		t.Fatalf("restarted by %q", r.By)
	}
	again := waitFor(t, s, r.Seq, func(e Event) bool { return claudeType(e) == "system/init" })
	var init struct {
		SessionID string `json:"session_id"`
		Resumed   bool   `json:"resumed"`
	}
	json.Unmarshal(again.Data, &init)
	if !init.Resumed || init.SessionID != "fake-session-1" {
		t.Fatalf("not the same conversation: %s", again.Data)
	}
	s.Send("after", "")
	waitFor(t, s, r.Seq, func(e Event) bool { return claudeType(e) == "result/success" })
	if !other.Info().Running {
		t.Fatal("another session's process was ended too")
	}
}

// Idle means idle: a process waiting on a prompt is not stopped however long
// the owner takes, and one with nothing to do is.
func TestOnlyAnIdleProcessIsStopped(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("run ls", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	s.stopIfIdle(time.Now().Add(24*time.Hour), time.Minute)
	time.Sleep(200 * time.Millisecond)
	if !s.Info().Running {
		t.Fatal("a process waiting for an answer was stopped as idle")
	}
	s.Answer("req-1", true, "", nil, "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	s.stopIfIdle(time.Now().Add(24*time.Hour), time.Minute)
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "stopped" })
}

// Sessions and their history survive a restart of shrooms-agent, and the
// numbering carries on where it stopped.
func TestSessionsSurviveARestart(t *testing.T) {
	state, dir := t.TempDir(), t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", dir)
	s, _ := m.Get("proj")
	s.Send("before", "")
	last := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	s.stop()
	stopped := waitFor(t, s, last.Seq, func(e Event) bool { return e.Kind == "stopped" })

	m2 := newTestManager(t, state)
	s2, ok := m2.Get("proj")
	if !ok {
		t.Fatal("the session was not kept")
	}
	if in := s2.Info(); in.Dir != dir || in.LastSeq != stopped.Seq {
		t.Errorf("after a restart: %+v, want dir %s and last seq %d", in, dir, stopped.Seq)
	}
	s2.Send("after", "")
	e := waitFor(t, s2, stopped.Seq, func(e Event) bool { return e.Kind == "message" })
	if e.Seq != stopped.Seq+1 {
		t.Errorf("numbering restarted: %d after %d", e.Seq, stopped.Seq)
	}
}

// Over HTTP: events stream as SSE, a reconnect with Last-Event-ID resumes
// without repeats, and every action is attributed to the calling device by its
// address.
func TestTheAPIOverHTTP(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	phone := netip.MustParseAddr("fd7b:15fb:5ec1:b2bf:31ab:8ad3:c152:728a")
	who := func(a netip.Addr) string {
		if a == phone || a.IsLoopback() {
			return "nothing"
		}
		return ""
	}
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, who))
	t.Cleanup(srv.Close)
	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	dir := t.TempDir()
	if r := do("POST", "/v1/sessions", fmt.Sprintf(`{"name":"proj","dir":%q}`, dir)); r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %s", r.Status)
	}
	if r := do("POST", "/v1/sessions", `{"name":"bad","dir":"/no/such/dir"}`); r.StatusCode != http.StatusBadRequest {
		t.Errorf("a session in a missing directory: %s", r.Status)
	}
	if r := do("POST", "/v1/sessions/proj/messages", `{"text":"run make test"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("message: %s", r.Status)
	}
	s, _ := m.Get("proj")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })

	var list struct{ Sessions []Info }
	json.NewDecoder(do("GET", "/v1/sessions", "").Body).Decode(&list)
	if len(list.Sessions) != 1 || list.Sessions[0].State != Waiting || list.Sessions[0].Pending != 1 {
		t.Fatalf("list while a prompt waits: %+v", list.Sessions)
	}
	if r := do("POST", "/v1/sessions/proj/prompts/req-1", `{"allow":true}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("answer: %s", r.Status)
	}
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })

	// The whole history, then a reconnect from the middle.
	first := readSSE(t, srv.URL+"/v1/sessions/proj/events", "", 0)
	if len(first) < 6 {
		t.Fatalf("only %d events: %+v", len(first), first)
	}
	for _, e := range first {
		if (e.Kind == "message" || e.Kind == "answer") && e.By != "nothing" {
			t.Errorf("%s not attributed to the calling device: %+v", e.Kind, e)
		}
	}
	mid := first[2].Seq
	rest := readSSE(t, srv.URL+"/v1/sessions/proj/events", fmt.Sprint(mid), 0)
	if len(rest) == 0 || rest[0].Seq != mid+1 || len(rest) != len(first)-3 {
		t.Errorf("reconnecting after %d gave %d events starting at %v", mid, len(rest), rest)
	}

	// Live: an event that happens while connected arrives.
	got := make(chan []Event, 1)
	go func() {
		got <- readSSE(t, srv.URL+"/v1/sessions/proj/events?after="+fmt.Sprint(first[len(first)-1].Seq), "", 2)
	}()
	time.Sleep(200 * time.Millisecond)
	do("POST", "/v1/sessions/proj/messages", `{"text":"live"}`)
	select {
	case live := <-got:
		if len(live) < 2 || live[0].Kind != "message" {
			t.Errorf("live events: %+v", live)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no live events arrived")
	}

	// Settings, both ways a client can send them.
	for _, c := range []struct {
		method, path, body string
		want               bool
	}{
		{"PATCH", "/v1/sessions/proj", `{"auto_approve":true}`, true},
		{"POST", "/v1/sessions/proj/settings", `{"auto_approve":false}`, false},
	} {
		var in Info
		r := do(c.method, c.path, c.body)
		json.NewDecoder(r.Body).Decode(&in)
		if r.StatusCode != http.StatusOK || in.AutoApprove != c.want {
			t.Errorf("%s %s: %s, auto_approve %v", c.method, c.path, r.Status, in.AutoApprove)
		}
	}

	if r := do("DELETE", "/v1/sessions/proj", ""); r.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %s", r.Status)
	}
	if r := do("GET", "/v1/sessions/proj/events", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("events of a deleted session: %s", r.Status)
	}
}

// readSSE reads events from an SSE stream: all of the backlog when want is 0
// (stopping at the first quiet moment), or exactly want events.
func readSSE(t *testing.T, url, lastID string, want int) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var out []Event
	for {
		quiet := time.After(500 * time.Millisecond)
		if want > 0 {
			quiet = nil
		}
		select {
		case l, ok := <-lines:
			if !ok {
				return out
			}
			if strings.HasPrefix(l, "data: ") {
				var e Event
				json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &e)
				out = append(out, e)
				if want > 0 && len(out) == want {
					return out
				}
			}
		case <-quiet:
			return out
		case <-ctx.Done():
			return out
		}
	}
}

// "~" is the agent's own home: the phone cannot know where that is.
func TestATildeIsThisMachinesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, "proj"), 0o700)
	m := newTestManager(t, t.TempDir())
	in, err := m.Create("p", "~/proj")
	if err != nil {
		t.Fatal(err)
	}
	if in.Dir != filepath.Join(home, "proj") {
		t.Errorf("~/proj became %s", in.Dir)
	}
}

// Opening a long session at its tail: only the last N events, the newest
// last — and a reconnect is never trimmed, since it is catching up.
func TestEventsCanStartAtTheTail(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	var last uint64
	for i := 0; i < 3; i++ {
		s.Send(fmt.Sprint("turn ", i), "")
		last = waitFor(t, s, last, func(e Event) bool { return claudeType(e) == "result/success" }).Seq
	}
	all, _ := s.Since(0)
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)

	tail := readSSE(t, srv.URL+"/v1/sessions/proj/events?tail=3", "", 0)
	if len(tail) != 3 || tail[2].Seq != all[len(all)-1].Seq {
		t.Fatalf("tail=3 gave %d events ending at %v; the session has %d ending at %d",
			len(tail), tail, len(all), all[len(all)-1].Seq)
	}
	mid := all[2].Seq
	rest := readSSE(t, srv.URL+fmt.Sprintf("/v1/sessions/proj/events?tail=3&after=%d", mid), "", 0)
	if len(rest) != len(all)-3 {
		t.Errorf("a reconnect after %d with tail=3 gave %d events, want all %d after it", mid, len(rest), len(all)-3)
	}
}

// A directory that is not there yet is made for the session — a new project
// started from the phone — but a refused request makes nothing, and a file
// where the directory would be is still refused.
func TestCreateMakesAMissingDirectory(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	base := t.TempDir()

	dir := filepath.Join(base, "new", "project")
	in, err := m.Create("fresh", dir)
	if err != nil {
		t.Fatalf("create in a missing directory: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || in.Dir != dir {
		t.Fatalf("directory not made: %v (dir %q)", err, in.Dir)
	}

	refused := filepath.Join(base, "refused")
	if _, err := m.Create("fresh", refused); err == nil {
		t.Fatal("a second session of the same name was made")
	}
	if _, err := os.Stat(refused); !os.IsNotExist(err) {
		t.Fatalf("a refused request left a directory behind: %v", err)
	}

	file := filepath.Join(base, "a-file")
	os.WriteFile(file, []byte("x"), 0o644)
	if _, err := m.Create("onfile", file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file accepted as the directory: %v", err)
	}
}

// Claude Code's own error messages name their model "<synthetic>"; the
// session keeps the model that actually answered.
func TestASyntheticMessageIsNotTheModel(t *testing.T) {
	s := &Session{}
	s.observe(json.RawMessage(`{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10}}}`))
	s.observe(json.RawMessage(`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Connection dropped (ECONNRESET)"}],"usage":{"input_tokens":0}}}`))
	if s.model != "claude-opus-5-5" {
		t.Fatalf("model %q", s.model)
	}
}

// A renamed session keeps its history, its process and its settings under the
// new name — across a restart of the agent too — and its followers are told.
func TestARenamedSessionKeepsEverything(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", t.TempDir())
	m.Create("other", t.TempDir())
	s, _ := m.Get("proj")
	s.SetStarred(true)
	s.Send("before", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()

	if _, err := m.Rename("proj", "other", "x"); err == nil {
		t.Error("renamed onto another session")
	}
	if _, err := m.Rename("proj", "../evil", "x"); err == nil {
		t.Error("renamed to a path")
	}
	in, err := m.Rename("proj", "renamed", "phone.home")
	if err != nil || in.Name != "renamed" || !in.Starred {
		t.Fatalf("rename: %+v %v", in, err)
	}
	if _, ok := m.Get("proj"); ok {
		t.Error("still found by the old name")
	}
	r := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "renamed" })
	if r.By != "phone.home" || !strings.Contains(string(r.Data), `"from":"proj"`) {
		t.Errorf("renamed event %+v", r)
	}
	s.Send("after", "")
	waitFor(t, s, r.Seq, func(e Event) bool { return claudeType(e) == "result/success" })
	s.mu.Lock()
	same := s.proc == p
	s.mu.Unlock()
	if !same {
		t.Error("the process was restarted")
	}
	if _, err := os.Stat(filepath.Join(state, "events", "proj.jsonl")); !os.IsNotExist(err) {
		t.Error("the old log is still there")
	}

	m2 := newTestManager(t, state)
	s2, ok := m2.Get("renamed")
	if !ok {
		t.Fatal("the new name was not kept")
	}
	var texts []string
	backlog, ch := s2.Since(0)
	s2.Unsubscribe(ch)
	for _, e := range backlog {
		if e.Kind == "message" {
			var d struct{ Text string }
			json.Unmarshal(e.Data, &d)
			texts = append(texts, d.Text)
		}
	}
	if strings.Join(texts, ",") != "before,after" || !s2.Info().Starred {
		t.Fatalf("after a restart: %v %+v", texts, s2.Info())
	}
}
