package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// An A2A client on another machine, as the agent sees it: every request from
// pi5.
func newA2A(t *testing.T) (*Manager, *httptest.Server) {
	t.Helper()
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	who := func(netip.Addr) string { return "pi5.default" }
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, who))
	t.Cleanup(srv.Close)
	return m, srv
}

type rpcResp struct {
	Result struct {
		Task a2aTask `json:"task"`
	} `json:"result"`
	Error *rpcError `json:"error"`
}

func call(t *testing.T, url, method string, params any) rpcResp {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func send(id, text string, blocking bool) map[string]any {
	return map[string]any{
		"message": map[string]any{"messageId": id, "role": "ROLE_USER", "parts": []any{map[string]any{"text": text}},
			"metadata": map[string]any{"shrooms/from": "pi5/jimmy"}},
		"configuration": map[string]any{"blocking": blocking},
	}
}

// The cards: the machine lists its sessions, each with a card of its own
// whose interface is where to send it messages.
func TestA2ACards(t *testing.T) {
	_, srv := newA2A(t)
	var machine struct {
		Skills []struct{ ID, Description string }
	}
	resp, _ := http.Get(srv.URL + "/.well-known/agent-card.json")
	json.NewDecoder(resp.Body).Decode(&machine)
	resp.Body.Close()
	if len(machine.Skills) != 1 || machine.Skills[0].ID != "proj" ||
		!strings.Contains(machine.Skills[0].Description, "/a2a/proj/.well-known/agent-card.json") {
		t.Fatalf("machine card skills %+v", machine.Skills)
	}
	var card struct {
		Name                string
		SupportedInterfaces []struct{ URL, ProtocolBinding string }
		Capabilities        struct{ Streaming bool }
	}
	resp, _ = http.Get(srv.URL + "/a2a/proj/.well-known/agent-card.json")
	json.NewDecoder(resp.Body).Decode(&card)
	resp.Body.Close()
	if !strings.HasSuffix(card.Name, "/proj") || len(card.SupportedInterfaces) != 1 ||
		card.SupportedInterfaces[0].URL != srv.URL+"/a2a/proj" || card.SupportedInterfaces[0].ProtocolBinding != "JSONRPC" ||
		!card.Capabilities.Streaming {
		t.Fatalf("session card %+v", card)
	}
}

// A message is a turn: answered, when blocking, with the turn's reply; sent
// as from the device the mesh says, with the session it claims; found again
// by its id alone; and not sent twice.
func TestA2ASendMessageIsATurn(t *testing.T) {
	m, srv := newA2A(t)
	r := call(t, srv.URL+"/a2a/proj", "SendMessage", send("m-1", "hello", true))
	if r.Error != nil || r.Result.Task.Status.State != taskCompleted || r.Result.Task.ID != "proj:m-1" ||
		len(r.Result.Task.Artifacts) != 1 || r.Result.Task.Artifacts[0].Parts[0].Text != "echo: hello" {
		t.Fatalf("send: %+v %+v", r.Result.Task, r.Error)
	}
	s, _ := m.Get("proj")
	msg := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "message" })
	if msg.By != "pi5.default (pi5/jimmy)" {
		t.Errorf("sent as from %q", msg.By)
	}
	g := call(t, srv.URL+"/a2a", "GetTask", map[string]any{"id": "proj:m-1"})
	if g.Error != nil || g.Result.Task.Status.State != taskCompleted || g.Result.Task.Status.Message.Parts[0].Text != "echo: hello" {
		t.Fatalf("get: %+v %+v", g.Result.Task, g.Error)
	}
	call(t, srv.URL+"/a2a/proj", "SendMessage", send("m-1", "hello", true))
	n := 0
	backlog, ch := s.Since(0)
	s.Unsubscribe(ch)
	for _, e := range backlog {
		if e.Kind == "message" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the same messageId was sent %d times", n)
	}
	if r := call(t, srv.URL+"/a2a", "GetTask", map[string]any{"id": "proj:nope"}); r.Error == nil || r.Error.Code != a2aTaskNotFound {
		t.Errorf("an unknown task: %+v", r)
	}
}

// One turn at a time: a message to a busy session is rejected, not folded
// into the turn running; a task is cancelled by interrupting its turn; a
// turn stopped at a permission prompt needs input.
func TestA2ABusyCancelAndInput(t *testing.T) {
	_, srv := newA2A(t)
	r := call(t, srv.URL+"/a2a/proj", "SendMessage", send("slow-1", "slow", false))
	if r.Error != nil || terminal(r.Result.Task.Status.State) {
		t.Fatalf("slow: %+v %+v", r.Result.Task, r.Error)
	}
	time.Sleep(200 * time.Millisecond)
	if b := call(t, srv.URL+"/a2a/proj", "SendMessage", send("b-1", "hello", false)); b.Result.Task.Status.State != taskRejected {
		t.Errorf("sent to a busy session: %+v", b.Result.Task)
	}
	c := call(t, srv.URL+"/a2a/proj", "CancelTask", map[string]any{"id": "proj:slow-1"})
	if c.Error != nil || c.Result.Task.Status.State != taskCanceled {
		t.Fatalf("cancel: %+v %+v", c.Result.Task, c.Error)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g := call(t, srv.URL+"/a2a", "GetTask", map[string]any{"id": "proj:slow-1"})
		if g.Result.Task.Status.State == taskCanceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after cancel: %+v", g.Result.Task)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r := call(t, srv.URL+"/a2a/proj", "CancelTask", map[string]any{"id": "proj:slow-1"}); r.Error == nil || r.Error.Code != a2aNotCancelable {
		t.Errorf("cancelled twice: %+v", r)
	}
	in := call(t, srv.URL+"/a2a/proj", "SendMessage", send("ask-1", "run ls", true))
	if in.Result.Task.Status.State != taskInputRequired {
		t.Errorf("a prompt waiting: %+v", in.Result.Task)
	}
}

// Streamed: the task first, then its status as it changes, the last final.
func TestA2AStreams(t *testing.T) {
	_, srv := newA2A(t)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendStreamingMessage", "params": send("s-1", "hello", false)})
	resp, err := http.Post(srv.URL+"/a2a/proj", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []map[string]json.RawMessage
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var r struct{ Result map[string]json.RawMessage }
		json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r)
		got = append(got, r.Result)
	}
	if len(got) < 2 || got[0]["task"] == nil {
		t.Fatalf("stream %v", got)
	}
	var last struct {
		Final  bool
		Status a2aStatus
	}
	json.Unmarshal(got[len(got)-1]["statusUpdate"], &last)
	if !last.Final || last.Status.State != taskCompleted || last.Status.Message.Parts[0].Text != "echo: hello" {
		t.Fatalf("last %+v", last)
	}
}

// A sender is held to a number of messages an hour per session, so two
// agents answering each other stop.
func TestA2ALimitsASender(t *testing.T) {
	var l a2aLimiter
	now := time.Now()
	for i := 0; i < a2aLimit; i++ {
		if !l.allow("proj|pi5", now) {
			t.Fatalf("refused at %d", i)
		}
	}
	if l.allow("proj|pi5", now) {
		t.Error("over the limit was allowed")
	}
	if !l.allow("proj|atlas", now) || !l.allow("proj|pi5", now.Add(61*time.Minute)) {
		t.Error("another sender, or an hour later, was refused")
	}
}
