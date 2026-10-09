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

// An A2A client on another machine (pi5), and this machine's own sessions
// (the address names nobody), against one agent.
type a2aRig struct {
	m             *Manager
	remote, local *httptest.Server
}

func newA2A(t *testing.T) a2aRig {
	t.Helper()
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	remote := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, func(netip.Addr) string { return "pi5.default" }))
	local := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, func(netip.Addr) string { return "" }))
	t.Cleanup(remote.Close)
	t.Cleanup(local.Close)
	return a2aRig{m, remote, local}
}

type rpcResp struct {
	Result struct {
		Task  a2aTask   `json:"task"`
		Tasks []a2aTask `json:"tasks"`
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

// update is the worker's task_update, as its MCP tool sends it.
func update(t *testing.T, url, id, state, summary string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"state": state, "summary": summary, "session": "proj"})
	resp, err := http.Post(url+"/v1/tasks/"+id, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func waitTurn(t *testing.T, s *Session, after uint64) Event {
	t.Helper()
	return waitFor(t, s, after, func(e Event) bool { return strings.HasPrefix(claudeType(e), "result/") })
}

// The cards: the machine lists its sessions, each with a card of its own,
// and both declare the task extension.
func TestA2ACards(t *testing.T) {
	r := newA2A(t)
	var machine struct {
		Skills       []struct{ ID, Description string }
		Capabilities struct{ Extensions []struct{ URI string } }
	}
	resp, _ := http.Get(r.remote.URL + "/.well-known/agent-card.json")
	json.NewDecoder(resp.Body).Decode(&machine)
	resp.Body.Close()
	if len(machine.Skills) != 1 || machine.Skills[0].ID != "proj" ||
		!strings.Contains(machine.Skills[0].Description, "/a2a/proj/.well-known/agent-card.json") ||
		len(machine.Capabilities.Extensions) != 1 || machine.Capabilities.Extensions[0].URI != taskExtension {
		t.Fatalf("machine card %+v", machine)
	}
	var card struct {
		Name                string
		SupportedInterfaces []struct{ URL, ProtocolBinding string }
		Capabilities        struct{ Streaming bool }
	}
	resp, _ = http.Get(r.remote.URL + "/a2a/proj/.well-known/agent-card.json")
	json.NewDecoder(resp.Body).Decode(&card)
	resp.Body.Close()
	if !strings.HasSuffix(card.Name, "/proj") || card.SupportedInterfaces[0].URL != r.remote.URL+"/a2a/proj" || !card.Capabilities.Streaming {
		t.Fatalf("session card %+v", card)
	}
}

// A task outlives its turn: the session answers and goes idle, and the task
// is still working — until the worker says it is done. Only this machine's
// sessions can say so. The asker then acknowledges it. A messageId seen
// before is the same task, not a second one.
func TestA2ATaskLastsUntilTheWorkerFinishesIt(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	got := call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-1", "hello", false))
	if got.Error != nil || got.Result.Task.ID != "proj:m-1" || got.Result.Task.Status.State != taskWorking {
		t.Fatalf("send: %+v %+v", got.Result.Task, got.Error)
	}
	msg := waitFor(t, s, 0, func(e Event) bool { return e.Kind == "message" })
	var d struct{ Text string }
	json.Unmarshal(msg.Data, &d)
	if msg.By != "pi5.default (pi5/jimmy)" || !strings.Contains(d.Text, "[shrooms task proj:m-1 from pi5.default (pi5/jimmy)]") ||
		!strings.Contains(d.Text, "task_update") {
		t.Fatalf("sent %q as from %q", d.Text, msg.By)
	}
	waitTurn(t, s, msg.Seq)
	g := call(t, r.remote.URL+"/a2a", "GetTask", map[string]any{"id": "proj:m-1"})
	if g.Result.Task.Status.State != taskWorking || !strings.Contains(statusText(g.Result.Task), "echo:") {
		t.Fatalf("after the turn: %+v", g.Result.Task)
	}
	if code := update(t, r.remote.URL, "proj:m-1", "done", "from elsewhere"); code != http.StatusForbidden {
		t.Errorf("another machine updated a task: %d", code)
	}
	if code := update(t, r.local.URL, "proj:m-1", "done", "said hello back"); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	g = call(t, r.remote.URL+"/a2a", "GetTask", map[string]any{"id": "proj:m-1"})
	if g.Result.Task.Status.State != taskCompleted || statusText(g.Result.Task) != "said hello back" || len(g.Result.Task.Artifacts) != 1 {
		t.Fatalf("done: %+v", g.Result.Task)
	}
	// What was asked stays with it, as A2A's history: the answer alone does
	// not say what the task was.
	if h := g.Result.Task.History; len(h) != 1 || h[0].Role != "ROLE_USER" || len(h[0].Parts) != 1 || h[0].Parts[0].Text != "hello" {
		t.Errorf("history: %+v", h)
	}
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "task" && strings.Contains(string(e.Data), `"completed"`) })
	a := call(t, r.remote.URL+"/a2a", "AckTask", map[string]any{"id": "proj:m-1"})
	if a.Result.Task.Metadata["shrooms/acknowledged"] != true {
		t.Errorf("ack: %+v", a.Result.Task.Metadata)
	}
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-1", "hello", false))
	if n := len(r.m.Tasks("proj")); n != 1 {
		t.Errorf("the same messageId made %d tasks", n)
	}
	if code := update(t, r.local.URL, "proj:m-1", "done", "again"); code != http.StatusConflict {
		t.Errorf("a finished task updated again: %d", code)
	}
	l := call(t, r.remote.URL+"/a2a/proj", "ListTasks", map[string]any{})
	if len(l.Result.Tasks) != 1 {
		t.Errorf("list: %+v", l.Result.Tasks)
	}
}

// A busy session queues a task instead of turning it away, and starts it
// when the task before it is finished; a cancelled one is interrupted.
func TestA2ATasksQueue(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("slow-1", "slow", false))
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "claude" && claudeType(e) == "system/init" })
	q := call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("q-1", "hello", false))
	if q.Result.Task.Status.State != taskSubmitted || q.Result.Task.Metadata["shrooms/queued"] != true {
		t.Fatalf("not queued: %+v", q.Result.Task)
	}
	c := call(t, r.remote.URL+"/a2a/proj", "CancelTask", map[string]any{"id": "proj:slow-1"})
	if c.Error != nil || c.Result.Task.Status.State != taskCanceled {
		t.Fatalf("cancel: %+v %+v", c.Result.Task, c.Error)
	}
	waitTurn(t, s, 0)
	r.m.superviseOnce(time.Now())
	waitFor(t, s, 0, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "[shrooms task proj:q-1")
	})
	if got, _ := r.m.tasks.get("proj:q-1"); got.State != taskWorking {
		t.Fatalf("the queued task: %+v", got)
	}
	if r := call(t, r.remote.URL+"/a2a/proj", "CancelTask", map[string]any{"id": "proj:slow-1"}); r.Error == nil {
		t.Error("cancelled twice")
	}
	// Idle between turns is not free: q-1 is still open, so q-2 waits for
	// it to be finished, not for the turn to end.
	waitTurn(t, s, waitFor(t, s, 0, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "[shrooms task proj:q-1")
	}).Seq)
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("q-2", "hello", false))
	r.m.superviseOnce(time.Now())
	if got, _ := r.m.tasks.get("proj:q-2"); got.State != taskSubmitted {
		t.Fatalf("started beside an open task: %+v", got)
	}
	update(t, r.local.URL, "proj:q-1", "done", "ok")
	if got, _ := r.m.tasks.get("proj:q-2"); got.State != taskWorking {
		t.Fatalf("not started once the one before was done: %+v", got)
	}
}

// Blocked is a question for the asker; its answer, sent to the task, goes to
// the session and the task works again.
func TestA2ABlockedAndAnswered(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("b-1", "hello", false))
	first := waitTurn(t, s, 0)
	update(t, r.local.URL, "proj:b-1", "blocked", "which branch?")
	g := call(t, r.remote.URL+"/a2a", "GetTask", map[string]any{"id": "proj:b-1"})
	if g.Result.Task.Status.State != taskInputRequired || statusText(g.Result.Task) != "which branch?" {
		t.Fatalf("blocked: %+v", g.Result.Task)
	}
	ans := send("b-2", "master", false)
	ans["message"].(map[string]any)["taskId"] = "proj:b-1"
	a := call(t, r.remote.URL+"/a2a/proj", "SendMessage", ans)
	if a.Error != nil || a.Result.Task.ID != "proj:b-1" || a.Result.Task.Status.State != taskWorking {
		t.Fatalf("answer: %+v %+v", a.Result.Task, a.Error)
	}
	waitFor(t, s, first.Seq, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "more from") && strings.Contains(string(e.Data), "master")
	})
	if n := len(r.m.Tasks("proj")); n != 1 {
		t.Errorf("an answer made a task of its own: %d", n)
	}
}

// The watchdog: a session quiet with a task open is reminded — once per
// step of the ladder, with what was asked and its last words, by a
// reminder that is not a person — and after the last step the task is
// stalled. Never while it works; never more than its hourly cap; not after
// its owner stopped it, until someone writes to it again.
func TestTheWatchdogRemindsAQuietWorker(t *testing.T) {
	defer func(a map[string]time.Duration, l []time.Duration, n int) {
		nudgeAfter, nudgeLadder, nudgesPerHour = a, l, n
	}(nudgeAfter, nudgeLadder, nudgesPerHour)
	nudgeAfter = map[string]time.Duration{"claude": time.Minute}
	nudgeLadder = []time.Duration{time.Minute, time.Minute}
	nudgesPerHour = 10
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("w-1", "write the probe", false))
	turn := waitTurn(t, s, 0)

	r.m.superviseOnce(time.Now()) // quiet for less than a minute: nothing
	if got, _ := r.m.tasks.get("proj:w-1"); got.Nudges != 0 {
		t.Fatal("reminded a session that only just went quiet")
	}
	later := time.Now().Add(2 * time.Minute)
	r.m.superviseOnce(later)
	n := waitFor(t, s, turn.Seq, func(e Event) bool { return e.Kind == "message" && e.By == "shrooms" })
	var d struct {
		Text    string
		Outside bool
		Nudge   []string
	}
	json.Unmarshal(n.Data, &d)
	if !d.Outside || len(d.Nudge) != 1 || d.Nudge[0] != "proj:w-1" || !strings.Contains(d.Text, "write the probe") ||
		!strings.Contains(d.Text, "Last you said") || !strings.Contains(d.Text, "a reminder, not a person") {
		t.Fatalf("the reminder %+v", d)
	}
	turn = waitTurn(t, s, n.Seq)
	r.m.superviseOnce(later.Add(30 * time.Second)) // before the next step of the ladder
	if got, _ := r.m.tasks.get("proj:w-1"); got.Nudges != 1 {
		t.Fatalf("reminded again before the ladder said: %d", got.Nudges)
	}
	r.m.superviseOnce(later.Add(3 * time.Minute))
	turn = waitTurn(t, s, waitFor(t, s, turn.Seq, func(e Event) bool { return e.Kind == "message" && e.By == "shrooms" }).Seq)
	r.m.superviseOnce(later.Add(6 * time.Minute))
	waitFor(t, s, turn.Seq, func(e Event) bool { return e.Kind == "task" && strings.Contains(string(e.Data), `"stalled"`) })
	got, _ := r.m.tasks.get("proj:w-1")
	if !got.Stalled || got.Nudges != 2 {
		t.Fatalf("after the ladder: %+v", got)
	}
	if in := r.m.List()[0]; in.TasksOpen != 1 || in.TasksStalled != 1 {
		t.Errorf("the session list: %+v", in)
	}

	// The owner stops the session: no reminders, until someone writes.
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("w-2", "another", false))
	update(t, r.local.URL, "proj:w-1", "failed", "gave up")
	r.m.superviseOnce(time.Now())
	waitTurn(t, s, waitFor(t, s, 0, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "[shrooms task proj:w-2")
	}).Seq)
	r.m.pauseTasks("proj")
	r.m.superviseOnce(time.Now().Add(time.Hour))
	if got, _ := r.m.tasks.get("proj:w-2"); got.Nudges != 0 {
		t.Fatal("reminded a session its owner stopped")
	}
	s.Send("go on", "laptop")
	waitTurn(t, s, waitFor(t, s, 0, func(e Event) bool { return e.Kind == "message" && e.By == "laptop" }).Seq)
	r.m.superviseOnce(time.Now().Add(time.Hour))
	if got, _ := r.m.tasks.get("proj:w-2"); got.Nudges != 1 {
		t.Fatalf("not reminded once written to again: %+v", got)
	}
}

// The hourly cap holds however many tasks a session has.
func TestRemindersHaveAnHourlyCap(t *testing.T) {
	var r nudgeRate
	defer func(n int) { nudgesPerHour = n }(nudgesPerHour)
	nudgesPerHour = 2
	now := time.Now()
	for i := 0; i < 2; i++ {
		if !r.allow("proj", now) {
			t.Fatalf("refused at %d", i)
		}
		r.note("proj", now)
	}
	if r.allow("proj", now) || !r.allow("other", now) || !r.allow("proj", now.Add(61*time.Minute)) {
		t.Error("the cap is wrong")
	}
}

// A task open a day expires; tasks are kept across a restart of the agent,
// and follow their session when it is renamed.
func TestTasksExpireSurviveARestartAndFollowARename(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("e-1", "hello", false))
	waitTurn(t, s, 0)
	m2, err := NewManager(r.m.ctx, slog.New(slog.DiscardHandler), r.m.dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m2.tasks.get("proj:e-1"); !ok || got.State != taskWorking {
		t.Fatalf("after a restart: %+v %v", got, ok)
	}
	if _, err := r.m.Rename("proj", "probe", "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.m.tasks.get("probe:e-1"); !ok {
		t.Fatal("the task did not follow the rename")
	}
	s.mu.Lock()
	s.events = append(s.events, Event{Kind: "message", Time: time.Now()})
	s.mu.Unlock()
	r.m.superviseOnce(time.Now().Add(25 * time.Hour))
	got, _ := r.m.tasks.get("probe:e-1")
	if got.State != taskCanceled || !got.Expired {
		t.Fatalf("after a day: %+v", got)
	}
}

// Streamed: the task first, then its status as it changes, the last final —
// when the worker finishes it, not when its turn ends.
func TestA2AStreams(t *testing.T) {
	r := newA2A(t)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendStreamingMessage", "params": send("s-1", "hello", false)})
	resp, err := http.Post(r.remote.URL+"/a2a/proj", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	s, _ := r.m.Get("proj")
	go func() {
		waitTurn(t, s, 0)
		update(t, r.local.URL, "proj:s-1", "done", "said hello")
	}()
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
	if !last.Final || last.Status.State != taskCompleted || last.Status.Message.Parts[0].Text != "said hello" {
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

// An asker's title names the task in lists: kept on one line and short, and
// returned with the task. A task without one has none, not a made-up one.
func TestA2ATaskKeepsTheAskersTitle(t *testing.T) {
	r := newA2A(t)
	p := send("m-t", "From pi5/jimmy. A long request\nthat goes on.", false)
	p["message"].(map[string]any)["metadata"].(map[string]any)["shrooms/title"] = "  Review notes\n on the board  "
	got := call(t, r.remote.URL+"/a2a/proj", "SendMessage", p)
	if got.Error != nil || got.Result.Task.Metadata["shrooms/title"] != "Review notes on the board" {
		t.Fatalf("send: %+v %+v", got.Result.Task.Metadata, got.Error)
	}
	if ts := r.m.Tasks("proj"); len(ts) != 1 || ts[0].Title != "Review notes on the board" {
		t.Errorf("stored: %+v", ts)
	}
	got = call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-u", "no title here", false))
	if _, has := got.Result.Task.Metadata["shrooms/title"]; has {
		t.Errorf("a title nobody gave: %v", got.Result.Task.Metadata["shrooms/title"])
	}
	if long := taskTitle(strings.Repeat("word ", 60)); len([]rune(long)) > 121 {
		t.Errorf("not cut: %d runes", len([]rune(long)))
	}
}
