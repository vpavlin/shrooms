package agent

// A fresh conversation for a session (ADR-072): the same name, cage, model,
// settings and role, on a new conversation, and for a caged session a cleared
// working directory with its tools kept.
//
// Asked for by the owner (the apps' "new conversation", POST
// /v1/sessions/{name}/new), or by another agent on a new task marked
// shrooms/fresh, and then only on a session whose owner allowed it
// (fresh_by_agents) and only while it is idle with no other task open. An
// auditor carrying one audit's clones, findings and half a million tokens of
// context into the next was the case (2026-10-27).

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// archiveKeep is how long a cleared working directory is kept, in case
// something in it was wanted after all.
const archiveKeep = 30 * 24 * time.Hour

// keptAlways stay in a working directory however it is cleared: a session's
// standing instructions, and the list of what else to keep.
var keptAlways = map[string]bool{".keep": true, "AGENTS.md": true, "CLAUDE.md": true}

// ErrNotFresh is a fresh start refused, with why.
var ErrNotFresh = errors.New("not started fresh")

// FreshByAgents is whether other agents may start this session fresh.
func (s *Session) FreshByAgents() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freshByAgents
}

// SetFreshByAgents lets other agents start this session fresh, or not. Off
// by default: another agent can never wipe a session its owner did not open
// to it.
func (s *Session) SetFreshByAgents(on bool, by string) error {
	s.mu.Lock()
	s.freshByAgents = on
	s.record("setting", by, map[string]bool{"fresh_by_agents": on})
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}

// NewConversation starts s on a fresh conversation: its process stopped and
// its conversation id forgotten, so the next message begins a new one; the
// old one stays in the harness's transcripts and the session's events. A
// caged session's working directory is cleared too (clearWorkdir); an
// uncaged one's is not, since it may be the owner's home. Refused unless the
// session is idle with nothing waiting and no task open.
func (m *Manager) NewConversation(s *Session, by string) (archived string, err error) {
	if in := s.Info(); in.State != Idle || in.Pending > 0 {
		return "", fmt.Errorf("%w: %s is %s; start it fresh once it is idle", ErrNotFresh, s.Name(), in.State)
	}
	if n := m.openTasks(s.Name()); n > 0 {
		return "", fmt.Errorf("%w: %s has %d task(s) open; finish or cancel them first", ErrNotFresh, s.Name(), n)
	}
	s.stop()
	s.mu.Lock()
	caged := s.cage != nil
	dir := s.dir
	s.mu.Unlock()
	if caged {
		if archived, err = m.clearWorkdir(s.Name(), dir, time.Now()); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	s.convID = ""
	data := map[string]string{}
	if archived != "" {
		data["archived"] = archived
	}
	s.record("new_conversation", by, data)
	s.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return archived, m.save()
}

// openTasks counts the tasks still open on a session.
func (m *Manager) openTasks(session string) int {
	m.tasks.mu.Lock()
	defer m.tasks.mu.Unlock()
	n := 0
	for _, t := range m.tasks.tasks {
		if t.Session == session && t.open() {
			n++
		}
	}
	return n
}

// archiveRoot is where cleared working directories are kept, beside the
// agent's state and outside every cage.
func (m *Manager) archiveRoot() string { return filepath.Join(m.dir, "archive") }

// clearWorkdir moves what is in dir to the archive, but for what keptAlways
// names, what .keep lists (a name per line), and dot-entries, where tools
// and their settings live (an audit lab's keys, .venv, .cache); a clone of
// the last audit's repository is not one. The cage itself, with whatever was
// installed in it, is not touched. Returns where it went, "" if nothing did.
func (m *Manager) clearWorkdir(session, dir string, now time.Time) (string, error) {
	m.pruneArchive(now)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	keep := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, ".keep")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "/")); l != "" && !strings.HasPrefix(l, "#") {
				keep[l] = true
			}
		}
	}
	var move []string
	for _, e := range ents {
		n := e.Name()
		if keptAlways[n] || keep[n] || strings.HasPrefix(n, ".") {
			continue
		}
		move = append(move, n)
	}
	if len(move) == 0 {
		return "", nil
	}
	to := filepath.Join(m.archiveRoot(), session, now.UTC().Format("20060102-150405"))
	if err := os.MkdirAll(to, 0o700); err != nil {
		return "", err
	}
	for _, n := range move {
		if err := moveEntry(filepath.Join(dir, n), filepath.Join(to, n)); err != nil {
			return to, fmt.Errorf("clearing %s: %w", n, err)
		}
	}
	return to, nil
}

// moveEntry renames src to dst. What a cage made belongs to one of the
// user's subordinate ids (525287 on proteus, 2026-10-27), and moving a
// directory to another parent needs write permission on the directory
// itself, which the user does not have; inside podman's user namespace it
// does, so a refused move is done there.
func moveEntry(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	podman, perr := exec.LookPath("podman")
	if perr != nil {
		return err
	}
	if out, uerr := exec.Command(podman, "unshare", "mv", "--", src, dst).CombinedOutput(); uerr != nil {
		return fmt.Errorf("%w (and in podman's namespace: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pruneArchive removes what was cleared more than archiveKeep ago.
func (m *Manager) pruneArchive(now time.Time) {
	sessions, err := os.ReadDir(m.archiveRoot())
	if err != nil {
		return
	}
	for _, s := range sessions {
		base := filepath.Join(m.archiveRoot(), s.Name())
		runs, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].Name() < runs[j].Name() })
		left := len(runs)
		for _, r := range runs {
			at, err := time.Parse("20060102-150405", r.Name())
			if err != nil || now.Sub(at) < archiveKeep {
				continue
			}
			if os.RemoveAll(filepath.Join(base, r.Name())) == nil {
				left--
			}
		}
		if left == 0 {
			os.Remove(base)
		}
	}
}
