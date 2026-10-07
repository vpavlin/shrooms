package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakePi is `pi --mode rpc` as observed from pi 0.72.1 against a local model
// (2026-10-03): get_state answered with the session and model; a prompt
// answered, then agent_start … agent_end around assistant messages (text
// streamed as text_delta), tool calls and their results. Turns:
//
//	"run <x>"  → a bash tool call and its result, then "done";
//	"ask <q>"  → an extension's select dialog, then what was picked;
//	"fail"     → an assistant message that ended in an error;
//	anything   → "echo: <it>".
//
// A prompt sent while a turn runs without streamingBehavior is refused, as pi
// refuses it. --session <id> resumes that id.
func fakePi() {
	out := json.NewEncoder(os.Stdout)
	emit := func(v any) { out.Encode(v) }
	// A new session gets an id of its own, as pi's do: resuming is the only
	// way to see an earlier one again.
	id := fmt.Sprintf("01a1-%d", os.Getpid())
	for i, a := range os.Args {
		if a == "--session" && i+1 < len(os.Args) {
			id = os.Args[i+1]
			// As pi: given an id, only a session of this directory is found
			// (with FAKE_PI_ELSEWHERE, none is); given a file, its id is the
			// end of its name.
			if !strings.HasSuffix(id, ".jsonl") && os.Getenv("FAKE_PI_ELSEWHERE") == "1" {
				fmt.Printf("No session found matching '%s'\n", id)
				os.Exit(1)
			}
			id = strings.TrimSuffix(id[strings.LastIndex(id, "_")+1:], ".jsonl")
		}
	}
	model := map[string]any{"id": "qwen3.5:0.8b", "provider": "local", "contextWindow": 128000}
	in := bufio.NewScanner(os.Stdin)
	next := func() map[string]any {
		if !in.Scan() {
			os.Exit(0)
		}
		var m map[string]any
		json.Unmarshal(in.Bytes(), &m)
		return m
	}
	usage := func(in, cost float64) map[string]any {
		return map[string]any{"input": in, "output": 5, "cacheRead": 0, "cacheWrite": 0, "totalTokens": in + 5,
			"cost": map[string]any{"total": cost}}
	}
	assistant := func(content []any, stop string, extra map[string]any) map[string]any {
		m := map[string]any{"role": "assistant", "content": content, "api": "openai-completions", "provider": "local",
			"model": "qwen3.5:0.8b", "usage": usage(1200, 0.01), "stopReason": stop, "timestamp": 1}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	say := func(text string) {
		emit(map[string]any{"type": "message_start", "message": assistant([]any{}, "stop", nil)})
		for _, part := range strings.SplitAfter(text, " ") {
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": part}})
		}
		msg := assistant([]any{map[string]any{"type": "thinking", "thinking": "hm"}, map[string]any{"type": "text", "text": text}}, "stop", nil)
		emit(map[string]any{"type": "message_end", "message": msg})
	}
	for {
		c := next()
		reqID, _ := c["id"].(string)
		switch c["type"] {
		case "get_state":
			emit(map[string]any{"id": reqID, "type": "response", "command": "get_state", "success": true,
				"data": map[string]any{"sessionId": id, "model": model, "isStreaming": false}})
		case "abort":
			emit(map[string]any{"type": "response", "command": "abort", "success": true})
		case "prompt":
			text, _ := c["message"].(string)
			emit(map[string]any{"id": reqID, "type": "response", "command": "prompt", "success": true})
			emit(map[string]any{"type": "agent_start"})
			emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "text", "text": text}}}})
			switch {
			case strings.HasPrefix(text, "run "):
				cmd := strings.TrimPrefix(text, "run ")
				emit(map[string]any{"type": "message_end", "message": assistant([]any{map[string]any{
					"type": "toolCall", "id": "call_1", "name": "bash", "arguments": map[string]any{"command": cmd}}}, "toolUse", nil)})
				emit(map[string]any{"type": "tool_execution_start", "toolCallId": "call_1", "toolName": "bash"})
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "toolResult", "toolCallId": "call_1",
					"toolName": "bash", "content": []any{map[string]any{"type": "text", "text": "ran " + cmd + "\n"}}, "isError": false}})
				say("done")
			case strings.HasPrefix(text, "ask "):
				emit(map[string]any{"type": "extension_ui_request", "id": "u1", "method": "select",
					"title": strings.TrimPrefix(text, "ask "), "options": []string{"Allow", "Block"}})
				var r map[string]any
				for {
					r = next()
					if r["type"] == "extension_ui_response" {
						break
					}
					if r["type"] == "prompt" && r["streamingBehavior"] == nil {
						emit(map[string]any{"type": "response", "command": "prompt", "success": false,
							"error": "Agent is already processing. Specify streamingBehavior"})
					}
				}
				switch {
				case r["cancelled"] == true:
					say("cancelled")
				default:
					say("picked " + r["value"].(string))
				}
			case text == "fail":
				emit(map[string]any{"type": "message_end", "message": assistant([]any{}, "error",
					map[string]any{"errorMessage": "connection refused"})})
			default:
				say("echo: " + text)
			}
			emit(map[string]any{"type": "agent_end", "messages": []any{}})
			if text == "wake" {
				// Turns of its extensions' own: a heartbeat's directives, and
				// what a chat bridge passed on as the user's.
				emit(map[string]any{"type": "agent_start"})
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "custom",
					"customType": "heartbeat", "display": true, "content": "HEARTBEAT: pick one task"}})
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "custom",
					"customType": "messenger", "display": false, "content": "kept out of sight"}})
				say("on it")
				emit(map[string]any{"type": "agent_end", "messages": []any{}})
				emit(map[string]any{"type": "agent_start"})
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "text", "text": "from telegram: hi Jimmy"}}}})
				say("hi")
				emit(map[string]any{"type": "agent_end", "messages": []any{}})
			}
		}
	}
}

// fakePiBin is a `pi` that runs this binary as the fake.
func fakePiBin(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PI", "1")
	return self
}
