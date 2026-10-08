package agent

// The task watchdog (docs/a2a-tasks.md): on the worker's machine, where the
// session is. It costs nothing while no task is open — no heartbeat, no
// model — and reminds a session only when it went quiet with a task it has
// not finished: idle, no prompt waiting, no turn for a while. The reminder is
// written here, not by a model, carries what was asked and the worker's last
// words, and backs off; after the last one the task is stalled and the owner
// is told. It also starts queued tasks when a session comes free, and
// expires tasks nobody finished in a day.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// superviseEvery is how often the watchdog looks.
	superviseEvery = 30 * time.Second
	// nudgeAfter is how long a session sits quiet before the first reminder:
	// longer for Claude Code, which ends a turn while a background command
	// runs on and comes back by itself when it is done.
	nudgeAfter = map[string]time.Duration{"claude": 5 * time.Minute, "pi": 2 * time.Minute}
	// nudgeLadder is the wait before each next reminder; its length is how
	// many there are before the task is stalled.
	nudgeLadder = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	// nudgesPerHour bounds a session's reminders, however many tasks it has.
	nudgesPerHour = 6
	// taskTTL is how long a task may stay open.
	taskTTL = 24 * time.Hour
)

// nudgeRate is when each session was last reminded, for nudgesPerHour.
type nudgeRate struct {
	mu   sync.Mutex
	sent map[string][]time.Time
}

func (r *nudgeRate) allow(session string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string][]time.Time{}
	}
	kept := r.sent[session][:0]
	for _, t := range r.sent[session] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	r.sent[session] = kept
	return len(kept) < nudgesPerHour
}

func (r *nudgeRate) note(session string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent[session] = append(r.sent[session], now)
}

// supervise runs the watchdog until the manager's context ends.
func (m *Manager) supervise(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-t.C:
			m.superviseOnce(now)
		}
	}
}

// superviseOnce is one look at every session with tasks.
func (m *Manager) superviseOnce(now time.Time) {
	bySession := map[string][]Task{}
	for _, t := range m.tasks.list("") {
		if t.open() {
			bySession[t.Session] = append(bySession[t.Session], t)
		}
	}
	for name, ts := range bySession {
		s, ok := m.Get(name)
		if !ok {
			continue
		}
		m.expire(s, ts, now)
		m.dispatch(s)
		m.remind(s, now)
	}
}

// expire closes the tasks open longer than taskTTL.
func (m *Manager) expire(s *Session, ts []Task, now time.Time) {
	for _, t := range ts {
		if now.Sub(t.Created) < taskTTL {
			continue
		}
		got, err := m.tasks.change(t.ID, func(t *Task) error {
			t.State, t.Expired = taskCanceled, true
			t.Summary = fmt.Sprintf("expired: open for %s without being finished", taskTTL)
			return nil
		})
		if err == nil {
			s.mu.Lock()
			s.record("task", "", map[string]string{"id": got.ID, "state": "expired", "summary": got.Summary})
			s.mu.Unlock()
		}
	}
}

// remind sends a session one reminder for its stalled tasks, when they are
// due, or marks them stalled once the reminders are spent.
func (m *Manager) remind(s *Session, now time.Time) {
	in := s.Info()
	if in.State != Idle || in.Pending > 0 {
		return
	}
	last := s.lastActivity()
	quiet := now.Sub(last)
	after := nudgeAfter[in.Harness]
	if after == 0 {
		after = 5 * time.Minute
	}
	if quiet < after || in.Harness == "claude" && s.backgroundWork() {
		return
	}
	var due []Task
	for _, t := range m.tasks.list(s.Name()) {
		if t.State != taskWorking || t.Stalled || t.Started.IsZero() {
			continue
		}
		if !t.Paused.IsZero() {
			// The owner stopped it: quiet until anyone writes to it again.
			if !s.messageSince(t.Paused) {
				continue
			}
			m.tasks.change(t.ID, func(t *Task) error { t.Paused = time.Time{}; return nil })
		}
		if t.Nudges > 0 && now.Sub(t.LastNudge) < nudgeLadder[min(t.Nudges, len(nudgeLadder))-1] {
			continue
		}
		if t.Nudges >= len(nudgeLadder) {
			got, err := m.tasks.change(t.ID, func(t *Task) error { t.Stalled = true; return nil })
			if err == nil {
				s.mu.Lock()
				s.record("task", "", map[string]string{"id": got.ID, "state": "stalled",
					"summary": fmt.Sprintf("no progress after %d reminders; the owner and %s are told by its status", got.Nudges, got.From)})
				s.mu.Unlock()
			}
			continue
		}
		due = append(due, t)
	}
	if len(due) == 0 || !m.nudges.allow(s.Name(), now) {
		return
	}
	text := nudgeText(due, quiet, s.lastWords(due[0].Started))
	ids := make([]string, len(due))
	for i, t := range due {
		ids[i] = t.ID
	}
	s.mu.Lock()
	err := s.send(text, "shrooms", map[string]any{"outside": true, "nudge": ids})
	s.mu.Unlock()
	if err != nil {
		m.log.Warn("could not remind a session of its tasks", "session", s.Name(), "err", err)
		return
	}
	m.nudges.note(s.Name(), now)
	for _, id := range ids {
		m.tasks.change(id, func(t *Task) error { t.Nudges++; t.LastNudge = now; return nil })
	}
}

// nudgeText is the reminder: written here, the same every time, with what
// was asked (the conversation may have been compacted since) and the last
// thing the session said, so it resumes rather than starts over.
func nudgeText(due []Task, quiet time.Duration, lastWords string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[shrooms: a reminder, not a person] This session has been idle %s with ", quiet.Round(time.Minute))
	if len(due) == 1 {
		b.WriteString("a task open:\n")
	} else {
		fmt.Fprintf(&b, "%d tasks open, oldest first:\n", len(due))
	}
	for _, t := range due {
		fmt.Fprintf(&b, "- task %s from %s (reminder %d of %d): %q\n", t.ID, t.From, t.Nudges+1, len(nudgeLadder), trim(t.Request, 400))
	}
	if lastWords != "" {
		fmt.Fprintf(&b, "Last you said: %q\n", trim(lastWords, 400))
	}
	b.WriteString("Continue it. When it is finished, call the shrooms tool task_update with the task and state \"done\" and a summary; " +
		"if you cannot go on, state \"blocked\" and what you need.")
	return b.String()
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// lastActivity is when the session last did or was told anything.
func (s *Session) lastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.events); n > 0 {
		return s.events[n-1].Time
	}
	return time.Time{}
}

// messageSince reports a message to the session after t, other than a
// reminder.
func (s *Session) messageSince(t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0 && s.events[i].Time.After(t); i-- {
		if s.events[i].Kind == "message" && s.events[i].By != "shrooms" {
			return true
		}
	}
	return false
}

// lastWords is the last thing the model said since since.
func (s *Session) lastWords(since time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0 && !s.events[i].Time.Before(since); i-- {
		if t := replyText(s.events[i]); t != "" {
			return t
		}
	}
	return ""
}

// replyText is the model's text in an event, if it is a reply.
func replyText(e Event) string {
	if e.Kind != "claude" {
		return ""
	}
	var d struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(e.Data, &d) != nil || d.Type != "assistant" {
		return ""
	}
	var parts []string
	for _, c := range d.Message.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// backgroundWork reports a command still running under the session's
// process — a Claude Code background shell — which ends the turn but not
// the work. Its MCP servers ("… mcp …") do not count.
func (s *Session) backgroundWork() bool {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil || p.cmd.Process == nil {
		return false
	}
	return busyChildren(p.cmd.Process.Pid)
}

func busyChildren(pid int) bool {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	for _, f := range tasks {
		b, _ := os.ReadFile(f)
		for _, c := range strings.Fields(string(b)) {
			child, err := strconv.Atoi(c)
			if err != nil {
				continue
			}
			cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", child))
			line := strings.ReplaceAll(string(cmd), "\x00", " ")
			if strings.Contains(line, "mcp") {
				continue
			}
			return true
		}
	}
	return false
}
