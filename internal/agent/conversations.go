package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Conversation is a Claude Code conversation on this machine, from its
// transcript — one started in a terminal, say, that a session can take over
// (docs/agents.md). Resuming it keeps writing to the same transcript, so taking
// it over is continuing it, not copying it.
type Conversation struct {
	ID       string    `json:"id"`
	Dir      string    `json:"dir"`
	Modified time.Time `json:"modified"`
	Size     int64     `json:"size"`
	// The last thing typed and the last thing answered, to recognise it by.
	LastUser      string `json:"last_user,omitempty"`
	LastAssistant string `json:"last_assistant,omitempty"`
	// AdoptedBy is the session already continuing it, if one is.
	AdoptedBy string `json:"adopted_by,omitempty"`
	// Terminals are claude processes open in the same directory. A terminal
	// does not keep its transcript open, so which conversation it holds cannot
	// be known — only that one in this directory may be this one, and that
	// two writers on one conversation split it (they cannot see each other's
	// turns).
	Terminals []Terminal `json:"terminals,omitempty"`
}

// Terminal is a claude process somebody runs by hand, as `cl` does in tmux.
type Terminal struct {
	PID  int    `json:"pid"`
	Dir  string `json:"dir"`
	Tmux string `json:"tmux,omitempty"`
	Args string `json:"args"`
}

// previewTail is how much of each transcript's end is read for the list: the
// last exchange, not the history — thirty conversations of a hundred
// megabytes each must not be read to draw a list.
const previewTail = 512 << 10

// Conversations lists this machine's Claude Code conversations, newest first.
func (m *Manager) Conversations(limit int) ([]Conversation, error) {
	base, err := claudeDir()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(base, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	type file struct {
		path string
		info os.FileInfo
	}
	var fs []file
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Size() > 0 {
			fs = append(fs, file{f, fi})
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].info.ModTime().After(fs[j].info.ModTime()) })

	adopted := map[string]string{}
	m.mu.Lock()
	for _, s := range m.sessions {
		s.mu.Lock()
		// Conversations listed here are Claude Code's; another harness's
		// id could only collide by chance.
		if s.convID != "" && s.harness.Name() == "claude" {
			adopted[s.convID] = s.Name()
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()
	terms := Terminals()

	out := make([]Conversation, 0, len(fs))
	for _, f := range fs {
		if limit > 0 && len(out) >= limit {
			break
		}
		c := Conversation{
			ID:       strings.TrimSuffix(filepath.Base(f.path), ".jsonl"),
			Modified: f.info.ModTime(),
			Size:     f.info.Size(),
		}
		c.AdoptedBy = adopted[c.ID]
		if fh, err := os.Open(f.path); err == nil {
			c.Dir = transcriptDir(fh)
			if said, err := readTail(fh, previewTail, time.Time{}, 0, parseTranscriptLine); err == nil {
				for i := len(said) - 1; i >= 0 && (c.LastUser == "" || c.LastAssistant == ""); i-- {
					t := clip(said[i].Text, 160)
					if said[i].Role == "user" && c.LastUser == "" {
						c.LastUser = t
					} else if said[i].Role == "assistant" && c.LastAssistant == "" {
						c.LastAssistant = t
					}
				}
			}
			fh.Close()
		}
		// Not one that ran somewhere else: a ~/.claude copied from another
		// machine (to bring its login along) brings that machine's
		// transcripts too, in directories this one does not have — the
		// Duet listed atlas's /home/vpavlin/duet, and continuing it tried to
		// make /home/vpavlin (2026-10-06).
		if c.Dir != "" && !isDir(c.Dir) {
			continue
		}
		for _, t := range terms {
			if c.Dir != "" && t.Dir == c.Dir {
				c.Terminals = append(c.Terminals, t)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// transcriptDir is the working directory a conversation ran in: recorded on
// its message lines, not its first ones, so the head is scanned for it.
func transcriptDir(f *os.File) string {
	f.Seek(0, io.SeekStart)
	sc := bufio.NewScanner(io.LimitReader(f, 2<<20))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"cwd"`)) {
			continue
		}
		var e struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &e) == nil && e.Cwd != "" {
			return e.Cwd
		}
	}
	return ""
}

func claudeDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// Terminals finds the claude processes this user runs by hand: named claude,
// and not one of an agent's own (those run with -p and stream-json).
func Terminals() []Terminal {
	tmux := tmuxPanes()
	var out []Terminal
	procs, _ := filepath.Glob("/proc/[0-9]*")
	uid := os.Getuid()
	for _, p := range procs {
		pid, err := strconv.Atoi(filepath.Base(p))
		if err != nil {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(p, &st) != nil || int(st.Uid) != uid {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if !isTerminalClaude(args) {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(p, "cwd"))
		if err != nil {
			continue
		}
		out = append(out, Terminal{PID: pid, Dir: cwd, Tmux: tmux[pid], Args: strings.Join(args, " ")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// isTerminalClaude says whether a command line is Claude Code run by a person:
// the agent's own processes are -p with stream-json, and are not.
func isTerminalClaude(args []string) bool {
	if len(args) == 0 || filepath.Base(args[0]) != "claude" {
		return false
	}
	for _, a := range args[1:] {
		if a == "-p" || a == "--print" || a == "stream-json" {
			return false
		}
	}
	return true
}

// tmuxPanes maps a pane's process to its tmux session: what `cl` calls it.
func tmuxPanes() map[int]string {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_pid} #{session_name}").Output()
	if err != nil {
		return nil
	}
	return parsePanes(string(out))
}

func parsePanes(s string) map[int]string {
	m := map[int]string{}
	for _, line := range strings.Split(s, "\n") {
		pid, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(pid); err == nil {
			m[n] = name
		}
	}
	return m
}

// StopTerminal ends a terminal's claude, so a session can take its
// conversation over without the two writing over each other. Only a process
// Terminals reports: this is not a way to signal anything else on the machine.
func StopTerminal(pid int) error {
	for _, t := range Terminals() {
		if t.PID != pid {
			continue
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return p.Signal(syscall.SIGTERM)
	}
	return fmt.Errorf("no terminal claude with pid %d", pid)
}

// Adopt creates a session continuing an existing conversation. With no
// directory given, the conversation's own is used — resuming it elsewhere
// would hand it a different working tree.
func (m *Manager) Adopt(name, dir, conversation string) (Info, error) {
	return m.AdoptWith(name, dir, conversation, "claude")
}

// AdoptWith is Adopt for a conversation of the named harness: Claude Code's,
// or pi's — one running in a terminal of its own until now.
func (m *Manager) AdoptWith(name, dir, conversation, harness string) (Info, error) {
	return m.AdoptCaged(name, dir, conversation, harness, nil)
}

// AdoptCaged continues a conversation in a session that runs in a cage when
// cage is not nil (cage.go): the transcript is where the cage sees it.
func (m *Manager) AdoptCaged(name, dir, conversation, harness string, cage *Cage) (Info, error) {
	if harness == "" {
		harness = "claude"
	}
	if !validName.MatchString(conversation) {
		return Info{}, fmt.Errorf("not a conversation id: %q", conversation)
	}
	m.mu.Lock()
	h, ok := m.harnesses[harness]
	m.mu.Unlock()
	t, kept := h.(Transcripts)
	if !ok || !kept {
		return Info{}, fmt.Errorf("continuing a conversation is not something %s sessions can do", harness)
	}
	file, err := t.TranscriptPath(conversation)
	if err != nil {
		return Info{}, err
	}
	if file == "" {
		return Info{}, fmt.Errorf("no conversation %s on this machine", conversation)
	}
	if dir == "" {
		f, err := os.Open(file)
		if err != nil {
			return Info{}, err
		}
		dir = transcriptDir(f)
		f.Close()
		if dir == "" {
			return Info{}, errors.New("the conversation does not say which directory it ran in")
		}
	}
	// Continued where it ran, which must be here already: a new session
	// makes a missing directory, but a conversation whose directory is not
	// on this machine ran on another one and was copied here with ~/.claude.
	if !isDir(dir) {
		return Info{}, fmt.Errorf("it ran in %s, which is not on this machine: its transcript was copied here from another one", dir)
	}
	m.mu.Lock()
	for _, s := range m.sessions {
		s.mu.Lock()
		taken := s.convID == conversation
		s.mu.Unlock()
		if taken {
			m.mu.Unlock()
			return Info{}, fmt.Errorf("session %q already continues it", s.Name())
		}
	}
	m.mu.Unlock()
	if _, err := m.CreateCaged(name, dir, harness, cage); err != nil {
		return Info{}, err
	}
	s, _ := m.Get(name)
	s.mu.Lock()
	s.convID = conversation
	s.mu.Unlock()
	m.mu.Lock()
	err = m.save()
	m.mu.Unlock()
	return s.Info(), err
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
