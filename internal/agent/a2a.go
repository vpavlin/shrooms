package agent

// A2A (Agent2Agent, v1.0.0, a2a-protocol.org) on the agent's own port: each
// session is an A2A agent with its card, and a message to it is a task — one
// turn, from its message to its result (docs/agents-together.md, "On the
// wire: A2A"). The JSON-RPC binding only; the mesh is the authentication, as
// for the rest of the API: the source address names the device that asks.

import (
	"encoding/json"
	"fmt"
	"io"
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
	// a2aCagedRefused: the session takes no tasks from caged agents.
	a2aCagedRefused = -32051
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
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    a2aStatus     `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
	// History is A2A's record of the messages that make the task; ours holds
	// the request, so whoever shows a task can say what was asked and not
	// only what was answered.
	History  []a2aMessage   `json:"history,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// a2aLimit bounds what one device may ask one session over A2A in an hour:
// two agents answering each other cannot keep each other busy for long.
const a2aLimit = 30

// a2aLimiter counts A2A messages per session and sender over the last hour.
type a2aLimiter struct {
	mu   sync.Mutex
	sent map[string][]time.Time
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
		"capabilities":       a2aCapabilities(),
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
		"capabilities":       a2aCapabilities(),
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

// taskExtension names the shrooms extension to A2A's tasks: they last until
// the worker finishes them, are supervised, and are acknowledged by the asker
// (docs/a2a-tasks.md). Its fields are in each task's metadata under
// "shrooms/"; AckTask and ListTasks are its methods.
const taskExtension = "https://github.com/vpavlin/shrooms/blob/master/docs/a2a-tasks.md"

func a2aCapabilities() map[string]any {
	return map[string]any{"streaming": true, "pushNotifications": false,
		"extensions": []map[string]any{{"uri": taskExtension, "required": false,
			"description": "A task stays open until the worker finishes it (or it is cancelled or expires); " +
				"a quiet worker is reminded, a stuck one marked stalled; the asker acknowledges the result (AckTask)."}}}
}

// view is a task as A2A shows it, with the session's own state folded in: a
// permission prompt waiting is input-required, and the reply is the worker's
// summary or, while it works, the last thing it said.
func (h *handler) view(t Task) a2aTask {
	s, ok := h.m.Get(t.Session)
	ctx := t.Session
	state := t.State
	text := t.Summary
	last := ""
	if ok {
		s.mu.Lock()
		if s.convID != "" {
			ctx = s.convID
		}
		prompts := len(s.pending)
		s.mu.Unlock()
		if !t.Started.IsZero() {
			last = s.lastWords(t.Started)
		}
		if state == taskWorking && prompts > 0 {
			state = taskInputRequired
			text = "waiting for a permission prompt to be answered on the session's machine"
		}
	}
	if text == "" {
		text = last
	}
	out := a2aTask{ID: t.ID, ContextID: ctx, Status: a2aStatus{State: state, Timestamp: t.Updated.UTC().Format(time.RFC3339Nano)},
		Metadata: map[string]any{
			"shrooms/session": t.Session, "shrooms/from": t.From,
			"shrooms/acknowledged": t.Acked, "shrooms/nudges": t.Nudges, "shrooms/stalled": t.Stalled,
			"shrooms/queued": t.State == taskSubmitted, "shrooms/expired": t.Expired,
			"shrooms/last_worker_line": trim(last, 600),
			// Asked by a caged agent, as its own agent said (ADR-044).
			"shrooms/caged": strings.Contains(t.From, ", in a cage)"),
		}}
	if t.Title != "" {
		out.Metadata["shrooms/title"] = t.Title
	}
	if t.Request != "" {
		out.History = []a2aMessage{{MessageID: t.MessageID, ContextID: ctx, TaskID: t.ID,
			Role: "ROLE_USER", Parts: []a2aPart{{Text: t.Request}}}}
	}
	if !t.LastNudge.IsZero() {
		out.Metadata["shrooms/last_nudge"] = t.LastNudge.UTC().Format(time.RFC3339)
	}
	if text != "" {
		out.Status.Message = &a2aMessage{MessageID: t.MessageID + "-status", ContextID: ctx, TaskID: t.ID,
			Role: "ROLE_AGENT", Parts: []a2aPart{{Text: text}}}
		if state == taskCompleted {
			out.Artifacts = []a2aArtifact{{ArtifactID: t.MessageID + "-result", Name: "result", Parts: []a2aPart{{Text: text}}}}
		}
	}
	return out
}

// a2a serves JSON-RPC for a session: POST /a2a/{name}, or POST /a2a with the
// session named by the task id ("session:messageId") or by the message's
// metadata ("shrooms/session").
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
		ReferenceTaskIDs []string `json:"referenceTaskIds"`
	}
	json.Unmarshal(req.Params, &p)
	name := r.PathValue("name")
	if name == "" && p.Message != nil {
		name, _ = p.Message.Metadata["shrooms/session"].(string)
		if name == "" && p.Message.TaskID != "" {
			name, _, _ = strings.Cut(p.Message.TaskID, ":")
		}
	}
	if name == "" {
		name, _, _ = strings.Cut(p.ID, ":")
	}
	s, ok := h.m.Get(name)
	if !ok {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, fmt.Sprintf("no session called %q", name)})
		return
	}
	full := func(id string) string {
		if strings.Contains(id, ":") {
			return id
		}
		return s.Name() + ":" + id
	}
	switch req.Method {
	case "SendMessage", "SendStreamingMessage":
		h.a2aSend(w, r, req, s, p.Message, p.Configuration.Blocking || req.Method == "SendStreamingMessage")
	case "ListTasks":
		var out []a2aTask
		for _, t := range h.m.Tasks(s.Name()) {
			out = append(out, h.view(t))
		}
		rpcReply(w, req.ID, map[string]any{"tasks": out}, nil)
	case "GetTask", "SubscribeToTask", "CancelTask", "AckTask":
		id := full(p.ID)
		t, found := h.m.tasks.get(id)
		if !found || t.Session != s.Name() {
			rpcReply(w, req.ID, nil, &rpcError{a2aTaskNotFound, "no task " + p.ID})
			return
		}
		switch req.Method {
		case "GetTask":
			rpcReply(w, req.ID, map[string]any{"task": h.view(t)}, nil)
		case "SubscribeToTask":
			h.a2aStream(w, r, req.ID, id)
		case "CancelTask":
			t, err := h.m.Cancel(id, h.caller(r))
			if err != nil {
				rpcReply(w, req.ID, nil, &rpcError{a2aNotCancelable, err.Error()})
				return
			}
			rpcReply(w, req.ID, map[string]any{"task": h.view(t)}, nil)
		case "AckTask":
			t, err := h.m.Ack(id)
			if err != nil {
				rpcReply(w, req.ID, nil, &rpcError{a2aTaskNotFound, err.Error()})
				return
			}
			rpcReply(w, req.ID, map[string]any{"task": h.view(t)}, nil)
		}
	default:
		rpcReply(w, req.ID, nil, &rpcError{rpcNoMethod, "not supported here: " + req.Method})
	}
}

func (h *handler) a2aSend(w http.ResponseWriter, r *http.Request, req rpcRequest, s *Session, m *a2aMessage, wait bool) {
	if m == nil || m.MessageID == "" {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, "a message with a messageId is needed"})
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
	caged := r.Header.Get(cagedHeader) != ""
	if from, _ := m.Metadata["shrooms/from"].(string); from != "" && (by != "" || caged) {
		// Who on that machine: its claim, beside what the mesh says — or,
		// from a cage, what its own agent says (ADR-044).
		if by == "" {
			by = "this machine"
		}
		if caged {
			by = by + " (" + from + ", in a cage)"
		} else {
			by = by + " (" + from + ")"
		}
	}
	// The receiver decides whether it works for caged agents (ADR-044). A
	// task already taken goes on.
	if caged && m.TaskID == "" && !s.AcceptsCaged() {
		rpcReply(w, req.ID, nil, &rpcError{a2aCagedRefused, "session " + s.Name() + " takes no tasks from caged agents"})
		return
	}
	var params struct {
		ReferenceTaskIDs []string `json:"referenceTaskIds"`
	}
	json.Unmarshal(req.Params, &params)
	var t Task
	var err error
	if m.TaskID != "" {
		// More on a task already open: the answer to "blocked", say.
		id := m.TaskID
		if !strings.Contains(id, ":") {
			id = s.Name() + ":" + id
		}
		if _, ok := h.m.tasks.get(id); !ok {
			rpcReply(w, req.ID, nil, &rpcError{a2aTaskNotFound, "no task " + m.TaskID})
			return
		}
		t, err = h.m.FollowUp(s, id, strings.Join(text, "\n\n"))
	} else {
		if _, seen := h.m.tasks.get(s.Name() + ":" + m.MessageID); !seen &&
			!h.m.a2a.allow(s.Name()+"|"+h.caller(r), time.Now()) {
			rpcReply(w, req.ID, nil, &rpcError{a2aTooMany, fmt.Sprintf("more than %d messages to this session from you in an hour", a2aLimit)})
			return
		}
		title, _ := m.Metadata["shrooms/title"].(string)
		t, err = h.m.Submit(s, m.MessageID, by, strings.Join(text, "\n\n"), taskTitle(title), params.ReferenceTaskIDs)
		if err == nil {
			t = h.m.noteAsker(t, r, m.Metadata)
		}
	}
	if err != nil {
		rpcReply(w, req.ID, nil, &rpcError{rpcInvalidParams, err.Error()})
		return
	}
	if req.Method == "SendStreamingMessage" {
		h.a2aStream(w, r, req.ID, t.ID)
		return
	}
	rpcReply(w, req.ID, map[string]any{"task": h.awaitTask(r, t.ID, wait)}, nil)
}

// a2aWait bounds how long a blocking SendMessage holds the request: a long
// task is followed with GetTask or SubscribeToTask instead.
var a2aWait = 10 * time.Minute

// settled is a state a waiting asker hears about: finished, or needing it.
func settled(state string) bool { return terminal(state) || state == taskInputRequired }

// awaitTask is the task now, or, when wait, once it is settled (or a2aWait).
func (h *handler) awaitTask(r *http.Request, id string, wait bool) a2aTask {
	if wait {
		release := h.m.await(id)
		defer release()
	}
	deadline := time.After(a2aWait)
	for {
		t, _ := h.m.tasks.get(id)
		v := h.view(t)
		if !wait || settled(v.Status.State) {
			return v
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			return v
		case <-r.Context().Done():
			return v
		}
	}
}

// a2aStream sends the task, then each change of its status, as server-sent
// events of JSON-RPC responses, ending with the one that is final.
func (h *handler) a2aStream(w http.ResponseWriter, r *http.Request, rid json.RawMessage, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		rpcReply(w, rid, nil, &rpcError{a2aUnsupported, "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rid, "result": result})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	t, _ := h.m.tasks.get(id)
	v := h.view(t)
	send(map[string]any{"task": v})
	last, text := v.Status.State, statusText(v)
	for !terminal(last) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		t, _ := h.m.tasks.get(id)
		v = h.view(t)
		if v.Status.State == last && statusText(v) == text {
			continue
		}
		last, text = v.Status.State, statusText(v)
		send(map[string]any{"statusUpdate": map[string]any{"taskId": v.ID, "contextId": v.ContextID,
			"status": v.Status, "final": terminal(v.Status.State)}})
	}
}

func statusText(v a2aTask) string {
	if v.Status.Message == nil || len(v.Status.Message.Parts) == 0 {
		return ""
	}
	return v.Status.Message.Parts[0].Text
}

// taskUpdate is the worker's word on a task (POST /v1/tasks/{id}): from this
// machine only — its own sessions, through the shrooms MCP tool — never from
// another device, which could otherwise close work it was not given.
// taskAck is AckTask over REST: whoever may ask may acknowledge.
func (h *handler) taskAck(w http.ResponseWriter, r *http.Request) {
	t, err := h.m.Ack(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": h.view(t)})
}

// taskAnswer is more for a task's worker, from whoever answers in the
// asker's place: a person in an app, usually. Said as theirs, in the text.
func (h *handler) taskAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct{ Text string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("an answer needs text"))
		return
	}
	id := r.PathValue("id")
	t, ok := h.m.tasks.get(id)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %s", id))
		return
	}
	s, ok := h.m.Get(t.Session)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no session %s", t.Session))
		return
	}
	by := h.caller(r)
	if by == "" {
		by = "this machine"
	}
	t, err := h.m.FollowUp(s, id, "(answered by "+by+", in place of the asker) "+req.Text)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": h.view(t)})
}

// taskNudge tells the task's asker again where its task stands.
func (h *handler) taskNudge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := h.m.tasks.get(id)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %s", id))
		return
	}
	if err := h.m.tellAsker(t); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": h.view(t)})
}

// taskCancel is CancelTask over /v1, for the apps.
func (h *handler) taskCancel(w http.ResponseWriter, r *http.Request) {
	by := h.caller(r)
	if by == "" {
		by = "this machine"
	}
	t, err := h.m.Cancel(r.PathValue("id"), by)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": h.view(t)})
}

func (h *handler) taskUpdate(w http.ResponseWriter, r *http.Request) {
	if h.caller(r) != "" {
		fail(w, http.StatusForbidden, fmt.Errorf("only this machine's sessions update their tasks"))
		return
	}
	var req struct{ State, Summary, Session string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	if t, ok := h.m.tasks.get(id); !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no task %s", id))
		return
	} else if req.Session != "" && req.Session != t.Session {
		fail(w, http.StatusForbidden, fmt.Errorf("task %s is session %s's, not %s's", id, t.Session, req.Session))
		return
	}
	t, err := h.m.Update(id, req.State, req.Summary)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(t))
}

// tasksList is GET /v1/tasks[?session=]: the tasks, as the apps show them.
func (h *handler) tasksList(w http.ResponseWriter, r *http.Request) {
	var out []a2aTask
	for _, t := range h.m.Tasks(r.URL.Query().Get("session")) {
		out = append(out, h.view(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// taskTitle is an asker's title as a list shows it: one line, and short. It
// is the asker's word, so it is kept as said, only cut down.
func taskTitle(s string) string {
	return trim(strings.Join(strings.Fields(s), " "), 120)
}
