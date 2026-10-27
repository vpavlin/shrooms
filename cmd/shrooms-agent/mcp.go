package main

// `shrooms-agent mcp`: an MCP server (stdio) with the mesh's agents as tools.
// shrooms-agent gives it to every session it starts — Claude Code by
// --mcp-config, pi through ~/.pi/agent/mcp.json — so an agent asks another
// agent the way it calls any tool (docs/agents.md, "Agents together";
// ADR-041).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const mcpInstructions = "The agents on this shrooms mesh: other machines' coding-agent sessions, " +
	"each named MACHINE/SESSION. list_agents shows them; ask_agent sends one a message and returns its reply; " +
	"task_status follows a task ask_agent returned without waiting."

var mcpTools = []map[string]any{
	{
		"name":        "list_agents",
		"description": "List the coding-agent sessions on the shrooms mesh, as MACHINE/SESSION with their harness, state (idle, working, waiting) and working directory, and, where their owner set them, what each is good at and responsible for: ask the one whose skills fit.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "ask_agent",
		"description": "Send a message to another agent session (MACHINE/SESSION, from list_agents) and, by default, wait for its reply — " +
			"up to 10 minutes. It becomes a task, which stays open until the other agent finishes it (done) or needs something " +
			"(input-required: answer with ask_agent and task set); a busy session queues it. From a cage, a session whose owner has " +
			"not allowed you holds it (auth-required) until they allow or decline you; nothing to do but wait. With wait false it returns a task id " +
			"for task_status. When you have what you needed, close it with task_ack.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"to", "text"}, "properties": map[string]any{
			"to":   map[string]any{"type": "string", "description": "MACHINE/SESSION, e.g. proteus/proteus"},
			"text": map[string]any{"type": "string", "description": "The message: say who you are, what you want, and whether you need a reply."},
			"title": map[string]any{"type": "string", "description": "A few words saying what you ask, e.g. \"Review notes on shrooms-board\": " +
				"lists of tasks show it. Always set it on a new task; not used when answering one."},
			"task": map[string]any{"type": "string", "description": "Only to answer a task that needs input: MACHINE/SESSION:MESSAGE-ID."},
			"wait": map[string]any{"type": "boolean", "description": "Wait for the reply (default true), for up to 45 s; a task still going after that is returned with its id, to follow with task_status."},
			"fresh": map[string]any{"type": "boolean", "description": "Start the session on a fresh conversation for this new task: none of its earlier context, and a caged " +
				"session's working directory cleared (its tools kept). Only where its owner allows other agents to (fresh_by_agents), and only while " +
				"it has no other task open; refused with why otherwise. For a session with one job per task, such as an auditor; leave it off to talk."},
		}},
	},
	{
		"name":        "task_status",
		"description": "Where a task from ask_agent stands — working, completed, failed, input-required, auth-required (held for the other side's owner), rejected, canceled — and its reply so far.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task"}, "properties": map[string]any{
			"task": map[string]any{"type": "string", "description": "MACHINE/SESSION:MESSAGE-ID, as ask_agent returned it (machine, slash, task id)"},
		}},
	},
	{
		"name": "task_update",
		"description": "Finish a task another agent gave you (a message beginning \"[shrooms task ID …]\"): state \"done\" with a summary " +
			"of the result; \"blocked\" only to ask the asker a question you need answered to go on (it is sent to them, and the " +
			"task waits for their answer); \"failed\" with why when you cannot or will not do it — never \"blocked\" to give up. " +
			"Until you do, the task stays open and you are reminded if you go quiet.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task", "state", "summary"}, "properties": map[string]any{
			"task":    map[string]any{"type": "string", "description": "The task id from the message: SESSION:MESSAGE-ID"},
			"state":   map[string]any{"type": "string", "enum": []string{"done", "blocked", "failed"}},
			"summary": map[string]any{"type": "string", "description": "The result; for blocked, the question for the asker; for failed, why — what the asker reads."},
		}},
	},
	{
		"name": "send_file",
		"description": "Send a file to another agent session (MACHINE/SESSION). It keeps it in a folder for files from you, " +
			"and is told where, with your note; only if it takes files from you — if not, the refusal says how to allow it. " +
			"At most 100 MB a file.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"to", "path"}, "properties": map[string]any{
			"to":   map[string]any{"type": "string", "description": "MACHINE/SESSION, e.g. laptop/reviewer"},
			"path": map[string]any{"type": "string", "description": "the file on this machine"},
			"note": map[string]any{"type": "string", "description": "What it is and what to do with it: the receiver reads this with the file's place."},
		}},
	},
	{
		"name":        "task_ack",
		"description": "Close a task you gave another agent, once you have what you needed from it: MACHINE/SESSION:MESSAGE-ID.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task"}, "properties": map[string]any{
			"task": map[string]any{"type": "string"},
		}},
	},
	{
		"name": "schedule",
		"description": "Your own routines, in the calendar you and your owner share (ADR-064): list them, add one, change one, or remove one. " +
			"Add a routine only once your owner has agreed to it in conversation; they are told of every change you make. " +
			"A routine has a title, a start (RFC 3339, e.g. 2026-10-20T08:00:00+02:00), how often (freq daily, weekly, monthly or yearly, " +
			"and interval: 2 is every other), a prompt you will be sent when it is due, and optional checks: shell commands, one a line, " +
			"run first without you; you are woken only when what they print changed.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"action"}, "properties": map[string]any{
			"action":   map[string]any{"type": "string", "enum": []string{"list", "add", "change", "remove"}},
			"id":       map[string]any{"type": "string", "description": "the routine, for change and remove"},
			"title":    map[string]any{"type": "string"},
			"start":    map[string]any{"type": "string", "description": "RFC 3339: when it is first due"},
			"freq":     map[string]any{"type": "string", "enum": []string{"daily", "weekly", "monthly", "yearly", ""}},
			"interval": map[string]any{"type": "integer"},
			"prompt":   map[string]any{"type": "string"},
			"checks":   map[string]any{"type": "string"},
			"enabled":  map[string]any{"type": "boolean"},
		}},
	},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// serveMCP answers MCP requests from in, one JSON-RPC message per line, until
// in ends.
func serveMCP(in io.Reader, out io.Writer, c a2aClient) error {
	enc := json.NewEncoder(out)
	var mu sync.Mutex
	var calls sync.WaitGroup // answered before returning, input closed or not
	reply := func(id json.RawMessage, result any, code int, msg string) {
		r := map[string]any{"jsonrpc": "2.0", "id": id}
		if msg != "" {
			r["error"] = map[string]any{"code": code, "message": msg}
		} else {
			r["result"] = result
		}
		mu.Lock()
		enc.Encode(r)
		mu.Unlock()
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var req mcpRequest
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			reply(nil, nil, -32700, "not JSON")
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: initialized, cancelled
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			reply(req.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "shrooms", "version": "1"},
				"instructions":    mcpInstructions,
			}, 0, "")
		case "ping":
			reply(req.ID, map[string]any{}, 0, "")
		case "tools/list":
			reply(req.ID, map[string]any{"tools": mcpTools}, 0, "")
		case "tools/call":
			var p struct {
				Name      string  `json:"name"`
				Arguments mcpArgs `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			// Each call on its own: a slow one (an ask waiting on a busy
			// agent) used to hold up every call after it, and a harness that
			// gave up on it (pi, at 60 s) then found all the tools hung.
			calls.Add(1)
			go func(id json.RawMessage) {
				defer calls.Done()
				text, err := c.call(p.Name, p.Arguments)
				if err != nil {
					text = err.Error()
				}
				reply(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": err != nil}, 0, "")
			}(req.ID)
		default:
			reply(req.ID, nil, -32601, "no method "+req.Method)
		}
	}
	calls.Wait()
	return sc.Err()
}

// mcpWait is how long ask_agent follows a task before answering with where it
// stands: under the minute a harness waits for a tool call.
const mcpWait = 45 * time.Second

// done is whether a task's state is one a waiting asker stops for: an end, or
// a question back.
func done(state string) bool {
	switch state {
	case "completed", "failed", "canceled", "rejected", "input_required", "auth_required":
		return true
	}
	return false
}

type mcpArgs struct {
	To      string `json:"to"`
	Text    string `json:"text"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Note    string `json:"note"`
	Wait    *bool  `json:"wait"`
	Fresh   bool   `json:"fresh"`
	Task    string `json:"task"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	// schedule
	Action   string `json:"action"`
	ID       string `json:"id"`
	Start    string `json:"start"`
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
	Prompt   string `json:"prompt"`
	Checks   string `json:"checks"`
	Enabled  *bool  `json:"enabled"`
}

func (c a2aClient) call(tool string, a mcpArgs) (string, error) {
	wait := a.Wait == nil || *a.Wait
	switch tool {
	case "list_agents":
		return c.list()
	case "ask_agent":
		if a.To == "" && a.Task != "" {
			a.To, _, _ = strings.Cut(a.Task, ":")
		}
		if a.To == "" || a.Text == "" {
			return "", fmt.Errorf("ask_agent needs to and text")
		}
		var t cliTask
		var err error
		// Sent without blocking, then followed for at most mcpWait: a
		// harness gives up on a tool call after a minute (pi does, at 60 s),
		// and an agent busy with other work can take far longer. The task
		// goes on either way; what comes back says how to follow it.
		if a.Task != "" {
			t, err = c.answer(a.Task, a.Text, false)
		} else {
			t, err = c.send(a.To, a.Text, a.Title, false, a.Fresh)
		}
		if err != nil {
			return "", err
		}
		name, _, _ := strings.Cut(a.To, "/")
		ref := name + "/" + t.ID
		if wait {
			for end := time.Now().Add(mcpWait); time.Now().Before(end); {
				if done(t.State()) {
					break
				}
				time.Sleep(2 * time.Second)
				if u, err := c.task(ref, "GetTask"); err == nil {
					t = u
				}
			}
		}
		t.ID = ref
		out := t.String()
		if wait && !done(t.State()) {
			out += fmt.Sprintf("\n(still %s after %d s: it goes on. Follow it with task_status, task %s; you are also told when it needs you or ends.)\n",
				strings.ReplaceAll(t.State(), "_", "-"), int(mcpWait.Seconds()), ref)
		}
		return out, nil
	case "send_file":
		if a.To == "" || a.Path == "" {
			return "", fmt.Errorf("send_file needs to and path")
		}
		at, err := c.sendFile(a.To, a.Path, a.Note, true)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("sent %s to %s, kept there at %s", filepath.Base(a.Path), a.To, at), nil
	case "task_status", "task_ack":
		method := "GetTask"
		if tool == "task_ack" {
			method = "AckTask"
		}
		t, err := c.task(a.Task, method)
		if err != nil {
			return "", err
		}
		name, _, _ := strings.Cut(a.Task, "/")
		t.ID = name + "/" + t.ID
		return t.String(), nil
	case "schedule":
		return c.schedule(a)
	case "task_update":
		if a.Task == "" || a.State == "" {
			return "", fmt.Errorf("task_update needs task and state")
		}
		t, err := c.update(a.Task, a.State, a.Summary)
		if err != nil {
			return "", err
		}
		return t.String(), nil
	}
	return "", fmt.Errorf("no tool %q", tool)
}

func mcpMain(args []string) error {
	sock := "/run/shrooms/shrooms.sock"
	// The agent that starts a session says which daemon and which agent the
	// tools belong to (--socket, --agent-socket).
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--socket":
			sock = args[i+1]
		case "--agent-socket":
			os.Setenv("SHROOMS_AGENT_SOCKET", args[i+1])
		}
	}
	return serveMCP(os.Stdin, os.Stdout, newA2AClient(sock))
}
