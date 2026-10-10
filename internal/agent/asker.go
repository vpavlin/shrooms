package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// The asker of a task is told when the task needs its answer, and of the
// result when it did not wait for it: a note sent into the asking session, as
// a turn, from the worker's machine. Before, an asker learned only by asking
// again, and tasks sat blocked on agents that never looked (2026-10-10).

// noteAsker records where the asker of a new task is, when it is an agent's
// session: the address it called from and the session it claims
// (shrooms/from, "machine/session"). A person asking from an app has no
// session, and is not sent notes.
func (m *Manager) noteAsker(t Task, r *http.Request, md map[string]any) Task {
	from, _ := md["shrooms/from"].(string)
	_, session, ok := strings.Cut(from, "/")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if !ok || session == "" || err != nil {
		return t
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return t
	}
	got, err := m.tasks.change(t.ID, func(t *Task) error {
		if t.AskerAddr == "" {
			t.AskerAddr, t.AskerSession = a.Unmap().String(), session
		}
		return nil
	})
	if err != nil {
		return t
	}
	return got
}

// await marks a task as one its asker is waiting on, until release.
func (m *Manager) await(id string) (release func()) {
	n, _ := m.waiting.LoadOrStore(id, new(atomic.Int32))
	c := n.(*atomic.Int32)
	c.Add(1)
	return func() { c.Add(-1) }
}

// awaited is whether an asker is waiting on the task right now.
func (m *Manager) awaited(id string) bool {
	n, ok := m.waiting.Load(id)
	return ok && n.(*atomic.Int32).Load() > 0
}

// askerNote is what the asker reads about its task.
func askerNote(t Task, machine string) string {
	ref := t.ID
	if machine != "" {
		ref = machine + "/" + t.ID
	}
	if t.State == taskInputRequired {
		return fmt.Sprintf("[shrooms task %s needs your answer]\n%s\n\n"+
			"[You asked for this task. Answer it with the shrooms tool ask_agent, task %q, and what it needs; "+
			"if you no longer need it, say that the same way.]", ref, t.Summary, ref)
	}
	return fmt.Sprintf("[shrooms task %s %s]\n%s\n\n"+
		"[When you have what you needed, close it with the shrooms tool task_ack %q.]", ref, stateWord(t.State), t.Summary, ref)
}

// tellAsker sends the asker its note. An error when the task has no agent
// asker, or its machine did not take the note.
func (m *Manager) tellAsker(t Task) error {
	if t.AskerAddr == "" || t.AskerSession == "" {
		return fmt.Errorf("task %s was not asked by an agent's session", t.ID)
	}
	a, err := netip.ParseAddr(t.AskerAddr)
	if err != nil {
		return err
	}
	machine := ""
	if ms, err := m.machines(); err == nil && len(ms) > 0 {
		machine = ms[0].Name
	}
	body, _ := json.Marshal(map[string]string{
		"text": askerNote(t, machine),
		// One note per change of the task; a nudge is a new one.
		"id": fmt.Sprintf("tasknote-%s-%s-%d", t.ID, stateWord(t.State), time.Now().UnixNano()),
	})
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(m.agentURL(a)+"/v1/sessions/"+t.AskerSession+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the asker's agent answered %s", resp.Status)
	}
	return nil
}
