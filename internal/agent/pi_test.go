package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPiSession(t *testing.T) (*Manager, *Session) {
	t.Helper()
	m := newTestManager(t, t.TempDir())
	m.Register(Pi{}, fakePiBin(t))
	if _, err := m.CreateWith("pi-proj", t.TempDir(), "pi"); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("pi-proj")
	return m, s
}

func isResult(e Event) bool { return strings.HasPrefix(claudeType(e), "result/") }

// A pi turn reaches the apps in the shape a Claude Code one does: the reply
// streamed, the tool call and its output, the end of the turn with its cost,
// and the session's model and context — so nothing in the apps knows pi.
func TestAPiSessionRoundTrips(t *testing.T) {
	_, s := newPiSession(t)
	var partial strings.Builder
	backlog, ch := s.Since(0)
	defer s.Unsubscribe(ch)
	if len(backlog) != 0 {
		t.Fatalf("a new session has events: %v", backlog)
	}
	if err := s.Send("run make test", "nothing"); err != nil {
		t.Fatal(err)
	}
	var got []Event
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case e := <-ch:
			if e.Kind == "partial" {
				var d struct{ Text string }
				json.Unmarshal(e.Data, &d)
				partial.WriteString(d.Text)
				continue
			}
			got = append(got, e)
			done = isResult(e)
		case <-deadline:
			t.Fatalf("no end of turn; events %v", got)
		}
	}
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.Kind+":"+claudeType(e))
	}
	want := "message: claude:system/init claude:assistant claude:user claude:assistant claude:result/success"
	if strings.Join(kinds, " ") != want {
		t.Errorf("events\n got %s\nwant %s", strings.Join(kinds, " "), want)
	}
	var tool struct {
		Message struct {
			Content []struct {
				Type, Name string
				Input      struct{ Command string }
			}
		}
	}
	json.Unmarshal(got[2].Data, &tool)
	if c := tool.Message.Content; len(c) != 1 || c[0].Type != "tool_use" || c[0].Name != "bash" || c[0].Input.Command != "make test" {
		t.Errorf("the tool call: %s", got[2].Data)
	}
	if !strings.Contains(string(got[3].Data), `"tool_result"`) || !strings.Contains(string(got[3].Data), "ran make test") {
		t.Errorf("the tool's output: %s", got[3].Data)
	}
	if assistantText(got[4]) != "done" || partial.String() != "done" {
		t.Errorf("the reply %q, streamed as %q", assistantText(got[4]), partial.String())
	}
	if !strings.Contains(string(got[5].Data), `"total_cost_usd":0.02`) {
		t.Errorf("the turn's cost: %s", got[5].Data)
	}
	in := s.Info()
	if in.Harness != "pi" || in.Caps.Approve || in.Model != "local/qwen3.5:0.8b" ||
		in.ContextWindow != 128000 || in.ContextUsed != 1200 || in.State != Idle {
		t.Errorf("info %+v", in)
	}
	first := s.convID
	if !strings.HasPrefix(first, "01a1-") {
		t.Errorf("the conversation id to resume by: %q", first)
	}

	// Stopped and sent to again, it resumes the same pi session.
	s.stop()
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "stopped" })
	last := s.Info().LastSeq
	s.Send("again", "")
	init := waitFor(t, s, last, func(e Event) bool { return claudeType(e) == "system/init" })
	if !strings.Contains(string(init.Data), `"session_id":"`+first+`"`) {
		t.Errorf("did not resume: %s", init.Data)
	}
	waitFor(t, s, last, func(e Event) bool { return assistantText(e) == "echo: again" })
}

// An extension's dialog is a question: it waits, auto-approve or not, and
// what is picked reaches pi; meanwhile a message sent is queued, not refused.
func TestAPiDialogIsAQuestion(t *testing.T) {
	_, s := newPiSession(t)
	s.SetAutoApprove(true, "")
	s.Send("ask Run the migration?", "")
	req := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	if !strings.Contains(string(req.Data), `"tool_name":"AskUserQuestion"`) ||
		!strings.Contains(string(req.Data), `"label":"Block"`) {
		t.Fatalf("the dialog: %s", req.Data)
	}
	time.Sleep(200 * time.Millisecond)
	if in := s.Info(); in.State != Waiting || in.Pending != 1 {
		t.Fatalf("not waiting for the answer: %+v", in)
	}
	if err := s.Send("and then tidy up", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer("ui-u1", true, "", map[string]string{"Run the migration?": "Block"}, "nothing"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return assistantText(e) == "picked Block" })
	for _, e := range mustSince(s) {
		if strings.Contains(assistantText(e), "refused") {
			t.Errorf("a message sent mid-turn was refused: %q", assistantText(e))
		}
	}

	_, s2 := newPiSession(t)
	s2.Send("ask Again?", "")
	waitFor(t, s2, 0, func(e Event) bool { return claudeType(e) == "control_request" })
	time.Sleep(100 * time.Millisecond)
	if err := s2.Answer("ui-u1", false, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s2, 0, func(e Event) bool { return assistantText(e) == "cancelled" })
}

func mustSince(s *Session) []Event {
	ev, ch := s.Since(0)
	s.Unsubscribe(ch)
	return ev
}

// A model that fails says why, and the turn ends as failed.
func TestAPiErrorIsShown(t *testing.T) {
	_, s := newPiSession(t)
	s.Send("fail", "")
	r := waitFor(t, s, 0, isResult)
	if claudeType(r) != "result/error_during_execution" {
		t.Errorf("result %s", r.Data)
	}
	waitFor(t, s, 0, func(e Event) bool { return assistantText(e) == "error: connection refused" })
}

// What was said in pi before the agent had the session — read from pi's own
// session file — is its history, and searchable.
func TestAPiSessionsHistoryIsItsTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	os.MkdirAll(filepath.Join(dir, "sessions", "--home-x-proj--"), 0o700)
	lines := []string{
		`{"type":"session","version":3,"id":"abc-123","timestamp":"2026-10-03T10:00:00.000Z","cwd":"/home/x/proj"}`,
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-03T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"Fix the parser"}]}}`,
		`{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-03T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"},{"type":"toolCall","id":"c","name":"read","arguments":{}}]}}`,
		`{"type":"message","id":"a3","parentId":"a2","timestamp":"2026-10-03T10:00:03.000Z","message":{"role":"toolResult","toolCallId":"c","content":[{"type":"text","text":"file"}]}}`,
		`{"type":"message","id":"a4","parentId":"a3","timestamp":"2026-10-03T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the Parser."}]}}`,
		`{"type":"model_change","id":"a5","parentId":"a4","timestamp":"2026-10-03T10:00:05.000Z"}`,
	}
	os.WriteFile(filepath.Join(dir, "sessions", "--home-x-proj--", "2026-10-03T10-00-00-000Z_abc-123.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o600)

	_, s := newPiSession(t)
	s.convID = "abc-123"
	h, err := s.History(time.Time{}, 10)
	if err != nil || len(h) != 2 || h[0].Role != "user" || h[0].Text != "Fix the parser" || h[1].Text != "Fixed the Parser." {
		t.Fatalf("history %+v %v", h, err)
	}
	f, _ := s.Search("parser", 10)
	if len(f) != 2 || f[0].Seq != 0 || f[0].Text != "Fixed the Parser." {
		t.Errorf("search %+v", f)
	}
	for _, bad := range []string{"*", "abc-*", "../sessions/*/x"} {
		if p, _ := (Pi{}).TranscriptPath(bad); p != "" {
			t.Errorf("%q was taken as an id: %s", bad, p)
		}
	}
}

// Over HTTP: the harnesses a machine has, and a session asked of one.
func TestHarnessesOverHTTP(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Register(Pi{}, fakePiBin(t))
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)

	r, err := http.Get(srv.URL + "/v1/harnesses")
	if err != nil {
		t.Fatal(err)
	}
	var hs struct{ Harnesses []HarnessInfo }
	json.NewDecoder(r.Body).Decode(&hs)
	r.Body.Close()
	if len(hs.Harnesses) != 2 || hs.Harnesses[0].Name != "claude" || !hs.Harnesses[0].Caps.Approve ||
		hs.Harnesses[1].Name != "pi" || hs.Harnesses[1].Caps.Approve {
		t.Errorf("harnesses %+v", hs.Harnesses)
	}
	post := func(body string) (int, string) {
		r, err := http.Post(srv.URL+"/v1/sessions", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b := new(bytes.Buffer)
		b.ReadFrom(r.Body)
		return r.StatusCode, b.String()
	}
	dir := t.TempDir()
	if code, body := post(`{"name":"p","dir":"` + dir + `","harness":"pi"}`); code/100 != 2 || !strings.Contains(body, `"harness":"pi"`) {
		t.Errorf("a pi session: %d %s", code, body)
	}
	if code, _ := post(`{"name":"q","dir":"` + dir + `","harness":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown harness: %d", code)
	}
	if code, _ := post(`{"name":"r","dir":"` + dir + `","harness":"pi","resume":"x"}`); code != http.StatusBadRequest {
		t.Errorf("taking over for pi: %d", code)
	}
	// Kept and loaded again: still pi.
	m2, err := NewManager(t.Context(), slog.New(slog.DiscardHandler), m.dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := m2.Get("p"); !ok || s.Info().Harness != "pi" {
		t.Errorf("after a restart: %+v", s)
	}
	if s, _ := m2.Get("p"); s.Send("hi", "") == nil || !strings.Contains(s.Send("hi", "").Error(), "no pi") {
		t.Errorf("a pi session on a machine without pi should say so")
	}
}

// A star is kept on the agent, over HTTP, and survives a restart.
func TestAStarIsKept(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Create("proj", t.TempDir())
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)
	r, err := http.Post(srv.URL+"/v1/sessions/proj/settings", "application/json", strings.NewReader(`{"starred":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var in Info
	json.NewDecoder(r.Body).Decode(&in)
	r.Body.Close()
	if !in.Starred {
		t.Errorf("not starred: %+v", in)
	}
	m2, _ := NewManager(t.Context(), slog.New(slog.DiscardHandler), state, "claude")
	if s, _ := m2.Get("proj"); !s.Info().Starred || s.Info().AutoApprove {
		t.Errorf("after a restart: %+v", s.Info())
	}
	r, _ = http.Post(srv.URL+"/v1/sessions/proj/settings", "application/json", strings.NewReader(`{"starred":false}`))
	r.Body.Close()
	if s, _ := m.Get("proj"); s.Info().Starred {
		t.Error("still starred")
	}
}

// A removed session leaves no event log behind: the process's last event
// ("stopped") used to be written after removal, recreating it.
func TestARemovedSessionLeavesNoLog(t *testing.T) {
	for i := 0; i < 20; i++ {
		m := newTestManager(t, t.TempDir())
		m.Create("proj", t.TempDir())
		s, _ := m.Get("proj")
		s.Send("hello", "")
		waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
		path := s.eventsPath()
		if err := m.Remove("proj"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("round %d: the log of a removed session is back", i)
		}
	}
}

// Turns pi starts itself — an extension's heartbeat, a chat bridge passing on
// a message — are shown as messages from that source; the session's own, and
// an extension's hidden ones, are not shown twice or at all.
func TestTurnsFromInsidePiAreShown(t *testing.T) {
	_, s := newPiSession(t)
	s.Send("wake", "nothing.home")
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "message" && e.By == "pi" })
	var got []string
	backlog, ch := s.Since(0)
	s.Unsubscribe(ch)
	for _, e := range backlog {
		if e.Kind == "message" {
			var d struct{ Text string }
			json.Unmarshal(e.Data, &d)
			got = append(got, e.By+": "+d.Text)
		}
	}
	want := []string{"nothing.home: wake", "heartbeat: HEARTBEAT: pick one task", "pi: from telegram: hi Jimmy"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages %q, want %q", got, want)
	}
}

// A session kept running is never stopped as idle, and comes back when its
// process ends.
func TestASessionKeptRunningComesBack(t *testing.T) {
	defer func(d time.Duration) { keepAliveEvery = d }(keepAliveEvery)
	keepAliveEvery = 50 * time.Millisecond
	m := newTestManager(t, t.TempDir())
	m.Register(Pi{}, fakePiBin(t))
	if _, err := m.CreateWith("jimmy", t.TempDir(), "pi"); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("jimmy")
	if err := s.SetKeepRunning(true, "nothing.home"); err != nil {
		t.Fatal(err)
	}
	if !s.Info().Running || !s.Info().KeepRunning {
		t.Fatalf("not running: %+v", s.Info())
	}
	s.mu.Lock()
	first := s.proc
	s.mu.Unlock()
	s.stopIfIdle(time.Now().Add(24*time.Hour), time.Minute)
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	still := s.proc == first
	s.mu.Unlock()
	if !still {
		t.Fatal("stopped as idle")
	}
	first.cmd.Process.Kill()
	<-first.read
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		p := s.proc
		s.mu.Unlock()
		if p != nil && p != first {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not started again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Kept across a restart of the agent.
	m2, err := NewManager(context.Background(), slog.New(slog.DiscardHandler), m.dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if s2, _ := m2.Get("jimmy"); !s2.Info().KeepRunning {
		t.Fatal("keep running was not kept")
	}
}

// A pi conversation kept in another directory's sessions — an agent started
// by a script of its own, with --session-dir — is continued by its file, and
// in the directory it ran in.
func TestAPiConversationFromElsewhereIsContinued(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("FAKE_PI_ELSEWHERE", "1")
	os.MkdirAll(filepath.Join(dir, "sessions", "--home-somewhere-else--"), 0o700)
	file := filepath.Join(dir, "sessions", "--home-somewhere-else--", "2026-09-11T19-44-08-789Z_01a091ff-b255.jsonl")
	os.WriteFile(file, []byte(`{"type":"session","version":3,"id":"01a091ff-b255","timestamp":"2026-09-11T19:44:08.789Z","cwd":"`+work+`"}`+"\n"), 0o600)

	if got := (Pi{}).Args(StartOptions{Resume: "01a091ff-b255"}); got[len(got)-1] != file {
		t.Fatalf("args %q", got)
	}
	m := newTestManager(t, t.TempDir())
	m.Register(Pi{}, fakePiBin(t))
	in, err := m.AdoptWith("jimmy", "", "01a091ff-b255", "pi")
	if err != nil {
		t.Fatal(err)
	}
	if in.Dir != work || in.Harness != "pi" {
		t.Fatalf("adopted as %+v", in)
	}
	s, _ := m.Get("jimmy")
	s.Send("hello", "")
	init := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "system/init" })
	if !strings.Contains(string(init.Data), `"session_id":"01a091ff-b255"`) {
		t.Fatalf("not that conversation: %s", init.Data)
	}
	waitFor(t, s, 0, isResult)
}
