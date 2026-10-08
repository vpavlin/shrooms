package agent

// The last few lines of a session, as a glance shows them (GET
// /v1/sessions?tail=N): what was asked, what the model said, the tools it
// ran — for a board of every agent at once in Basecamp, without following
// each one's stream.

import (
	"encoding/json"
	"strings"
)

// maxTail bounds ?tail=N.
const maxTail = 20

// Tail is the session's last n lines, oldest first.
func (s *Session) Tail(n int) []string {
	if n <= 0 {
		return nil
	}
	if n > maxTail {
		n = maxTail
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rev []string // newest first
	for i := len(s.events) - 1; i >= 0 && len(rev) < n; i-- {
		for _, l := range reverse(eventLines(s.events[i])) {
			if len(rev) == n {
				break
			}
			rev = append(rev, l)
		}
	}
	return reverse(rev)
}

// eventLines are an event's lines for a glance, oldest first.
func eventLines(e Event) []string {
	switch e.Kind {
	case "message":
		var d struct{ Text string }
		json.Unmarshal(e.Data, &d)
		who := "you"
		if e.By == "shrooms" {
			who = "reminder"
		}
		return []string{"› " + who + ": " + firstLine(d.Text)}
	case "task":
		var d struct{ ID, State, Summary string }
		json.Unmarshal(e.Data, &d)
		return []string{"◆ task " + d.ID + " " + d.State}
	case "claude":
		var d struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.Type != "assistant" {
			return nil
		}
		var out []string
		for _, c := range d.Message.Content {
			switch c.Type {
			case "text":
				for _, l := range strings.Split(c.Text, "\n") {
					if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "```") {
						out = append(out, clip(l, 200))
					}
				}
			case "tool_use":
				out = append(out, clip("▸ "+c.Name+" "+toolGist(c.Input), 200))
			}
		}
		return out
	}
	return nil
}

// toolGist is the one thing that says what a tool call does.
func toolGist(in json.RawMessage) string {
	var m map[string]any
	json.Unmarshal(in, &m)
	for _, k := range []string{"description", "command", "file_path", "path", "pattern", "url", "query", "prompt"} {
		if v, ok := m[k].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return clip(s, 200)
}

func reverse(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[len(ss)-1-i] = s
	}
	return out
}
