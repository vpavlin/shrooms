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
	"strings"
)

const mcpInstructions = "The agents on this shrooms mesh: other machines' coding-agent sessions, " +
	"each named MACHINE/SESSION. list_agents shows them; ask_agent sends one a message and returns its reply; " +
	"task_status follows a task ask_agent returned without waiting."

var mcpTools = []map[string]any{
	{
		"name":        "list_agents",
		"description": "List the coding-agent sessions on the shrooms mesh, as MACHINE/SESSION with their harness, state (idle, working, waiting) and working directory.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "ask_agent",
		"description": "Send a message to another agent session (MACHINE/SESSION, from list_agents) and, by default, wait for its reply — " +
			"up to 10 minutes. It becomes a task, which stays open until the other agent finishes it (done) or needs something " +
			"(input-required: answer with ask_agent and task set); a busy session queues it. With wait false it returns a task id " +
			"for task_status. When you have what you needed, close it with task_ack.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"to", "text"}, "properties": map[string]any{
			"to":   map[string]any{"type": "string", "description": "MACHINE/SESSION, e.g. proteus/proteus"},
			"text": map[string]any{"type": "string", "description": "The message: say who you are, what you want, and whether you need a reply."},
			"task": map[string]any{"type": "string", "description": "Only to answer a task that needs input: MACHINE/SESSION:MESSAGE-ID."},
			"wait": map[string]any{"type": "boolean", "description": "Wait for the reply (default true)."},
		}},
	},
	{
		"name":        "task_status",
		"description": "Where a task from ask_agent stands — working, completed, failed, input-required, canceled — and its reply so far.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task"}, "properties": map[string]any{
			"task": map[string]any{"type": "string", "description": "MACHINE/SESSION:MESSAGE-ID, as ask_agent returned it (machine, slash, task id)"},
		}},
	},
	{
		"name": "task_update",
		"description": "Finish a task another agent gave you (a message beginning \"[shrooms task ID …]\"): state \"done\" with a summary " +
			"of the result, \"blocked\" with what you need from the asker, or \"failed\" with why. Until you do, the task stays open " +
			"and you are reminded if you go quiet.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task", "state", "summary"}, "properties": map[string]any{
			"task":    map[string]any{"type": "string", "description": "The task id from the message: SESSION:MESSAGE-ID"},
			"state":   map[string]any{"type": "string", "enum": []string{"done", "blocked", "failed"}},
			"summary": map[string]any{"type": "string", "description": "The result, what you need, or why it failed — what the asker reads."},
		}},
	},
	{
		"name":        "task_ack",
		"description": "Close a task you gave another agent, once you have what you needed from it: MACHINE/SESSION:MESSAGE-ID.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task"}, "properties": map[string]any{
			"task": map[string]any{"type": "string"},
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
	reply := func(id json.RawMessage, result any, code int, msg string) {
		r := map[string]any{"jsonrpc": "2.0", "id": id}
		if msg != "" {
			r["error"] = map[string]any{"code": code, "message": msg}
		} else {
			r["result"] = result
		}
		enc.Encode(r)
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
			text, err := c.call(p.Name, p.Arguments)
			if err != nil {
				text = err.Error()
			}
			reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": err != nil}, 0, "")
		default:
			reply(req.ID, nil, -32601, "no method "+req.Method)
		}
	}
	return sc.Err()
}

type mcpArgs struct {
	To      string `json:"to"`
	Text    string `json:"text"`
	Wait    *bool  `json:"wait"`
	Task    string `json:"task"`
	State   string `json:"state"`
	Summary string `json:"summary"`
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
		if a.Task != "" {
			t, err = c.answer(a.Task, a.Text, wait)
		} else {
			t, err = c.send(a.To, a.Text, wait)
		}
		if err != nil {
			return "", err
		}
		name, _, _ := strings.Cut(a.To, "/")
		t.ID = name + "/" + t.ID
		return t.String(), nil
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
	if len(args) == 2 && args[0] == "--socket" {
		sock = args[1]
	}
	return serveMCP(os.Stdin, os.Stdout, newA2AClient(sock))
}
