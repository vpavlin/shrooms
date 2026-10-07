package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// Usage: what the model did, turn by turn, and for whom.
//
// Every turn ends in a result event with the turn's tokens, and the turn was
// asked for by a device: the message that started it carries its sender — the
// mesh peer the request came from, which WireGuard makes unforgeable, or ""
// for this machine's own socket (Basecamp here). So who uses which machine's
// model, and how much, is in the session logs already, history included; this
// reads it out. With a model on a machine of your own, it is the basis of
// sharing it fairly — or of billing for it.

// UsageRow is the usage of one day, session, device and model.
type UsageRow struct {
	Day     string `json:"day"` // the machine's local date, 2006-01-02
	Session string `json:"session"`
	By      string `json:"by"` // the device that asked; "" is this machine
	Model   string `json:"model"`
	Harness string `json:"harness"`
	Turns   int    `json:"turns"`
	// Tokens: fresh input, input read from the cache, input written to it,
	// and output (thinking included).
	Input      uint64  `json:"input"`
	CacheRead  uint64  `json:"cache_read"`
	CacheWrite uint64  `json:"cache_write"`
	Output     uint64  `json:"output"`
	CostUSD    float64 `json:"cost_usd"` // as the harness prices it; 0 for a local model
	BusyMs     int64   `json:"busy_ms"`  // from the asking to the answer
}

// Limits is where the machine's Claude subscription stands, as Claude Code
// last reported it (a rate_limit_event, after a turn): each window's share
// used and when it starts again, and whether the newest request was allowed.
// Not a documented API — Claude Code's own stream — so read defensively; and
// only as fresh as the last turn on this machine, which At says.
type Limits struct {
	At      time.Time              `json:"at"`
	Status  string                 `json:"status"`           // allowed, allowed_warning, rejected
	Window  string                 `json:"window,omitempty"` // which window the status is about
	Overage bool                   `json:"overage,omitempty"`
	Windows map[string]LimitWindow `json:"windows"` // five_hour, seven_day, …
}

// LimitWindow is one window: the share of it used (0 to 1) and when it resets.
type LimitWindow struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

type rawLimits struct {
	Status         string   `json:"status"`
	ResetsAt       int64    `json:"resetsAt"`
	RateLimitType  string   `json:"rateLimitType"`
	Utilization    *float64 `json:"utilization"`
	IsUsingOverage bool     `json:"isUsingOverage"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

func (r *rawLimits) limits(at time.Time) *Limits {
	l := &Limits{At: at, Status: r.Status, Window: r.RateLimitType, Overage: r.IsUsingOverage, Windows: map[string]LimitWindow{}}
	for name, w := range r.UnifiedWindows {
		l.Windows[name] = LimitWindow{Utilization: w.Utilization, ResetsAt: time.Unix(w.ResetsAt, 0)}
	}
	// An older Claude Code reports only the window the status is about.
	if _, ok := l.Windows[r.RateLimitType]; !ok && r.RateLimitType != "" && r.Utilization != nil {
		l.Windows[r.RateLimitType] = LimitWindow{Utilization: *r.Utilization, ResetsAt: time.Unix(r.ResetsAt, 0)}
	}
	if l.Status == "" && len(l.Windows) == 0 {
		return nil
	}
	return l
}

type usageKey struct{ day, by, model string }

// usageScan reads one session's log as it grows: only what was added since.
type usageScan struct {
	offset int64
	rows   map[usageKey]*UsageRow
	limits *Limits // the newest the log holds
	turnState
}

// turnState is what a scan carries between lines.
type turnState struct {
	by       string    // the sender of the latest message
	model    string    // from the latest init
	started  time.Time // when the turn was asked for; zero between turns
	peakCost float64   // the highest running cost seen
	costBase bool      // peakCost is known: a result has been seen
	pending  tokens    // the turn's tokens from its messages, for a result without its own
}

type tokens struct{ input, cacheRead, cacheWrite, output uint64 }

type usageCache struct {
	mu    sync.Mutex
	scans map[string]*usageScan // by session
}

// maxTurn bounds what counts as one turn's busy time: a "turn" whose result
// came this long after its message was a session left waiting — on a prompt
// nobody answered, say — not a model at work.
const maxTurn = 2 * time.Hour

// Usage is every session's usage from since (a local date, "" for all).
func (m *Manager) Usage(since string) []UsageRow {
	m.usage.mu.Lock()
	defer m.usage.mu.Unlock()
	if m.usage.scans == nil {
		m.usage.scans = map[string]*usageScan{}
	}
	out := []UsageRow{} // [] rather than null for a machine with nothing yet
	for _, info := range m.List() {
		s, ok := m.Get(info.Name)
		if !ok {
			continue
		}
		sc := m.usage.scans[info.Name]
		if sc == nil {
			sc = &usageScan{rows: map[usageKey]*UsageRow{}}
			m.usage.scans[info.Name] = sc
		}
		sc.read(s.eventsPath(), info.Name, info.Harness)
		for _, r := range sc.rows {
			if since == "" || r.Day >= since {
				out = append(out, *r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		return out[i].By < out[j].By
	})
	return out
}

// noteLimits keeps a reading as Claude Code reports it.
func (m *Manager) noteLimits(at time.Time, raw []byte) {
	var d struct {
		RateLimitInfo *rawLimits `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &d) != nil || d.RateLimitInfo == nil {
		return
	}
	l := d.RateLimitInfo.limits(at)
	if l == nil {
		return
	}
	m.live.Lock()
	if m.live.l == nil || !l.At.Before(m.live.l.At) {
		m.live.l = l
	}
	m.live.Unlock()
}

// Limits is the newest reading this machine has: one reported since it
// started, or what its logs held (read by Usage, which the agent runs once
// in the background at startup so a restart does not forget).
func (m *Manager) Limits() *Limits {
	m.live.Lock()
	l := m.live.l
	m.live.Unlock()
	if logged := m.UsageLimits(); logged != nil && (l == nil || logged.At.After(l.At)) {
		return logged
	}
	return l
}

// UsageLimits is the newest subscription reading across this machine's
// sessions, from what Usage last read; nil when Claude Code never reported one.
func (m *Manager) UsageLimits() *Limits {
	m.usage.mu.Lock()
	defer m.usage.mu.Unlock()
	var newest *Limits
	for _, sc := range m.usage.scans {
		if sc.limits != nil && (newest == nil || sc.limits.At.After(newest.At)) {
			newest = sc.limits
		}
	}
	return newest
}

// read takes in what the log gained since the last read. A log shorter than
// what was read is a session made again: read from the start.
func (sc *usageScan) read(path, session, harness string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < sc.offset {
		*sc = usageScan{rows: map[usageKey]*UsageRow{}}
	}
	if _, err := f.Seek(sc.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // a partial last line is read again next time
		}
		sc.offset += int64(len(line))
		var e Event
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		sc.event(e, session, harness)
	}
}

func (sc *usageScan) event(e Event, session, harness string) {
	switch e.Kind {
	case "message":
		sc.by = e.By
		if sc.started.IsZero() {
			sc.started = e.Time
		}
	case "stopped":
		// Not the running cost: Claude Code carries a conversation's cost
		// across a restart that resumes it (475.43, restarted, 479.39).
		sc.started = time.Time{}
		sc.pending = tokens{}
	case "claude":
		var d struct {
			Type    string
			Subtype string
			Model   string
			Message struct {
				Usage *rawUsage
			}
			Usage         *rawUsage
			TotalCostUSD  *float64   `json:"total_cost_usd"`
			DurationMs    int64      `json:"duration_ms"`
			RateLimitInfo *rawLimits `json:"rate_limit_info"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			return
		}
		switch d.Type {
		case "rate_limit_event":
			if d.RateLimitInfo != nil {
				if l := d.RateLimitInfo.limits(e.Time); l != nil {
					sc.limits = l
				}
			}
		case "system":
			if d.Subtype == "init" && d.Model != "" {
				sc.model = d.Model
			}
			if d.Subtype == "init" && harness == "pi" {
				// pi's total is the process's, from nothing: each start of
				// one counts from zero, whatever an earlier one reached.
				sc.peakCost, sc.costBase = 0, true
			}
		case "assistant":
			// Summed only for a harness whose result does not carry the turn's
			// tokens; Claude Code repeats a message's usage on each of its
			// content blocks, so its result is the one to believe.
			if u := d.Message.Usage; u != nil {
				sc.pending.add(u.tokens())
			}
		case "result":
			t := sc.pending
			if d.Usage != nil {
				t = d.Usage.tokens()
			}
			cost := 0.0
			if d.TotalCostUSD != nil {
				// The figure is a running total, for the conversation (Claude
				// Code, across restarts) or the process (pi): a turn's cost is
				// what it took the total above its highest so far. Not simply
				// above the last: Claude Code's total dips on a resume and
				// climbs back (419.25, 418.10, 414.93, … 433.50), and counting
				// the climb counted every dip twice — $6,600 from logs whose
				// totals rose by $131 (2026-10-05). A pi process's counter
				// truly starts again, so its init resets the peak (above):
				// held to the old one, Jimmy's turns on Venice after restarts
				// counted $0 under an earlier process's $0.079 (2026-10-07).
				//
				// The log's first result only sets the peak: a conversation
				// taken over from a terminal arrives with its whole past cost
				// on the counter, and charging that to whoever asked first
				// would be the larger error.
				c := *d.TotalCostUSD
				if sc.costBase && c > sc.peakCost {
					cost = c - sc.peakCost
				}
				if !sc.costBase || c > sc.peakCost {
					sc.peakCost = c
				}
				sc.costBase = true
			}
			busy := int64(0)
			if !sc.started.IsZero() && e.Time.Sub(sc.started) >= 0 && e.Time.Sub(sc.started) < maxTurn {
				busy = e.Time.Sub(sc.started).Milliseconds()
			} else if d.DurationMs > 0 && d.DurationMs < maxTurn.Milliseconds() {
				busy = d.DurationMs
			}
			k := usageKey{day: e.Time.Local().Format("2006-01-02"), by: sc.by, model: sc.model}
			row := sc.rows[k]
			if row == nil {
				row = &UsageRow{Day: k.day, Session: session, By: k.by, Model: k.model, Harness: harness}
				sc.rows[k] = row
			}
			row.Turns++
			row.Input += t.input
			row.CacheRead += t.cacheRead
			row.CacheWrite += t.cacheWrite
			row.Output += t.output
			row.CostUSD += cost
			row.BusyMs += busy
			sc.started = time.Time{}
			sc.pending = tokens{}
		}
	}
}

type rawUsage struct {
	Input      uint64 `json:"input_tokens"`
	CacheRead  uint64 `json:"cache_read_input_tokens"`
	CacheWrite uint64 `json:"cache_creation_input_tokens"`
	Output     uint64 `json:"output_tokens"`
}

func (u *rawUsage) tokens() tokens {
	return tokens{input: u.Input, cacheRead: u.CacheRead, cacheWrite: u.CacheWrite, output: u.Output}
}

func (t *tokens) add(o tokens) {
	t.input += o.input
	t.cacheRead += o.cacheRead
	t.cacheWrite += o.cacheWrite
	t.output += o.output
}
