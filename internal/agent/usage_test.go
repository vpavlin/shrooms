package agent

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Who asked is who pays: turns asked for over the mesh are the asking
// device's, turns from this machine's socket are "", each with the tokens its
// result reported and what it added to the running cost — the session's first
// result only setting where that count starts.
func TestUsageIsCountedPerDevice(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("proj", t.TempDir())
	s, _ := m.Get("proj")
	who := func(a netip.Addr) string { return "phone.office" }
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, who))
	t.Cleanup(srv.Close)

	results := 0
	turn := func(send func()) {
		send()
		results++
		waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" && countResults(s) == results })
	}
	turn(func() { postJSON(t, srv.URL+"/v1/sessions/proj/messages", `{"text":"one"}`) })
	turn(func() { postJSON(t, srv.URL+"/v1/sessions/proj/messages", `{"text":"two"}`) })
	turn(func() { s.Send("from this machine", "") })

	get := func() map[string]UsageRow {
		r, err := http.Get(srv.URL + "/v1/usage")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out struct {
			Machine string
			Rows    []UsageRow
		}
		json.NewDecoder(r.Body).Decode(&out)
		if out.Machine == "" {
			t.Error("no machine named")
		}
		rows := map[string]UsageRow{}
		for _, row := range out.Rows {
			rows[row.By] = row
		}
		return rows
	}
	rows := get()
	phone, local := rows["phone.office"], rows[""]
	if phone.Turns != 2 || phone.Output != 100 || phone.CacheRead != 2000 || phone.Input != 20 || !near(phone.CostUSD, 0.01) {
		t.Errorf("the phone's: %+v", phone)
	}
	if local.Turns != 1 || local.Output != 50 || !near(local.CostUSD, 0.01) {
		t.Errorf("this machine's: %+v", local)
	}
	if phone.Session != "proj" || phone.Day != time.Now().Format("2006-01-02") || phone.Model == "" {
		t.Errorf("labels: %+v", phone)
	}
	// Read again, and after one more turn: what was counted is not counted twice.
	if again := get(); again["phone.office"].Turns != 2 {
		t.Errorf("read twice: %+v", again["phone.office"])
	}
	turn(func() { postJSON(t, srv.URL+"/v1/sessions/proj/messages", `{"text":"three"}`) })
	if after := get(); after["phone.office"].Turns != 3 || !near(after["phone.office"].CostUSD, 0.02) {
		t.Errorf("after another turn: %+v", after["phone.office"])
	}
}

func countResults(s *Session) int {
	ev, ch := s.Since(0)
	s.Unsubscribe(ch)
	n := 0
	for _, e := range ev {
		if claudeType(e) == "result/success" {
			n++
		}
	}
	return n
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The rules a log is read by, on one written out: a harness whose result has
// no tokens has its messages' summed; the first result only sets where the
// running cost starts; a turn's cost is what it took the total above its
// highest — a restart does not reset it (Claude Code carries it over) and a
// dip adds nothing; a turn left waiting for hours is not hours of work; days
// are the machine's local dates.
func TestUsageReadsALog(t *testing.T) {
	day1 := time.Date(2026, 10, 4, 23, 50, 0, 0, time.Local)
	day2 := day1.Add(20 * time.Minute) // past midnight
	var b strings.Builder
	seq := 0
	ev := func(at time.Time, kind, by, data string) {
		seq++
		e := map[string]any{"seq": seq, "time": at, "kind": kind, "by": by, "data": json.RawMessage(data)}
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	ev(day1, "claude", "", `{"type":"system","subtype":"init","model":"ollama/qwen3"}`)
	ev(day1, "message", "phone.office", `{"text":"taken over from a terminal"}`)
	ev(day1, "claude", "", `{"type":"result","subtype":"success","total_cost_usd":400,"usage":{"output_tokens":1}}`)
	ev(day1, "message", "phone.office", `{"text":"hi"}`)
	ev(day1.Add(time.Second), "claude", "", `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":7}}}`)
	ev(day1.Add(2*time.Second), "claude", "", `{"type":"assistant","message":{"usage":{"input_tokens":120,"output_tokens":9}}}`)
	ev(day1.Add(3*time.Second), "claude", "", `{"type":"result","subtype":"success","total_cost_usd":400.05}`)
	ev(day1.Add(4*time.Second), "stopped", "", `{"reason":"idle"}`)
	ev(day2, "message", "laptop.home", `{"text":"again"}`)
	ev(day2.Add(5*time.Second), "claude", "", `{"type":"result","subtype":"success","total_cost_usd":399.9,"usage":{"input_tokens":1,"output_tokens":2}}`)
	ev(day2.Add(time.Minute), "message", "laptop.home", `{"text":"left waiting"}`)
	ev(day2.Add(5*time.Hour), "claude", "", `{"type":"result","subtype":"success","total_cost_usd":400.35,"duration_ms":4000}`)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(b.String()), 0o600)

	sc := &usageScan{rows: map[usageKey]*UsageRow{}}
	sc.read(path, "s", "claude")
	got := map[string]UsageRow{}
	for _, r := range sc.rows {
		got[r.Day+" "+r.By] = *r
	}
	first := got["2026-10-04 phone.office"]
	// The first result: its turn and tokens, none of the 400 it arrived with.
	if first.Turns != 2 || first.Input != 220 || first.Output != 17 || !near(first.CostUSD, 0.05) || first.BusyMs != 3000 ||
		first.Model != "ollama/qwen3" || first.Harness != "claude" {
		t.Errorf("summed from its messages: %+v", first)
	}
	second := got["2026-10-05 laptop.home"]
	// After the restart the total dips to 399.9: nothing. Then 400.35 is
	// 0.30 above the peak of 400.05 — not 0.45 above the dip. The turn left
	// waiting five hours counts its own 4 s, not five hours.
	if second.Turns != 2 || second.Output != 2 || !near(second.CostUSD, 0.3) || second.BusyMs != 5000+4000 {
		t.Errorf("after a restart, past midnight: %+v", second)
	}

	// Only what the log gained is read again.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	b.Reset()
	ev(day2.Add(6*time.Hour), "message", "phone.office", `{"text":"later"}`)
	ev(day2.Add(6*time.Hour+time.Second), "claude", "", `{"type":"result","subtype":"success","total_cost_usd":400.45,"usage":{"output_tokens":5}}`)
	f.WriteString(b.String())
	f.Close()
	sc.read(path, "s", "claude")
	if r := sc.rows[usageKey{"2026-10-05", "phone.office", "ollama/qwen3"}]; r == nil || r.Turns != 1 || r.Output != 5 {
		t.Errorf("the appended turn: %+v", r)
	}
	if r := sc.rows[usageKey{"2026-10-04", "phone.office", "ollama/qwen3"}]; r.Turns != 2 {
		t.Errorf("counted again: %+v", r)
	}
}

// pi's running cost is its process's: every start counts from zero, so a
// process that never reaches an earlier one's total is still counted — as
// Jimmy's were not on pi5: deepseek to $0.079, then GLM to $0.046 after restarts.
func TestUsageOfPiCountsEachProcessFromZero(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	var b strings.Builder
	seq := 0
	ev := func(kind, by, data string) {
		seq++
		at = at.Add(time.Second)
		line, _ := json.Marshal(map[string]any{"seq": seq, "time": at, "kind": kind, "by": by, "data": json.RawMessage(data)})
		b.Write(line)
		b.WriteByte('\n')
	}
	ev("claude", "", `{"type":"system","subtype":"init","model":"venice/deepseek","harness":"pi"}`)
	ev("message", "laptop", `{"text":"one"}`)
	ev("claude", "", `{"type":"result","subtype":"success","total_cost_usd":0.05}`)
	ev("message", "laptop", `{"text":"two"}`)
	ev("claude", "", `{"type":"result","subtype":"success","total_cost_usd":0.08}`)
	ev("claude", "", `{"type":"system","subtype":"init","model":"venice/glm","harness":"pi"}`)
	ev("message", "laptop", `{"text":"three"}`)
	ev("claude", "", `{"type":"result","subtype":"success","total_cost_usd":0.02}`)
	ev("message", "laptop", `{"text":"four"}`)
	ev("claude", "", `{"type":"result","subtype":"success","total_cost_usd":0.045}`)
	path := filepath.Join(t.TempDir(), "jimmy.jsonl")
	os.WriteFile(path, []byte(b.String()), 0o600)

	sc := &usageScan{rows: map[usageKey]*UsageRow{}}
	sc.read(path, "jimmy", "pi")
	day := at.Format("2006-01-02")
	deep := sc.rows[usageKey{day, "laptop", "venice/deepseek"}]
	glm := sc.rows[usageKey{day, "laptop", "venice/glm"}]
	if deep == nil || !near(deep.CostUSD, 0.08) || glm == nil || !near(glm.CostUSD, 0.045) {
		t.Fatalf("deepseek %+v, glm %+v", deep, glm)
	}
}

// Where the subscription stands is Claude Code's newest report on this
// machine, whichever session it came from: both windows, with what the
// newest request was told. An older Claude Code reports only one window.
func TestUsageLimitsAreTheNewestReport(t *testing.T) {
	at := time.Date(2026, 10, 6, 18, 21, 0, 0, time.Local)
	write := func(name string, lines ...string) string {
		var b strings.Builder
		for i, l := range lines {
			e := map[string]any{"seq": i + 1, "time": at.Add(time.Duration(i) * time.Minute), "kind": "claude", "data": json.RawMessage(l)}
			line, _ := json.Marshal(e)
			b.Write(line)
			b.WriteByte('\n')
		}
		p := filepath.Join(t.TempDir(), name+".jsonl")
		os.WriteFile(p, []byte(b.String()), 0o600)
		return p
	}
	older := write("a", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.4,"resetsAt":1791314400}}`)
	newer := write("b",
		`{"type":"result","subtype":"success"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1791314400,"rateLimitType":"five_hour","utilization":0.98,"isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.98,"resetsAt":1791314400},"seven_day":{"utilization":0.49,"resetsAt":1791709200}}}}`)

	m := &Manager{}
	m.usage.scans = map[string]*usageScan{}
	for name, p := range map[string]string{"a": older, "b": newer} {
		sc := &usageScan{rows: map[usageKey]*UsageRow{}}
		sc.read(p, name, "claude")
		m.usage.scans[name] = sc
	}
	l := m.UsageLimits()
	if l == nil || l.Status != "allowed_warning" || l.Window != "five_hour" || !l.At.Equal(at.Add(time.Minute)) {
		t.Fatalf("newest: %+v", l)
	}
	fh, sd := l.Windows["five_hour"], l.Windows["seven_day"]
	if !near(fh.Utilization, 0.98) || !fh.ResetsAt.Equal(time.Unix(1791314400, 0)) || !near(sd.Utilization, 0.49) || !sd.ResetsAt.Equal(time.Unix(1791709200, 0)) {
		t.Errorf("windows: %+v", l.Windows)
	}
	one := m.usage.scans["a"].limits
	if one == nil || len(one.Windows) != 1 || !near(one.Windows["five_hour"].Utilization, 0.4) {
		t.Errorf("an older report's one window: %+v", one)
	}
}

// A reading Claude Code reports while the agent runs is kept as it comes,
// newer than what the logs held, and the session list carries it.
func TestTheSessionListCarriesTheNewestLimits(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	at := time.Now()
	m.noteLimits(at, []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","utilization":0.62,"resetsAt":1791332400,"unifiedWindows":{"five_hour":{"utilization":0.62,"resetsAt":1791332400}}}}`))
	m.noteLimits(at.Add(-time.Hour), []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","utilization":1,"resetsAt":1791314400}}`))
	srv := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), m, func(netip.Addr) string { return "" }))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Limits *Limits `json:"limits"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Limits == nil || got.Limits.Status != "allowed" || !near(got.Limits.Windows["five_hour"].Utilization, 0.62) {
		t.Fatalf("the newest reading, not an older one: %+v", got.Limits)
	}
}
