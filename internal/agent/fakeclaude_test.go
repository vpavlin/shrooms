package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests run the production session and HTTP code against a stand-in for
// `claude -p`: this test binary, re-executed with FAKE_CLAUDE=1. It speaks the
// stream-json protocol as observed from Claude Code 2.1.287 (docs/agents.md):
//
//	a system/init message carrying the session id (the resumed one, if
//	  --resume was given), then for each user turn:
//	"run <x>"  → a tool_use, a control_request can_use_tool, and after the
//	             control_response either a tool result or the denial reason;
//	"ask <q>"  → an AskUserQuestion, asked even with skipped permissions,
//	             replying with the answer it was given;
//	"slow"     → a turn that only ends when interrupted;
//	"hang"     → a turn that never ends, deaf to interrupts and EOF alike —
//	             a request stuck on a dropped connection;
//	anything   → an assistant text echoing it;
//	each turn ending with a result. EOF on stdin ends the process.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_PI") == "1" {
		fakePi()
		return
	}
	if os.Getenv("FAKE_CLAUDE") == "1" {
		fakeClaude()
		return
	}
	// A process that only sits there, under whatever name it was run as: a
	// terminal claude, for TestATerminalIsFoundAndStopped.
	if os.Getenv("FAKE_TERMINAL") == "1" {
		time.Sleep(time.Minute)
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	out := json.NewEncoder(os.Stdout)
	emit := func(v any) { out.Encode(v) }

	id, resumed := "fake-session-1", false
	for i, a := range os.Args {
		if a == "--resume" && i+1 < len(os.Args) {
			id, resumed = os.Args[i+1], true
		}
	}
	if !hasArgs(os.Args, "--permission-prompt-tool", "stdio") {
		// Without it Claude Code denies every prompt on its own; a session
		// started that way could never be approved from a phone.
		emit(map[string]any{"type": "result", "subtype": "error", "result": "no permission prompt tool"})
		os.Exit(3)
	}
	skip := hasFlag(os.Args, "--dangerously-skip-permissions")
	stream := hasFlag(os.Args, "--include-partial-messages")
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": id, "resumed": resumed, "cwd": mustWd(),
		"model":            "claude-opus-5[1m]",
		"skip_permissions": skip})

	in := bufio.NewScanner(os.Stdin)
	next := func() map[string]any {
		if !in.Scan() {
			os.Exit(0)
		}
		var m map[string]any
		json.Unmarshal(in.Bytes(), &m)
		return m
	}
	// Shaped as Claude Code 2.1.287 sends them: the reply streamed as
	// text_delta events when asked for, then the whole message with its usage;
	// the turn's result carries each model's context window.
	text := func(s string) {
		if stream {
			for _, part := range strings.SplitAfter(s, " ") {
				emit(map[string]any{"type": "stream_event", "parent_tool_use_id": nil, "event": map[string]any{
					"type": "content_block_delta", "index": 1,
					"delta": map[string]any{"type": "text_delta", "text": part}}})
			}
		}
		emit(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "thinking", "thinking": ""}, map[string]any{"type": "text", "text": s}},
			"usage":   map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200}}})
	}
	// As Claude Code's: the turn's tokens, and the process's running cost.
	cost := 0.0
	result := func(sub string) {
		cost += 0.01
		emit(map[string]any{"type": "result", "subtype": sub, "session_id": id, "total_cost_usd": cost,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200, "output_tokens": 50},
			"modelUsage": map[string]any{
				"claude-opus-5[1m]":         map[string]any{"contextWindow": 1000000},
				"claude-haiku-4-5-20251001": map[string]any{"contextWindow": 200000}}})
	}

	for {
		m := next()
		if m["type"] != "user" {
			continue
		}
		content, _ := m["message"].(map[string]any)["content"].(string)
		switch {
		case strings.HasPrefix(content, "run "):
			cmd := strings.TrimPrefix(content, "run ")
			emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash",
					"input": map[string]any{"command": cmd}}}}})
			if skip {
				// --dangerously-skip-permissions: nothing asks.
				emit(map[string]any{"type": "user", "message": map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1",
						"content": "ran " + cmd}}}})
				text("done")
				result("success")
				continue
			}
			emit(map[string]any{"type": "control_request", "request_id": "req-1", "request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": cmd},
				"description": "Run " + cmd}})
			var resp map[string]any
			for {
				resp = next()
				if resp["type"] == "control_response" {
					break
				}
			}
			r := resp["response"].(map[string]any)
			if r["request_id"] != "req-1" {
				text("answered the wrong prompt")
			}
			inner := r["response"].(map[string]any)
			if inner["behavior"] == "allow" {
				got := inner["updatedInput"].(map[string]any)["command"]
				emit(map[string]any{"type": "user", "message": map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1",
						"content": fmt.Sprintf("ran %v", got)}}}})
				text("done")
			} else {
				text(fmt.Sprintf("denied: %v", inner["message"]))
			}
			result("success")
		case strings.HasPrefix(content, "ask "):
			// AskUserQuestion: asked through can_use_tool even with
			// --dangerously-skip-permissions (observed 2026-10-03), and
			// answered by allowing it with input.answers filled in.
			q := strings.TrimPrefix(content, "ask ")
			input := map[string]any{"questions": []any{map[string]any{"question": q, "header": "Pick",
				"multiSelect": false, "options": []any{
					map[string]any{"label": "yes", "description": "do it"},
					map[string]any{"label": "no", "description": "leave it"}}}}}
			emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": "toolu_q", "name": "AskUserQuestion", "input": input}}}})
			emit(map[string]any{"type": "control_request", "request_id": "req-q", "request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "AskUserQuestion", "input": input}})
			var resp map[string]any
			for {
				resp = next()
				if resp["type"] == "control_response" {
					break
				}
			}
			inner := resp["response"].(map[string]any)["response"].(map[string]any)
			switch {
			case inner["behavior"] != "allow":
				text(fmt.Sprintf("denied: %v", inner["message"]))
			default:
				ans, _ := inner["updatedInput"].(map[string]any)["answers"].(map[string]any)
				if len(ans) == 0 {
					text("The user did not answer the questions.")
				} else {
					text(fmt.Sprintf("answered: %v", ans[q]))
				}
			}
			result("success")
		case content == "background":
			// Observed 2026-10-04: a turn ends while a background command
			// runs, heartbeats keep coming, and when it finishes Claude Code
			// starts a turn of its own — no user message — and ends it.
			text("started it in the background")
			result("success")
			for i := 0; i < 3; i++ {
				emit(map[string]any{"type": "tool_progress", "tool_name": "Bash", "heartbeat": true, "elapsed_time_seconds": 30 * (i + 1)})
				emit(map[string]any{"type": "system", "subtype": "thinking_tokens", "estimated_tokens": 50})
			}
			emit(map[string]any{"type": "system", "subtype": "task_notification", "status": "completed"})
			emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": "toolu_b", "name": "Bash",
					"input": map[string]any{"command": "cat build.log"}}}}})
			if marker := os.Getenv("FAKE_HOLD"); marker != "" {
				for { // until the test lets the resumed turn go on
					if _, err := os.Stat(marker); err == nil {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			text("the build passed")
			result("success")
		case content == "hang":
			time.Sleep(time.Hour)
		case content == "slow":
			for {
				c := next()
				if c["type"] == "control_request" {
					if r, _ := c["request"].(map[string]any); r["subtype"] == "interrupt" {
						break
					}
				}
			}
			result("error_during_execution")
		default:
			text("echo: " + content)
			result("success")
		}
	}
}

func hasArgs(args []string, k, v string) bool {
	for i := range args {
		if args[i] == k && i+1 < len(args) && args[i+1] == v {
			return true
		}
	}
	return false
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

func mustWd() string {
	wd, _ := os.Getwd()
	return wd
}

// fakeClaudeBin is a `claude` that runs this binary as the fake.
func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE", "1")
	return self
}
