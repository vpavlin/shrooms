package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A transcript the way Claude Code writes one: header lines without a working
// directory, then messages that carry it.
func writeTranscript(t *testing.T, base, id, cwd, user, answer string, mod time.Time) {
	t.Helper()
	dir := filepath.Join(base, "projects", strings.ReplaceAll(cwd, "/", "-"))
	os.MkdirAll(dir, 0o700)
	lines := []string{
		`{"type":"mode","mode":"default","sessionId":"` + id + `"}`,
		`{"type":"user","cwd":"` + cwd + `","timestamp":"2026-10-03T10:00:00Z","message":{"role":"user","content":"` + user + `"}}`,
		`{"type":"assistant","cwd":"` + cwd + `","timestamp":"2026-10-03T10:01:00Z","message":{"content":[{"type":"text","text":"` + answer + `"}]}}`,
	}
	p := filepath.Join(dir, id+".jsonl")
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	os.Chtimes(p, mod, mod)
}

func TestConversationsAreListedNewestFirstWithWhereTheyRan(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	now := time.Now()
	notes, vpn := t.TempDir(), t.TempDir()
	writeTranscript(t, base, "old-1", notes, "what is this", "notes", now.Add(-48*time.Hour))
	writeTranscript(t, base, "new-2", vpn, "fix the tether", "Fixed by moving ports.", now)
	// Ran on another machine, its transcript copied here with ~/.claude:
	// newest of all, and not listed — nor counted against the limit.
	writeTranscript(t, base, "elsewhere-3", "/home/somebody-else/duet", "from atlas", "yes", now.Add(time.Hour))

	m := newTestManager(t, t.TempDir())
	cs, err := m.Conversations(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != "new-2" || cs[1].ID != "old-1" {
		t.Fatalf("order: %+v", cs)
	}
	if cs[0].Dir != vpn || cs[0].LastUser != "fix the tether" || cs[0].LastAssistant != "Fixed by moving ports." {
		t.Errorf("first: %+v", cs[0])
	}
	if one, _ := m.Conversations(1); len(one) != 1 {
		t.Errorf("limit 1 gave %d", len(one))
	}
}

// Taking one over: a session continuing it, in the directory it ran in, once.
func TestAConversationIsTakenOverOnce(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	work := t.TempDir()
	writeTranscript(t, base, "conv-9", work, "hello", "hi", time.Now())

	m := newTestManager(t, t.TempDir())
	in, err := m.Adopt("taken", "", "conv-9")
	if err != nil {
		t.Fatal(err)
	}
	if in.Dir != work {
		t.Errorf("took it over in %s, it ran in %s", in.Dir, work)
	}
	s, _ := m.Get("taken")
	if s.convID != "conv-9" {
		t.Errorf("claude id %q", s.convID)
	}
	cs, _ := m.Conversations(10)
	if cs[0].AdoptedBy != "taken" {
		t.Errorf("not shown as taken: %+v", cs[0])
	}
	if _, err := m.Adopt("again", "", "conv-9"); err == nil {
		t.Error("the same conversation was taken over twice")
	}
	if _, err := m.Adopt("nope", "", "no-such"); err == nil {
		t.Error("a conversation that does not exist was taken over")
	}
	if _, err := m.Adopt("evil", "", "../../etc/passwd"); err == nil {
		t.Error("a path was accepted as a conversation id")
	}

	// And the session resumes it: its first process is started with --resume.
	s.Send("go on", "")
	init := waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "system/init" })
	var h struct {
		SessionID string `json:"session_id"`
		Resumed   bool   `json:"resumed"`
	}
	json.Unmarshal(init.Data, &h)
	if !h.Resumed || h.SessionID != "conv-9" {
		t.Errorf("did not resume the conversation: %+v", h)
	}
}

func TestTerminalClaudeIsToldFromAnAgentsOwn(t *testing.T) {
	for args, want := range map[string]bool{
		"claude --dangerously-skip-permissions --continue":                           true,
		"/home/x/.local/bin/claude":                                                  true,
		"claude -p --input-format stream-json --output-format stream-json --verbose": false,
		"claude --print hello":                                                       false,
		"node /usr/lib/claude-code/cli.js":                                           false,
		"vim claude":                                                                 false,
	} {
		if got := isTerminalClaude(strings.Fields(args)); got != want {
			t.Errorf("%q: %v, want %v", args, got, want)
		}
	}
	got := parsePanes("23173 cl-logos-vpn\n450204 cl-lez-programs\nbad line\n")
	if got[23173] != "cl-logos-vpn" || got[450204] != "cl-lez-programs" || len(got) != 2 {
		t.Errorf("panes: %v", got)
	}
}

// A real process named claude, in a directory: found, matched to its
// conversation by that directory, and stopped — and nothing else is.
func TestATerminalIsFoundAndStopped(t *testing.T) {
	// This test binary, run as "claude": it sits there (FAKE_TERMINAL). Not
	// a copy of sleep, which is a multi-call binary on some systems and will
	// not run under another name.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "claude")
	b, _ := os.ReadFile(self)
	os.WriteFile(fake, b, 0o755)
	work := t.TempDir()
	cmd := exec.Command(fake, "--continue")
	cmd.Env = append(os.Environ(), "FAKE_TERMINAL=1")
	cmd.Dir = work
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	writeTranscript(t, base, "term-1", work, "hi", "hello", time.Now())
	m := newTestManager(t, t.TempDir())
	cs, _ := m.Conversations(5)
	if len(cs) != 1 || len(cs[0].Terminals) != 1 || cs[0].Terminals[0].PID != cmd.Process.Pid {
		t.Fatalf("the terminal in %s was not matched: %+v", work, cs)
	}

	if err := StopTerminal(os.Getpid()); err == nil {
		t.Fatal("a process that is not a terminal claude was signalled")
	}
	if err := StopTerminal(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal claude was not stopped")
	}
}

func TestTheConversationsAPI(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	work := t.TempDir()
	writeTranscript(t, base, "api-1", work, "q", "a", time.Now())
	m := newTestManager(t, t.TempDir())
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	t.Cleanup(srv.Close)

	r, _ := http.Get(srv.URL + "/v1/conversations")
	var list struct{ Conversations []Conversation }
	json.NewDecoder(r.Body).Decode(&list)
	if len(list.Conversations) != 1 || list.Conversations[0].Dir != work {
		t.Fatalf("list: %+v", list)
	}
	r, _ = http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{"name":"took","resume":"api-1"}`))
	var in Info
	json.NewDecoder(r.Body).Decode(&in)
	if r.StatusCode != http.StatusCreated || in.Dir != work {
		t.Errorf("take over: %s %+v", r.Status, in)
	}
	r, _ = http.Post(srv.URL+fmt.Sprintf("/v1/terminals/%d/stop", os.Getpid()), "", nil)
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("stopping a non-terminal: %s", r.Status)
	}
}

// A conversation that ran on another machine — its transcript came with a
// copied ~/.claude — is not continued here, and nothing is made for it: a
// new session makes a missing directory, continuing one does not.
func TestAConversationFromElsewhereIsNotContinued(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	gone := filepath.Join(t.TempDir(), "not-here", "duet")
	writeTranscript(t, base, "elsewhere-1", gone, "hi", "hello", time.Now())

	m := newTestManager(t, t.TempDir())
	_, err := m.Adopt("took", "", "elsewhere-1")
	if err == nil || !strings.Contains(err.Error(), "not on this machine") {
		t.Fatalf("continued a conversation from elsewhere: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(gone)); !os.IsNotExist(err) {
		t.Fatalf("made its directory: %v", err)
	}
	if _, ok := m.Get("took"); ok {
		t.Fatal("a session was left behind")
	}
}
