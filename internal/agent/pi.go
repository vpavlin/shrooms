package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Pi is pi (pi.dev, @mariozechner/pi-coding-agent), driven in its RPC mode:
// `pi --mode rpc`, JSON commands in and events out, one per line (pi's
// docs/rpc.md). It runs on whatever model pi is set up with — a local one
// included — which is the point for machines with no Claude account.
//
// It is the example harness: everything pi says is translated here into the
// agent's stream-json shape (docs/agents-harnesses.md), so the apps show it
// without knowing it is pi. Written against pi 0.72.1.
type Pi struct {
	// Extra arguments for every process: a --provider and --model, say.
	// Without them pi uses its own settings (~/.pi/agent/settings.json).
	Extra []string
}

func (Pi) Name() string  { return "pi" }
func (Pi) Title() string { return "pi" }
func (Pi) Bin() string   { return "pi" }

// Caps: pi runs its tools without asking — there is nothing to approve — and
// its conversations are not offered for taking over (yet).
func (Pi) Caps() Caps { return Caps{} }

func (p Pi) Args(o StartOptions) []string {
	args := append([]string{"--mode", "rpc"}, p.Extra...)
	if o.Resume != "" {
		// By its file where there is one: given an id, pi looks only among
		// the sessions of the directory it runs in, and says there is none
		// for a conversation kept elsewhere — one started with
		// --session-dir, as an agent run from a script of its own may be.
		session := o.Resume
		if f, _ := p.TranscriptPath(o.Resume); f != "" {
			session = f
		}
		args = append(args, "--session", session)
	}
	return args
}

func (Pi) Codec() Codec { return &piCodec{ui: map[string]piDialog{}} }

// piStateID is the id of the get_state sent at start; its answer carries the
// conversation id and the model.
const piStateID = "shrooms-state"

// piDialog is an extension's dialog waiting for an answer, as the question it
// was shown as.
type piDialog struct {
	method   string // select, confirm, input, editor
	question string
}

type piCodec struct {
	mu        sync.Mutex
	sessionID string   // pi's session id, once get_state has answered
	model     string   // provider/id
	window    uint64   // the model's context window
	cost      float64  // the process's, summed over its assistant messages: Claude Code's meaning of total_cost_usd
	turn      rawUsage // this turn's tokens, summed over its assistant messages
	failed    bool     // the turn ended in an error or was aborted
	sent      []string // prompts written and not yet seen back as pi's user messages
	ui        map[string]piDialog
}

func (c *piCodec) Start() []any {
	return []any{map[string]string{"id": piStateID, "type": "get_state"}}
}

func (c *piCodec) Turn(text string) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, text)
	// Always as a follow-up: sent while pi is working it would be refused,
	// queued it is taken up when the turn ends — what a message sent mid-turn
	// does with Claude Code — and an idle pi starts it at once (pi 1.0.0).
	// Not only when a turn is known to run: pi retrying a 429 between
	// agent_end and its next attempt refused one ("Agent is already
	// processing", Jimmy, 2026-10-07).
	return []any{map[string]any{"type": "prompt", "message": text, "streamingBehavior": "followUp"}}
}

func (c *piCodec) Interrupt() []any { return []any{map[string]string{"type": "abort"}} }

// Respond answers an extension's dialog, shown as a question.
func (c *piCodec) Respond(requestID string, response map[string]any) []any {
	c.mu.Lock()
	d, ok := c.ui[requestID]
	delete(c.ui, requestID)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	id := strings.TrimPrefix(requestID, "ui-")
	if response["behavior"] != "allow" {
		return []any{map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true}}
	}
	answer := ""
	if in, _ := response["updatedInput"].(json.RawMessage); in != nil {
		var v struct{ Answers map[string]string }
		json.Unmarshal(in, &v)
		answer = v.Answers[d.question]
	}
	if d.method == "confirm" {
		return []any{map[string]any{"type": "extension_ui_response", "id": id, "confirmed": answer == "Yes"}}
	}
	return []any{map[string]any{"type": "extension_ui_response", "id": id, "value": answer}}
}

// piMessage is a pi AgentMessage, as much of it as is shown.
type piMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // a string, or blocks
	Model   string          `json:"model"`
	Usage   struct {
		Input      uint64 `json:"input"`
		Output     uint64 `json:"output"`
		CacheRead  uint64 `json:"cacheRead"`
		CacheWrite uint64 `json:"cacheWrite"`
		Cost       struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	} `json:"usage"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
	ToolCallID   string `json:"toolCallId"`
	IsError      bool   `json:"isError"`
	// An extension's message (role "custom"): which, and whether pi shows it.
	CustomType string `json:"customType"`
	Display    *bool  `json:"display"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (m piMessage) blocks() []piBlock {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []piBlock{{Type: "text", Text: s}}
	}
	var bs []piBlock
	json.Unmarshal(m.Content, &bs)
	return bs
}

func (m piMessage) text() string {
	var b strings.Builder
	for _, c := range m.blocks() {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func out(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// outsideTurn is a turn the harness started itself, decoded as
// {"type": outsideTurn, "by": source, "text": …}; the session records it as a
// message from by.
const outsideTurn = "outside_turn"

func outside(by, text string) []json.RawMessage {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []json.RawMessage{out(map[string]any{"type": outsideTurn, "by": by, "text": text})}
}

func piText(text string) json.RawMessage {
	return out(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}}}})
}

func (c *piCodec) Decode(line json.RawMessage) []json.RawMessage {
	var e struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Command string          `json:"command"`
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
		Message piMessage       `json:"message"`
		Delta   struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
		// auto_retry_end, compaction_end
		FinalError   string `json:"finalError"`
		ErrorMessage string `json:"errorMessage"`
	}
	// An extension's dialog is read on its own: its "message" is a string,
	// where every other event's is an object.
	var d struct {
		Type    string   `json:"type"`
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Text    string   `json:"message"`
		Options []string `json:"options"`
	}
	if json.Unmarshal(line, &d) == nil && d.Type == "extension_ui_request" {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.dialog(d.ID, d.Method, d.Title, d.Text, d.Options)
	}
	if json.Unmarshal(line, &e) != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	switch e.Type {
	case "response":
		switch {
		case e.ID == piStateID && e.Success:
			var st struct {
				SessionID string `json:"sessionId"`
				Model     *struct {
					ID            string `json:"id"`
					Provider      string `json:"provider"`
					ContextWindow uint64 `json:"contextWindow"`
				} `json:"model"`
			}
			json.Unmarshal(e.Data, &st)
			c.sessionID = st.SessionID
			if st.Model != nil {
				c.model = st.Model.Provider + "/" + st.Model.ID
				c.window = st.Model.ContextWindow
			}
			return []json.RawMessage{out(map[string]any{"type": "system", "subtype": "init",
				"session_id": c.sessionID, "model": c.model, "harness": "pi"})}
		case !e.Success && e.Command == "prompt":
			// Refused before it started: no agent_end will follow.
			return []json.RawMessage{piText("pi refused the message: " + e.Error),
				out(map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": c.sessionID})}
		}
	case "agent_start":
		c.failed = false
		c.turn = rawUsage{}
	case "message_update":
		if e.Delta.Type == "text_delta" && e.Delta.Delta != "" {
			return []json.RawMessage{out(map[string]any{"type": "stream_event", "event": map[string]any{
				"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": e.Delta.Delta}}})}
		}
	case "message_end":
		switch e.Message.Role {
		case "assistant":
			m := e.Message
			c.cost += m.Usage.Cost.Total
			c.turn.Input += m.Usage.Input
			c.turn.Output += m.Usage.Output
			c.turn.CacheRead += m.Usage.CacheRead
			c.turn.CacheWrite += m.Usage.CacheWrite
			var content []any
			for _, b := range m.blocks() {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						content = append(content, map[string]any{"type": "text", "text": b.Text})
					}
				case "thinking":
					content = append(content, map[string]any{"type": "thinking", "thinking": b.Thinking})
				case "toolCall":
					args := b.Arguments
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					content = append(content, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": args})
				}
			}
			if m.StopReason == "error" || m.StopReason == "aborted" {
				c.failed = true
			}
			if m.StopReason == "error" && m.ErrorMessage != "" {
				content = append(content, map[string]any{"type": "text", "text": "error: " + m.ErrorMessage})
			}
			if len(content) == 0 {
				return nil
			}
			model := c.model
			if m.Model != "" && !strings.Contains(model, "/"+m.Model) {
				model = m.Model
			}
			return []json.RawMessage{out(map[string]any{"type": "assistant", "message": map[string]any{
				"role": "assistant", "model": model, "content": content,
				"usage": map[string]uint64{"input_tokens": m.Usage.Input, "output_tokens": m.Usage.Output,
					"cache_read_input_tokens": m.Usage.CacheRead, "cache_creation_input_tokens": m.Usage.CacheWrite}}})}
		case "user":
			// One of ours is already in the log as the message it was sent
			// as; any other came from inside pi — an extension's
			// sendUserMessage, a chat bridge passing on what someone wrote.
			t := e.Message.text()
			for i, s := range c.sent {
				if s == t {
					c.sent = append(c.sent[:i], c.sent[i+1:]...)
					return nil
				}
			}
			return outside("pi", t)
		case "custom":
			// An extension's own message sent to the model — a heartbeat's
			// directives — unless it keeps it out of sight.
			if e.Message.Display != nil && !*e.Message.Display {
				return nil
			}
			by := e.Message.CustomType
			if by == "" {
				by = "pi"
			}
			return outside(by, e.Message.text())
		case "toolResult":
			return []json.RawMessage{out(map[string]any{"type": "user", "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "tool_result", "tool_use_id": e.Message.ToolCallID,
					"content": e.Message.text(), "is_error": e.Message.IsError}}}})}
		}
	case "agent_end":
		sub := "success"
		if c.failed {
			sub = "error_during_execution"
		}
		// As Claude Code's result: the turn's tokens, and the process's running
		// cost — which is what usage (usage.go) reads both from.
		r := map[string]any{"type": "result", "subtype": sub, "session_id": c.sessionID, "total_cost_usd": c.cost,
			"usage": map[string]uint64{"input_tokens": c.turn.Input, "output_tokens": c.turn.Output,
				"cache_read_input_tokens": c.turn.CacheRead, "cache_creation_input_tokens": c.turn.CacheWrite}}
		if c.window > 0 {
			r["modelUsage"] = map[string]any{c.model: map[string]uint64{"contextWindow": c.window}}
		}
		return []json.RawMessage{out(r)}
	case "auto_retry_end":
		if e.FinalError != "" {
			return []json.RawMessage{piText("gave up retrying: " + e.FinalError)}
		}
	case "compaction_end":
		if e.ErrorMessage != "" {
			return []json.RawMessage{piText("compaction failed: " + e.ErrorMessage)}
		}
	}
	return nil
}

// dialog is an extension asking the person something, shown as a question
// with its options and answered by Respond. The fire-and-forget methods
// (notify, setStatus, …) have no place in a chat and are dropped. Called with
// c.mu held.
func (c *piCodec) dialog(uiID, method, title, text string, options []string) []json.RawMessage {
	q := map[string]any{"question": title, "header": "pi", "multiSelect": false}
	switch method {
	case "select":
		opts := []any{}
		for _, o := range options {
			opts = append(opts, map[string]any{"label": o, "description": ""})
		}
		q["options"] = opts
	case "confirm":
		if text != "" {
			q["question"] = title + "\n\n" + text
		}
		q["options"] = []any{map[string]any{"label": "Yes", "description": ""}, map[string]any{"label": "No", "description": ""}}
	case "input", "editor":
		q["options"] = []any{}
	default:
		return nil
	}
	id := "ui-" + uiID
	c.ui[id] = piDialog{method: method, question: q["question"].(string)}
	input := map[string]any{"questions": []any{q}}
	return []json.RawMessage{
		out(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": QuestionTool, "input": input}}}}),
		out(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{
			"subtype": "can_use_tool", "tool_name": QuestionTool, "input": input}}),
	}
}

// --- transcripts ------------------------------------------------------------

// piSessions is where pi keeps its sessions: one directory per working
// directory, one JSONL file per session (pi's docs/session-format.md).
func piSessions() (string, error) {
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return filepath.Join(d, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent", "sessions"), nil
}

// TranscriptPath finds a session's file, named <timestamp>_<id>.jsonl.
func (Pi) TranscriptPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return "", nil
	}
	base, err := piSessions()
	if err != nil {
		return "", err
	}
	m, err := filepath.Glob(filepath.Join(base, "*", "*_"+id+".jsonl"))
	if err != nil || len(m) == 0 {
		return "", err
	}
	return m[0], nil
}

// ParseTranscriptLine reads a message entry: what was typed, or the model's
// text. A session is a tree — lines of a branch left behind are read too.
func (Pi) ParseTranscriptLine(line []byte) (Said, bool) {
	var e struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Message   piMessage `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "message" {
		return Said{}, false
	}
	switch e.Message.Role {
	case "user", "assistant":
		t := strings.TrimSpace(e.Message.text())
		if t == "" {
			return Said{}, false
		}
		return Said{Time: e.Timestamp, Role: e.Message.Role, Text: t}, true
	}
	return Said{}, false
}
