package agent

// A2A (Agent2Agent, v1.0.0, a2a-protocol.org) on the agent's own port: each
// session is an A2A agent with its card, and a message to it is a task — one
// turn, from its message to its result (docs/agents-together.md, "On the
// wire: A2A"). The JSON-RPC binding only; the mesh is the authentication, as
// for the rest of the API: the source address names the device that asks.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// A2A task states, as A2A's JSON spells them.
const (
	taskSubmitted     = "TASK_STATE_SUBMITTED"
	taskWorking       = "TASK_STATE_WORKING"
	taskInputRequired = "TASK_STATE_INPUT_REQUIRED"
	taskCompleted     = "TASK_STATE_COMPLETED"
	taskFailed        = "TASK_STATE_FAILED"
	taskCanceled      = "TASK_STATE_CANCELED"
	taskRejected      = "TASK_STATE_REJECTED"
)

func terminal(state string) bool {
	switch state {
	case taskCompleted, taskFailed, taskCanceled, taskRejected:
		return true
	}
	return false
}

// JSON-RPC and A2A error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcNoMethod       = -32601
	rpcInvalidParams  = -32602
	a2aTaskNotFound   = -32001
	a2aNotCancelable  = -32002
	a2aUnsupported    = -32004
	// Ours, in the server range: a sender over its rate (a2aLimiter).
	a2aTooMany = -32050
)

// a2aPart is a Part; only text is taken and given.
type a2aPart struct {
	Text string `json:"text,omitempty"`
}

type a2aMessage struct {
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aStatus struct {
	State     string      `json:"state"`
	Message   *a2aMessage `json:"message,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Name       string    `json:"name,omitempty"`
	Parts      []a2aPart `json:"parts"`
}

type a2aTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    a2aStatus      `json:"status"`
	Artifacts []a2aArtifact  `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// taskFrom works out the task begun by the message with id from a session's
// events, which must start at that message: the turn's text, and where it
// stands — waiting on a prompt, ended by its result or by the process going.
func taskFrom(events []Event, id, contextID string, canceled bool) a2aTask {
	t := a2aTask{ID: id, ContextID: contextID, Status: a2aStatus{State: taskSubmitted}}
	var said []string
	open := map[string]bool{}
	at := time.Time{}
	for i, e := range events {
		at = e.Time
		if i == 0 {
			continue // the message itself
		}
		switch e.Kind {
		case "claude":
			var d struct {
				Type      string `json:"type"`
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Message   struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			json.Unmarshal(e.Data, &d)
			switch d.Type {
			case "assistant":
				for _, c := range d.Message.Content {
					if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
						said = append(said, strings.TrimSpace(c.Text))
					}
				}
				t.Status.State = taskWorking
			case "control_request":
				open[d.RequestID] = true
			case "result":
				switch {
				case d.Subtype == "success":
					t.Status.State = taskCompleted
				case canceled:
					t.Status.State = taskCanceled
				default:
					t.Status.State = taskFailed
				}
				return t.finish(said, at)
			default:
				if t.Status.State == taskSubmitted {
					t.Status.State = taskWorking
				}
			}
		case "answer":
			var d struct {
				Prompt string `json:"prompt"`
			}
			json.Unmarshal(e.Data, &d)
			delete(open, d.Prompt)
		case "stopped":
			// The process went before the turn ended: restarted, crashed.
			t.Status.State = taskFailed
			if canceled {
				t.Status.State = taskCanceled
			}
			said = append(said, "(the session's process ended before the turn did)")
			return t.finish(said, at)
		}
	}
	if len(open) > 0 {
		t.Status.State = taskInputRequired
	} else if canceled {
		t.Status.State = taskCanceled
	}
	return t.finish(said, at)
}

func (t a2aTask) finish(said []string, at time.Time) a2aTask {
	if !at.IsZero() {
		t.Status.Timestamp = at.UTC().Format(time.RFC3339Nano)
	}
	if len(said) > 0 {
		text := strings.Join(said, "\n\n")
		t.Status.Message = &a2aMessage{MessageID: t.ID + "-reply", ContextID: t.ContextID, TaskID: t.ID,
			Role: "ROLE_AGENT", Parts: []a2aPart{{Text: text}}}
		if t.Status.State == taskCompleted {
			t.Artifacts = []a2aArtifact{{ArtifactID: t.ID + "-reply", Name: "reply", Parts: []a2aPart{{Text: text}}}}
		}
	}
	return t
}

// turnOf returns the session's events from the message with id on, or false
// when it has none: those in memory, else the log on disk.
func (s *Session) turnOf(id string) ([]Event, bool) {
	s.mu.Lock()
	for i, e := range s.events {
		if e.Kind == "message" && messageID(e) == id {
			out := append([]Event(nil), s.events[i:]...)
			s.mu.Unlock()
			return out, true
		}
	}
	var first uint64 = ^uint64(0)
	if len(s.events) > 0 {
		first = s.events[0].Seq
	}
	s.mu.Unlock()
	older := s.fromDisk(0, first)
	for i, e := range older {
		if e.Kind == "message" && messageID(e) == id {
			return older[i:], true
		}
	}
	return nil, false
}

func messageID(e Event) string {
	var d struct {
		ID string `json:"id"`
	}
	json.Unmarshal(e.Data, &d)
	return d.ID
}

// a2aLimit bounds what one device may ask one session over A2A in an hour:
// two agents answering each other cannot keep each other busy for long.
const a2aLimit = 30

// a2aLimiter counts A2A messages per session and sender over the last hour,
// and remembers the tasks cancelled through A2A.
type a2aLimiter struct {
	mu       sync.Mutex
	sent     map[string][]time.Time
	canceled map[string]bool
}

func (l *a2aLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[string][]time.Time{}
	}
	kept := l.sent[key][:0]
	for _, t := range l.sent[key] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if len(kept) >= a2aLimit {
		l.sent[key] = kept
		return false
	}
	l.sent[key] = append(kept, now)
	return true
}

func (l *a2aLimiter) cancel(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.canceled == nil {
		l.canceled = map[string]bool{}
	}
	l.canceled[id] = true
}

func (l *a2aLimiter) wasCanceled(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.canceled[id]
}

// --- HTTP -------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcReply(w http.ResponseWriter, id json.RawMessage, result any, e *rpcError) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = result
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) cardBase(r *http.Request) string { return "http://" + r.Host + "/a2a/" }

// sessionCard is a session's Agent Card.
func (h *handler) sessionCard(r *http.Request, in Info) map[string]any {
	host, _ := os.Hostname()
	desc := fmt.Sprintf("%s session %q on %s, working in %s.", harnessTitle(h.m, in.Harness), in.Name, host, in.Dir)
	return map[string]any{
		"name":        host + "/" + in.Name,
		"description": desc,
		"version":     "1",
		"supportedInterfaces": []map[string]any{{"url": h.cardBase(r) + in.Name,
			"protocolBinding": "JSONRPC", "protocolVersion": "1.0"}},
		"capabilities":       map[string]any{"streaming": true, "pushNotifications": false},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills": []map[string]any{{"id": in.Name, "name": in.Name, "description": desc,
			"tags": []string{"shrooms", in.Harness}}},
	}
}

func harnessTitle(m *Manager, name string) string {
	for _, h := range m.Harnesses() {
		if h.Name == name {
			return h.Title
		}
	}
	return name
}

// machineCard lists the machine's sessions, each a skill whose own card
// is at /a2a/{session}/.well-known/agent-card.json.
func (h *handler) machineCard(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	var skills []map[string]any
	for _, in := range h.m.List() {
		skills = append(skills, map[string]any{"id": in.Name, "name": in.Name,
			"description": fmt.Sprintf("%s session in %s; its card: %s%s/.well-known/agent-card.json",
				harnessTitle(h.m, in.Harness), in.Dir, h.cardBase(r), in.Name),
			"tags": []string{"shrooms", in.Harness}})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        host,
		"description": "The coding-agent sessions on " + host + ", one A2A agent each; send to a session by its own card.",
		"version":     "1",
		"supportedInterfaces": []map[string]any{{"url": "http://" + r.Host + "/a2a",
			"protocolBinding": "JSONRPC", "protocolVersion": "1.0"}},
		"capabilities":       map[string]any{"streaming": true, "pushNotifications": false},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             skills,
	})
}

func (h *handler) a2aCard(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.sessionCard(r, s.Info()))
}

// a2a serves JSON-RPC for a session: POST /a2a/{name}, or POST /a2a with the
// session in the message's metadata ("shrooms/session").
func (h *handler) a2a(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		rpcReply(w, nil, nil, &rpcError{rpcParseError, err.Error()})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidRequest, "not a JSON-RPC 2.0 request"})
		return
	}
	var p struct {
		Message       *a2aMessage `json:"message"`
		ID            string      `json:"id"`
		Configuration struct {
			Blocking bool `json:"blocking"`
		} `json:"configuration"`
	}
	json.Unmarshal(req.Params, &p)
	name := r.PathValue("name")
	if name == "" && p.Message != nil {
		name, _ = p.Message.Metadata["shrooms/session"].(string)
	}
	if name == "" {
		// A task id names its session too: "<session>:<message id>".
		if i := strings.Index(p.ID, ":"); i > 0 {
			name = p.ID[:i]
		}
	}
	s, ok := h.m.Get(name)
	if !ok {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, fmt.Sprintf("no session called %q", name)})
		return
	}
	switch req.Method {
	case "SendMessage", "SendStreamingMessage":
		h.a2aSend(w, r, req, s, p.Message, p.Configuration.Blocking || req.Method == "SendStreamingMessage")
	case "GetTask", "SubscribeToTask", "CancelTask":
		id := strings.TrimPrefix(p.ID, s.Name()+":")
		evs, found := s.turnOf(id)
		if !found {
			rpcReply(w, req.ID, nil, &rpcError{a2aTaskNotFound, "no task " + p.ID})
			return
		}
		t := h.task(s, id, evs)
		switch req.Method {
		case "GetTask":
			rpcReply(w, req.ID, map[string]any{"task": t}, nil)
		case "SubscribeToTask":
			h.a2aStream(w, r, req.ID, s, id)
		case "CancelTask":
			if terminal(t.Status.State) {
				rpcReply(w, req.ID, nil, &rpcError{a2aNotCancelable, "task already " + t.Status.State})
				return
			}
			h.m.a2a.cancel(s.Name() + ":" + id)
			if err := s.Interrupt(h.caller(r)); err != nil {
				rpcReply(w, req.ID, nil, &rpcError{a2aNotCancelable, err.Error()})
				return
			}
			t.Status.State = taskCanceled
			rpcReply(w, req.ID, map[string]any{"task": t}, nil)
		}
	default:
		rpcReply(w, req.ID, nil, &rpcError{rpcNoMethod, "not supported here: " + req.Method})
	}
}

// task is the task as it stands, under its full id ("<session>:<message id>"),
// so that the id alone finds it again through POST /a2a.
func (h *handler) task(s *Session, id string, evs []Event) a2aTask {
	s.mu.Lock()
	ctx := s.convID
	s.mu.Unlock()
	if ctx == "" {
		ctx = s.Name()
	}
	t := taskFrom(evs, id, ctx, h.m.a2a.wasCanceled(s.Name()+":"+id))
	t.ID = s.Name() + ":" + id
	t.Metadata = map[string]any{"shrooms/session": s.Name()}
	return t
}

func (h *handler) a2aSend(w http.ResponseWriter, r *http.Request, req rpcRequest, s *Session, m *a2aMessage, wait bool) {
	if m == nil || m.MessageID == "" {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, "a message with a messageId is needed"})
		return
	}
	if m.TaskID != "" {
		rpcReply(w, req.ID, nil, &rpcError{a2aUnsupported, "continuing a task is not supported yet: send a new message"})
		return
	}
	var text []string
	for _, p := range m.Parts {
		if strings.TrimSpace(p.Text) != "" {
			text = append(text, p.Text)
		}
	}
	if len(text) == 0 {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, "only text parts are taken, and there were none"})
		return
	}
	by := h.caller(r)
	if from, _ := m.Metadata["shrooms/from"].(string); from != "" && by != "" {
		// Who on that machine: its claim, beside what the mesh says.
		by = by + " (" + from + ")"
	}
	id := m.MessageID
	if _, seen := s.turnOf(id); !seen {
		if !h.m.a2a.allow(s.Name()+"|"+h.caller(r), time.Now()) {
			rpcReply(w, req.ID, nil, &rpcError{a2aTooMany, fmt.Sprintf("more than %d messages to this session from you in an hour", a2aLimit)})
			return
		}
		// A task is a turn: one sent while another runs would be folded into
		// it (Claude Code) or queued behind it, and its result would be
		// taken for another's. Busy says so; ask again.
		if st := s.Info().State; st != Idle {
			t := a2aTask{ID: s.Name() + ":" + id, ContextID: s.Name(), Status: a2aStatus{State: taskRejected,
				Message: &a2aMessage{MessageID: id + "-busy", Role: "ROLE_AGENT",
					Parts: []a2aPart{{Text: "busy (" + string(st) + "): ask again when it is idle"}}},
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}}
			rpcReply(w, req.ID, map[string]any{"task": t}, nil)
			return
		}
		if _, err := s.SendID(strings.Join(text, "\n\n"), by, id); err != nil {
			rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, err.Error()})
			return
		}
	}
	if req.Method == "SendStreamingMessage" {
		h.a2aStream(w, r, req.ID, s, id)
		return
	}
	t := h.awaitTask(r, s, id, wait)
	rpcReply(w, req.ID, map[string]any{"task": t}, nil)
}

// a2aWait bounds how long a blocking SendMessage holds the request: a long
// turn is followed with GetTask or SubscribeToTask instead.
var a2aWait = 10 * time.Minute

// awaitTask returns the task now, or, when wait, once it ends or needs input.
func (h *handler) awaitTask(r *http.Request, s *Session, id string, wait bool) a2aTask {
	_, ch := s.Since(^uint64(0) >> 1)
	defer s.Unsubscribe(ch)
	timeout := time.After(a2aWait)
	for {
		evs, _ := s.turnOf(id)
		t := h.task(s, id, evs)
		if !wait || terminal(t.Status.State) || t.Status.State == taskInputRequired {
			return t
		}
		select {
		case <-ch:
		case <-timeout:
			return t
		case <-r.Context().Done():
			return t
		}
	}
}

// a2aStream sends the task, then each change of its status, as server-sent
// events of JSON-RPC responses, and ends with the one that is final.
func (h *handler) a2aStream(w http.ResponseWriter, r *http.Request, rid json.RawMessage, s *Session, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		rpcReply(w, rid, nil, &rpcError{a2aUnsupported, "streaming unsupported"})
		return
	}
	_, ch := s.Since(^uint64(0) >> 1)
	defer s.Unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rid, "result": result})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	evs, _ := s.turnOf(id)
	t := h.task(s, id, evs)
	send(map[string]any{"task": t})
	last := t.Status.State
	textLen := 0
	if t.Status.Message != nil {
		textLen = len(t.Status.Message.Parts[0].Text)
	}
	for !terminal(last) {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-time.After(20 * time.Second):
		}
		evs, _ := s.turnOf(id)
		t = h.task(s, id, evs)
		n := 0
		if t.Status.Message != nil {
			n = len(t.Status.Message.Parts[0].Text)
		}
		if t.Status.State == last && n == textLen {
			continue
		}
		last, textLen = t.Status.State, n
		send(map[string]any{"statusUpdate": map[string]any{"taskId": t.ID, "contextId": t.ContextID,
			"status": t.Status, "final": terminal(t.Status.State)}})
	}
}
