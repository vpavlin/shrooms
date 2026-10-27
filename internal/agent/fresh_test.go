package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fresh start clears a caged session's working directory of what one job
// left (a clone, a report) and keeps its tools and standing instructions:
// dot-entries, AGENTS.md, CLAUDE.md, and what .keep names (ADR-072).
func TestAFreshStartClearsTheWorkButKeepsTheTools(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	dir := t.TempDir()
	for _, d := range []string{"repo-from-last-audit", ".lab", "lab", ".venv"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	for _, f := range []string{"report.md", "AGENTS.md"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, ".keep"), []byte("# kept between audits\nlab/\n"), 0o644)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	to, err := m.clearWorkdir("auditor", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(m.archiveRoot(), "auditor", "20261027-120000"); to != want {
		t.Fatalf("archived to %q, want %q", to, want)
	}
	var left, gone []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	ents, _ = os.ReadDir(to)
	for _, e := range ents {
		gone = append(gone, e.Name())
	}
	if strings.Join(left, ",") != ".keep,.lab,.venv,AGENTS.md,lab" || strings.Join(gone, ",") != "repo-from-last-audit,report.md" {
		t.Fatalf("left %v, archived %v", left, gone)
	}
}

// What was cleared is kept 30 days, then removed; a session's archive goes
// with its last run.
func TestClearedWorkIsPrunedAfterAMonth(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	now := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	old := filepath.Join(m.archiveRoot(), "auditor", "20261001-000000")
	recent := filepath.Join(m.archiveRoot(), "auditor", "20261120-000000")
	gone := filepath.Join(m.archiveRoot(), "other", "20261002-000000")
	for _, d := range []string{old, recent, gone} {
		os.MkdirAll(d, 0o700)
	}
	m.pruneArchive(now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a month-old run was kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("a recent run was removed")
	}
	if _, err := os.Stat(filepath.Dir(gone)); !os.IsNotExist(err) {
		t.Error("an emptied session's archive was kept")
	}
}

// The owner's way: the session's conversation is forgotten, so the next
// message starts a new one; its settings stay. Not while a task is open.
func TestTheOwnerStartsASessionFresh(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	s.mu.Lock()
	s.convID = "conv-old"
	s.mu.Unlock()
	s.SetAutoApprove(true, "laptop")
	if _, err := r.m.NewConversation(s, "laptop"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	conv, auto := s.convID, s.autoApprove
	s.mu.Unlock()
	if conv != "" || !auto {
		t.Fatalf("after a fresh start: conversation %q, auto-approve %v", conv, auto)
	}
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "new_conversation" })

	got := call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-open", "a task left open", false))
	if got.Error != nil {
		t.Fatal(got.Error)
	}
	waitTurn(t, s, 0)
	if _, err := r.m.NewConversation(s, "laptop"); err == nil || !strings.Contains(err.Error(), "task(s) open") {
		t.Fatalf("started fresh with a task open: %v", err)
	}
}

// Another agent asks with its task: refused where the owner has not allowed
// it, and while another task is open; honoured otherwise, on a new
// conversation. A task without the mark goes on in the old one.
func TestAnAgentStartsASessionFreshOnlyWhereAllowed(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	fresh := func(id string) map[string]any {
		p := send(id, "audit this", false)
		p["message"].(map[string]any)["metadata"].(map[string]any)["shrooms/fresh"] = true
		return p
	}
	got := call(t, r.remote.URL+"/a2a/proj", "SendMessage", fresh("m-1"))
	if got.Error == nil || got.Error.Code != a2aFreshRefused || !strings.Contains(got.Error.Message, "fresh_by_agents") {
		t.Fatalf("not allowed, yet: %+v", got.Error)
	}
	if _, ok := r.m.tasks.get("proj:m-1"); ok {
		t.Fatal("a refused fresh task was kept")
	}

	s.SetFreshByAgents(true, "laptop")
	s.mu.Lock()
	s.convID = "conv-old"
	s.mu.Unlock()
	got = call(t, r.remote.URL+"/a2a/proj", "SendMessage", fresh("m-2"))
	if got.Error != nil || got.Result.Task.ID != "proj:m-2" {
		t.Fatalf("allowed: %+v %+v", got.Result.Task, got.Error)
	}
	ev := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "new_conversation" })
	if ev.By != "pi5.default (pi5/jimmy)" {
		t.Fatalf("fresh start recorded as by %q", ev.By)
	}
	waitTurn(t, s, ev.Seq)

	// m-2 is still open: another fresh task waits for nothing, it is refused.
	got = call(t, r.remote.URL+"/a2a/proj", "SendMessage", fresh("m-3"))
	if got.Error == nil || got.Error.Code != a2aFreshRefused || !strings.Contains(got.Error.Message, "open") {
		t.Fatalf("fresh with a task open: %+v", got.Error)
	}
}
