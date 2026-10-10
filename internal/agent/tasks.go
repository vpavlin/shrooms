package agent

// Tasks: work asked of a session over A2A (docs/a2a-tasks.md, ADR-042). A
// task is not a turn. It is open from the message that asks for it until the
// worker says it is done or blocked (task_update), it is cancelled, or it
// expires; a turn can end with the work half done, and only the worker knows
// which. Tasks are kept on disk, on the worker's machine, where the session
// that does them is: a watchdog there (supervise.go) reminds a session that
// went quiet with a task open, and a queue starts the next task when the
// session is free, instead of turning the asker away.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Task is one piece of work asked of a session.
type Task struct {
	ID        string `json:"id"` // session:messageId
	Session   string `json:"session"`
	MessageID string `json:"message_id"`
	From      string `json:"from"` // the device the mesh names, and the session it claims
	Request   string `json:"request"`
	// Title is the asker's few words for what it asks (shrooms/title), for
	// lists of tasks; empty when it gave none.
	Title string `json:"title,omitempty"`
	// Where the asker is, when it is an agent's session: its machine's mesh
	// address and the session it said it is. Notes about the task go there
	// (asker.go); empty for a person asking from an app.
	AskerAddr    string    `json:"asker_addr,omitempty"`
	AskerSession string    `json:"asker_session,omitempty"`
	Refs         []string  `json:"refs,omitempty"` // tasks this one follows on from (A2A referenceTaskIds)
	Created      time.Time `json:"created"`
	Started      time.Time `json:"started,omitempty"` // sent to the session; zero while queued
	Updated      time.Time `json:"updated"`
	State        string    `json:"state"`             // an A2A task state
	Summary      string    `json:"summary,omitempty"` // the worker's: what was done, or what it needs
	// More messages from the asker for this task (answers to "blocked"),
	// waiting for the session to be free.
	FollowUps []string `json:"follow_ups,omitempty"`

	// Supervision: the shrooms extension's fields.
	Acked     bool      `json:"acked,omitempty"`
	AckedAt   time.Time `json:"acked_at,omitempty"`
	Nudges    int       `json:"nudges,omitempty"`
	LastNudge time.Time `json:"last_nudge,omitempty"`
	Stalled   bool      `json:"stalled,omitempty"`
	Paused    time.Time `json:"paused,omitempty"` // the owner stopped the session; no reminders until a new message
	Expired   bool      `json:"expired,omitempty"`
}

// open is not finished: queued, working, or waiting on its asker.
func (t *Task) open() bool { return !terminal(t.State) }

// taskStore is a machine's tasks, kept in tasks.json.
type taskStore struct {
	mu    sync.Mutex
	path  string
	tasks map[string]*Task
}

func openTaskStore(dir string) (*taskStore, error) {
	st := &taskStore{path: filepath.Join(dir, "tasks.json"), tasks: map[string]*Task{}}
	b, err := os.ReadFile(st.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var ts []*Task
		if err := json.Unmarshal(b, &ts); err != nil {
			return nil, fmt.Errorf("%s: %w", st.path, err)
		}
		for _, t := range ts {
			st.tasks[t.ID] = t
		}
	}
	return st, nil
}

// keepClosed is how long a finished task stays on record.
const keepClosed = 7 * 24 * time.Hour

// save writes the store, dropping tasks closed long ago. Called with st.mu held.
func (st *taskStore) save() error {
	var ts []*Task
	for id, t := range st.tasks {
		if !t.open() && time.Since(t.Updated) > keepClosed {
			delete(st.tasks, id)
			continue
		}
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Created.Before(ts[j].Created) })
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// get is a copy of a task.
func (st *taskStore) get(id string) (Task, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// change applies f to a task and keeps the result.
func (st *taskStore) change(id string, f func(*Task) error) (Task, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("no task %s", id)
	}
	if err := f(t); err != nil {
		return *t, err
	}
	t.Updated = time.Now()
	return *t, st.save()
}

// list is the tasks of a session ("" for all), oldest first.
func (st *taskStore) list(session string) []Task {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []Task
	for _, t := range st.tasks {
		if session == "" || t.Session == session {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// taskHeader is what the session reads before the request: which task it is
// and how to finish it. Written by the agent, not a model.
func taskHeader(t Task) string {
	return fmt.Sprintf("[shrooms task %s from %s]\n%s\n\n"+
		"[This is a task: it stays open until you finish it. When it is done, call the shrooms tool task_update "+
		"with task %q, state \"done\" and a short summary of the result. If you need an answer from the asker to go on, "+
		"call it with state \"blocked\" and your question; if you cannot or will not do it, state \"failed\" and why. "+
		"If you stop before any of these, you will be reminded.]",
		t.ID, t.From, t.Request, t.ID)
}

// Submit records a task asked of session s, and starts it if the session is
// free, else queues it. A messageId seen before is the same task.
func (m *Manager) Submit(s *Session, messageID, from, text, title string, refs []string) (Task, error) {
	id := s.Name() + ":" + messageID
	if t, ok := m.tasks.get(id); ok {
		return t, nil
	}
	now := time.Now()
	t := &Task{ID: id, Session: s.Name(), MessageID: messageID, From: from, Request: text, Title: title, Refs: refs,
		Created: now, Updated: now, State: taskSubmitted}
	m.tasks.mu.Lock()
	m.tasks.tasks[id] = t
	err := m.tasks.save()
	m.tasks.mu.Unlock()
	if err != nil {
		return *t, err
	}
	m.dispatch(s)
	got, _ := m.tasks.get(id)
	return got, nil
}

// FollowUp is more from the asker on an open task — the answer to "blocked",
// say: sent when the session is free, and the task works again.
func (m *Manager) FollowUp(s *Session, id, text string) (Task, error) {
	t, err := m.tasks.change(id, func(t *Task) error {
		if !t.open() {
			return fmt.Errorf("task %s is %s", id, stateWord(t.State))
		}
		t.FollowUps = append(t.FollowUps, text)
		t.Paused = time.Time{}
		return nil
	})
	if err != nil {
		return t, err
	}
	m.dispatch(s)
	got, _ := m.tasks.get(id)
	return got, nil
}

// dispatch sends the session what waits for it, when it is free: the
// follow-ups of the task it has, else the oldest queued task. Free is idle,
// with no prompt waiting, and no other task being worked on.
func (m *Manager) dispatch(s *Session) {
	in := s.Info()
	if in.State != Idle || in.Pending > 0 {
		return
	}
	ts := m.tasks.list(s.Name())
	var next *Task
	followUp := false
	for i := range ts {
		t := &ts[i]
		if t.open() && len(t.FollowUps) > 0 && !t.Started.IsZero() {
			next, followUp = t, true
			break
		}
	}
	if next == nil {
		for i := range ts {
			if ts[i].State == taskWorking && !ts[i].Stalled {
				return // the session has a task; the next waits for it
			}
		}
		for i := range ts {
			if ts[i].State == taskSubmitted {
				next = &ts[i]
				break
			}
		}
	}
	if next == nil {
		return
	}
	var text, mid string
	if followUp {
		text = fmt.Sprintf("[shrooms task %s — more from %s]\n%s", next.ID, next.From, strings.Join(next.FollowUps, "\n\n"))
		mid = next.MessageID + fmt.Sprintf("-f%d", time.Now().UnixNano())
	} else {
		text, mid = taskHeader(*next), next.MessageID
	}
	_, err := s.SendID(text, next.From, mid)
	m.tasks.change(next.ID, func(t *Task) error {
		if err != nil {
			t.State, t.Summary = taskFailed, "could not be sent to the session: "+err.Error()
			return nil
		}
		if t.Started.IsZero() {
			t.Started = time.Now()
		}
		t.State, t.FollowUps, t.Stalled = taskWorking, nil, false
		t.Nudges, t.LastNudge = 0, time.Time{}
		return nil
	})
}

// Update is the worker's word on a task: done, blocked (it needs something
// from the asker), or failed.
func (m *Manager) Update(id, state, summary string) (Task, error) {
	var to string
	switch state {
	case "done", "completed":
		to = taskCompleted
	case "blocked", "input_required", "input-required":
		to = taskInputRequired
	case "failed":
		to = taskFailed
	default:
		return Task{}, fmt.Errorf("state %q: done, blocked or failed", state)
	}
	t, err := m.tasks.change(id, func(t *Task) error {
		if !t.open() {
			return fmt.Errorf("task %s is %s already", id, stateWord(t.State))
		}
		t.State, t.Summary, t.Stalled = to, summary, false
		return nil
	})
	if err == nil {
		if s, ok := m.Get(t.Session); ok {
			s.mu.Lock()
			s.record("task", "", map[string]string{"id": t.ID, "state": stateWord(t.State), "summary": t.Summary})
			s.mu.Unlock()
			m.dispatch(s)
		}
		// The asker is told: always when it has to answer, and of the result
		// unless it is waiting for it right now (asker.go). A sealed
		// session's results go with it first, out of its outbox (sealedoutbox.go).
		go func() {
			extra := ""
			if to != taskInputRequired && m.sealed(t.Session) {
				if s, ok := m.Get(t.Session); ok {
					extra = m.deliverOutbox(s, t)
				}
			}
			if to == taskInputRequired || extra != "" || !m.awaited(t.ID) {
				m.tellAskerWith(t, extra)
			}
		}()
	}
	return t, err
}

// Cancel ends a task: a queued one never starts; one being worked on is
// interrupted.
func (m *Manager) Cancel(id, by string) (Task, error) {
	var started bool
	t, err := m.tasks.change(id, func(t *Task) error {
		if !t.open() {
			return fmt.Errorf("task %s is %s already", id, stateWord(t.State))
		}
		started = t.State == taskWorking
		t.State, t.Summary = taskCanceled, "cancelled by "+by
		return nil
	})
	if err == nil && started {
		if s, ok := m.Get(t.Session); ok && s.Info().State != Idle {
			s.Interrupt(by)
		}
	}
	return t, err
}

// Ack is the asker's: it has seen the result.
func (m *Manager) Ack(id string) (Task, error) {
	return m.tasks.change(id, func(t *Task) error {
		t.Acked, t.AckedAt = true, time.Now()
		return nil
	})
}

// Tasks is a session's tasks ("" for every session's).
func (m *Manager) Tasks(session string) []Task { return m.tasks.list(session) }

// pauseTasks stops reminders for a session's tasks: its owner interrupted it.
func (m *Manager) pauseTasks(session string) {
	for _, t := range m.tasks.list(session) {
		if t.State == taskWorking {
			m.tasks.change(t.ID, func(t *Task) error { t.Paused = time.Now(); return nil })
		}
	}
}

// renameTasks keeps a renamed session's tasks with it.
func (m *Manager) renameTasks(from, to string) {
	m.tasks.mu.Lock()
	defer m.tasks.mu.Unlock()
	for id, t := range m.tasks.tasks {
		if t.Session == from {
			delete(m.tasks.tasks, id)
			t.Session, t.ID = to, to+":"+t.MessageID
			m.tasks.tasks[t.ID] = t
		}
	}
	m.tasks.save()
}

func stateWord(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "TASK_STATE_")), "_", "-")
}
