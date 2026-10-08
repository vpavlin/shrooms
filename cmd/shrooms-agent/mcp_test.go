package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The MCP server over stdio, as Claude Code and pi drive it: initialize, the
// tools, and each tool against a mesh of one machine — a stand-in agent that
// lists a session and answers A2A as shrooms-agent does.
func TestMCPServesTheMeshsAgents(t *testing.T) {
	var asked map[string]any
	var updated map[string]string
	var updatedPath string
	var methods []string
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/tasks/") {
			updatedPath = r.URL.Path
			json.NewDecoder(r.Body).Decode(&updated)
			io.WriteString(w, `{"id":"proteus:m1","status":{"state":"TASK_STATE_COMPLETED","message":{"parts":[{"text":"probe written"}]}}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/sessions":
			io.WriteString(w, `{"sessions":[{"name":"proteus","state":"idle","harness":"pi","dir":"/home/vpavlin"}]}`)
		case "/a2a/proteus", "/a2a":
			var req struct {
				Method string
				Params map[string]any
			}
			json.NewDecoder(r.Body).Decode(&req)
			methods = append(methods, req.Method)
			if req.Method == "SendMessage" {
				asked = req.Params
			}
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"task":{"id":"proteus:m1","status":{"state":"TASK_STATE_COMPLETED",`+
				`"message":{"role":"ROLE_AGENT","parts":[{"text":"42"}]}}}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer agentSrv.Close()
	c := a2aClient{
		base:  func(netip.Addr) string { return agentSrv.URL },
		peers: func() ([]machine, error) { return []machine{{"proteus", netip.MustParseAddr("fd00::2")}}, nil },
	}
	t.Setenv("SHROOMS_AGENT_SESSION", "jimmy")

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_agents","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ask_agent","arguments":{"to":"proteus/proteus","text":"what is 6*7?"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"task_status","arguments":{"task":"proteus/proteus:m1"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"ask_agent","arguments":{"to":"nobody"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"task_update","arguments":{"task":"proteus:m1","state":"done","summary":"probe written"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"task_ack","arguments":{"task":"proteus/proteus:m1"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_agent","arguments":{"task":"proteus/proteus:m1","text":"use master"}}}`,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	go func() { serveMCP(strings.NewReader(in), pw, c); pw.Close() }()
	got := map[float64]json.RawMessage{}
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var r struct {
			ID     float64
			Result json.RawMessage
		}
		json.Unmarshal(sc.Bytes(), &r)
		got[r.ID] = r.Result
	}
	if len(got) != 9 {
		t.Fatalf("answers: %d (a notification answered?)", len(got))
	}
	var init struct {
		ProtocolVersion string
		Capabilities    map[string]any
	}
	json.Unmarshal(got[1], &init)
	if init.ProtocolVersion != "2025-06-18" || init.Capabilities["tools"] == nil {
		t.Errorf("initialize: %s", got[1])
	}
	var list struct{ Tools []struct{ Name string } }
	json.Unmarshal(got[2], &list)
	if len(list.Tools) != 5 || list.Tools[1].Name != "ask_agent" || list.Tools[3].Name != "task_update" {
		t.Errorf("tools: %s", got[2])
	}
	text := func(id float64) (string, bool) {
		var r struct {
			Content []struct{ Text string }
			IsError bool
		}
		json.Unmarshal(got[id], &r)
		return r.Content[0].Text, r.IsError
	}
	if s, _ := text(3); !strings.Contains(s, "proteus/proteus\tpi, idle, in /home/vpavlin") {
		t.Errorf("list_agents: %q", s)
	}
	if s, e := text(4); e || !strings.Contains(s, "task proteus/proteus:m1: completed") || !strings.Contains(s, "42") {
		t.Errorf("ask_agent: %q", s)
	}
	msg, _ := asked["message"].(map[string]any)
	meta, _ := msg["metadata"].(map[string]any)
	if !strings.HasSuffix(meta["shrooms/from"].(string), "/jimmy") {
		t.Errorf("sent without saying who: %v", msg)
	}
	if s, _ := text(5); !strings.Contains(s, "completed") {
		t.Errorf("task_status: %q", s)
	}
	if s, e := text(6); !e || !strings.Contains(s, "needs to and text") {
		t.Errorf("a bad call: %q %v", s, e)
	}
	// The worker's word goes to its own machine's agent, saying which session it is.
	if s, e := text(7); e || !strings.Contains(s, "completed") || updatedPath != "/v1/tasks/proteus:m1" ||
		updated["state"] != "done" || updated["session"] != "jimmy" {
		t.Errorf("task_update: %q %v %s %v", s, e, updatedPath, updated)
	}
	if strings.Join(methods, ",") != "SendMessage,GetTask,AckTask,SendMessage" {
		t.Errorf("methods %v", methods)
	}
	// An answer to a task names it.
	if m, _ := asked["message"].(map[string]any); m["taskId"] != "proteus:m1" {
		t.Errorf("an answer without its task: %v", asked)
	}
}
