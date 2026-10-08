package agent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The session list with ?tail=N carries each session's last lines — what was
// asked and what was said — and without it, none.
func TestTheListCarriesTheLastLines(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	s.Send("hello\nsecond line", "")
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, nil))
	defer srv.Close()
	get := func(q string) []Info {
		resp, err := http.Get(srv.URL + "/v1/sessions" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var d struct{ Sessions []Info }
		json.NewDecoder(resp.Body).Decode(&d)
		return d.Sessions
	}
	got := get("?tail=5")[0].Tail
	// The message by its first line; the reply line by line.
	if strings.Join(got, "|") != "› you: hello …|echo: hello|second line" {
		t.Fatalf("tail %q", got)
	}
	if one := get("?tail=1")[0].Tail; len(one) != 1 || one[0] != "second line" {
		t.Fatalf("the newest line: %q", one)
	}
	if none := get("")[0].Tail; none != nil {
		t.Fatalf("a tail nobody asked for: %q", none)
	}
}

// What a glance shows of each kind of event: tool calls by what they do,
// a reminder as one, a task's end, and no code fences.
func TestTheLinesOfEvents(t *testing.T) {
	ev := func(kind, by, data string) Event { return Event{Kind: kind, By: by, Data: json.RawMessage(data)} }
	cases := []struct {
		e    Event
		want []string
	}{
		{ev("claude", "", "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Running:\\n```\\nmake\\n```\"},"+
			`{"type":"tool_use","name":"Bash","input":{"command":"make test","description":"Run the tests"}}]}}`),
			[]string{"Running:", "make", "▸ Bash Run the tests"}},
		{ev("message", "shrooms", `{"text":"[shrooms: a reminder]\nmore"}`), []string{"› reminder: [shrooms: a reminder] …"}},
		{ev("task", "", `{"id":"proj:m1","state":"completed","summary":"done"}`), []string{"◆ task proj:m1 completed"}},
		{ev("claude", "", `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`), nil},
	}
	for _, c := range cases {
		if got := eventLines(c.e); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: %q, want %q", c.e.Kind, got, c.want)
		}
	}
}
