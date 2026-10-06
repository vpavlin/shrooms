package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one numbered entry in a session's history. A phone that was away
// asks for everything after the last number it saw.
type Event struct {
	Seq  uint64    `json:"seq"`
	Time time.Time `json:"time"`
	// Kind is one of:
	//   claude   a message from Claude Code, verbatim in Data
	//   message  a user turn sent from a device: {"text"}
	//   answer   a permission prompt answered: {"prompt","allow","message"}
	//   stopped  the process ended: {"reason"}
	//   partial  reply text as it is written: {"text"}. Live only — never
	//            kept or numbered (its Seq is the last real event's), since
	//            the whole message follows as a claude event.
	Kind string `json:"kind"`
	// By is the device that caused it, for message and answer.
	By   string          `json:"by,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// State is what a session is doing.
type State string

const (
	Idle    State = "idle"    // nothing running, or a turn has finished
	Working State = "working" // a turn is in progress
	Waiting State = "waiting" // a permission prompt needs an answer
)

// Info is a session as the list shows it.
type Info struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	State    State     `json:"state"`
	Pending  int       `json:"pending"`
	Running  bool      `json:"running"`
	LastSeq  uint64    `json:"last_seq"`
	LastTime time.Time `json:"last_time,omitempty"`

	// AutoApprove: nothing asks, as with --dangerously-skip-permissions.
	AutoApprove bool `json:"auto_approve"`
	// Context is how much of the model's context window the conversation
	// fills, as of the last reply: what decides when it will be compacted.
	ContextUsed   uint64 `json:"context_used,omitempty"`
	ContextWindow uint64 `json:"context_window,omitempty"`
	// Preview is the start of the last thing the model said.
	Preview string `json:"preview,omitempty"`
	// Model is the one the conversation runs on, as its harness names it.
	Model string `json:"model,omitempty"`
	// Harness runs it ("claude", "pi"), and what that harness can do.
	Harness string `json:"harness"`
	Caps    Caps   `json:"caps"`
	// Starred sessions are listed first, above every machine's others. Kept
	// here, not in an app, so a star set on the phone shows in Basecamp.
	Starred bool `json:"starred,omitempty"`
	// Turns counts the turns that have ended: what the phone notifies on,
	// once each. The event count is no use for that — a session sends
	// progress, heartbeats and thinking while it waits on background work.
	Turns uint64 `json:"turns"`
}

// Session is one conversation in one directory.
type Session struct {
	name, dir string
	m         *Manager
	harness   Harness

	mu sync.Mutex
	// convID is the harness's own id for the conversation, to resume it by:
	// the session_id of its init message.
	convID   string
	events   []Event // in memory: the tail; the whole history is on disk
	seq      uint64
	subs     map[chan Event]struct{}
	pending  map[string]json.RawMessage // request_id → the can_use_tool request
	state    State
	proc     *proc
	lastUsed time.Time

	autoApprove bool
	starred     bool
	turns       uint64
	// ids are the device-made ids of messages and voice notes already taken,
	// so a device that sends again — it did not hear the answer, the
	// network went — does not send twice. The last maxIDs, loaded from the
	// log at start.
	ids     map[string]bool
	idOrder []string
	// voices are the voice notes taken, by id: where each is kept, and
	// whether it failed — what RetryVoice needs, and a failed one has.
	voices map[string]voiceNote
	// queued are the turns taken from devices that wait behind a voice note
	// still being transcribed, in the order they arrived: a message typed
	// after recording one is sent after it.
	queued    []*queuedTurn
	ctxUsed   uint64
	ctxWindow uint64
	preview   string
	model     string
}

// memoryEvents bounds what a session keeps in memory. Older events are on
// disk and served from there.
const memoryEvents = 2000

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Manager owns every session on this machine.
type Manager struct {
	log *slog.Logger
	dir string // state: sessions.json and one event log per session
	// usage is what the session logs say the model did, read incrementally
	// (Usage).
	usage usageCache

	// harnesses this machine can run sessions of, by name, and the program
	// each is run as. Claude Code always; others when Register finds them.
	harnesses map[string]Harness
	bins      map[string]string

	// IdleStop is how long a session's process may sit with nothing to do
	// before it is stopped. The conversation is kept and resumed by id.
	IdleStop time.Duration

	// STT transcribes voice notes; nil when this machine has no model.
	STT *Transcriber

	ctx context.Context

	mu       sync.Mutex
	sessions map[string]*Session
}

type record struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Harness is empty for Claude Code, which every session was before
	// there were others.
	Harness string `json:"harness,omitempty"`
	// ConvID is the harness's conversation id. Named for Claude Code, the
	// only harness when the registry was first written.
	ConvID      string `json:"claude_id,omitempty"`
	AutoApprove bool   `json:"auto_approve,omitempty"`
	Starred     bool   `json:"starred,omitempty"`
}

// NewManager loads the sessions kept in stateDir.
func NewManager(ctx context.Context, log *slog.Logger, stateDir, claudeBin string) (*Manager, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "events"), 0o700); err != nil {
		return nil, err
	}
	m := &Manager{log: log, dir: stateDir, IdleStop: 30 * time.Minute,
		harnesses: map[string]Harness{}, bins: map[string]string{},
		ctx: ctx, sessions: map[string]*Session{}}
	m.Register(Claude{}, claudeBin)
	b, err := os.ReadFile(m.registryPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var recs []record
		if err := json.Unmarshal(b, &recs); err != nil {
			return nil, fmt.Errorf("%s: %w", m.registryPath(), err)
		}
		for _, r := range recs {
			h, ok := m.harnesses[r.Harness]
			if r.Harness == "" {
				h, ok = Claude{}, true
			}
			if !ok {
				// Registered later, when its program is found; until then the
				// session is listed and its history readable, and starting it
				// says what is missing.
				h = missingHarness{name: r.Harness}
			}
			s := m.newSession(r.Name, r.Dir, h)
			s.convID = r.ConvID
			s.autoApprove = r.AutoApprove
			s.starred = r.Starred
			s.loadEvents()
			m.sessions[r.Name] = s
		}
	}
	go m.reap()
	return m, nil
}

func (m *Manager) registryPath() string { return filepath.Join(m.dir, "sessions.json") }

// Register adds a harness this machine can run sessions of, run as bin.
// Sessions of it loaded before it was registered take it up.
func (m *Manager) Register(h Harness, bin string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.harnesses[h.Name()] = h
	m.bins[h.Name()] = bin
	for _, s := range m.sessions {
		s.mu.Lock()
		if mh, ok := s.harness.(missingHarness); ok && mh.name == h.Name() {
			s.harness = h
		}
		s.mu.Unlock()
	}
}

// HarnessInfo is a harness as the apps are told of it.
type HarnessInfo struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Caps  Caps   `json:"caps"`
}

// Harnesses lists those this machine can run, Claude Code first.
func (m *Manager) Harnesses() []HarnessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []HarnessInfo{}
	for _, h := range m.harnesses {
		out = append(out, HarnessInfo{Name: h.Name(), Title: h.Title(), Caps: h.Caps()})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Name == "claude") != (out[j].Name == "claude") {
			return out[i].Name == "claude"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) newSession(name, dir string, h Harness) *Session {
	return &Session{name: name, dir: dir, m: m, harness: h, subs: map[chan Event]struct{}{}, ids: map[string]bool{}, voices: map[string]voiceNote{},
		pending: map[string]json.RawMessage{}, state: Idle}
}

// save writes the registry. Called with m.mu held.
func (m *Manager) save() error {
	recs := make([]record, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		r := record{Name: s.name, Dir: s.dir, ConvID: s.convID, AutoApprove: s.autoApprove, Starred: s.starred}
		if s.harness.Name() != "claude" {
			r.Harness = s.harness.Name()
		}
		recs = append(recs, r)
		s.mu.Unlock()
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.registryPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.registryPath())
}

// Create adds a Claude Code session for a directory.
func (m *Manager) Create(name, dir string) (Info, error) { return m.CreateWith(name, dir, "claude") }

// CreateWith adds a session of the named harness for a directory.
func (m *Manager) CreateWith(name, dir, harness string) (Info, error) {
	if harness == "" {
		harness = "claude"
	}
	if !validName.MatchString(name) {
		return Info{}, fmt.Errorf("a session name is letters, digits, dot, dash and underscore: %q", name)
	}
	// "~" is this machine's home: a phone cannot know where that is, and
	// shrooms-agent runs as the user whose home it is.
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Info{}, err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Info{}, err
	}
	// A directory that is not there yet is made — a new project started
	// from the phone — but only once the rest of the request is known to
	// be good, so a refused one leaves nothing behind.
	st, err := os.Stat(abs)
	missing := errors.Is(err, fs.ErrNotExist)
	if !missing && (err != nil || !st.IsDir()) {
		return Info{}, fmt.Errorf("%s is not a directory on this machine", abs)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.harnesses[harness]
	if !ok {
		return Info{}, fmt.Errorf("this machine has no %q to run sessions with", harness)
	}
	if _, ok := m.sessions[name]; ok {
		return Info{}, fmt.Errorf("there is already a session called %q", name)
	}
	if missing {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return Info{}, fmt.Errorf("making %s: %w", abs, err)
		}
	}
	s := m.newSession(name, abs, h)
	m.sessions[name] = s
	if err := m.save(); err != nil {
		delete(m.sessions, name)
		return Info{}, err
	}
	return s.Info(), nil
}

// Get returns a session by name.
func (m *Manager) Get(name string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	return s, ok
}

// List returns every session, by name.
func (m *Manager) List() []Info {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Remove stops a session and forgets it. Its event log is deleted; the
// Claude Code transcript stays where Claude Code keeps it.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	s, ok := m.sessions[name]
	if ok {
		delete(m.sessions, name)
	}
	err := m.save()
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no session called %q", name)
	}
	s.stop()
	os.Remove(s.eventsPath())
	return err
}

// reap stops processes that have been idle longer than IdleStop.
func (m *Manager) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			ss := make([]*Session, 0, len(m.sessions))
			for _, s := range m.sessions {
				ss = append(ss, s)
			}
			m.mu.Unlock()
			for _, s := range ss {
				s.stopIfIdle(now, m.IdleStop)
			}
		}
	}
}

func (s *Session) eventsPath() string {
	return filepath.Join(s.m.dir, "events", s.name+".jsonl")
}

// Info reports the session for the list.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{Name: s.name, Dir: s.dir, State: s.state, Pending: len(s.pending),
		Running: s.proc != nil, LastSeq: s.seq, AutoApprove: s.autoApprove,
		ContextUsed: s.ctxUsed, ContextWindow: s.ctxWindow, Preview: s.preview, Model: s.model,
		Harness: s.harness.Name(), Caps: s.harness.Caps(), Starred: s.starred, Turns: s.turns}
	if n := len(s.events); n > 0 {
		in.LastTime = s.events[n-1].Time
	}
	return in
}

func (s *Session) loadEvents() {
	f, err := os.Open(s.eventsPath())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		s.seq = e.Seq
		if e.Kind == "claude" {
			s.observe(e.Data)
		}
		if e.Kind == "message" || e.Kind == "voice" {
			var d struct{ ID, Path, Status, Voice string }
			json.Unmarshal(e.Data, &d)
			s.remember(d.ID)
			switch {
			case e.Kind == "voice" && d.ID != "":
				s.voices[d.ID] = voiceNote{path: d.Path, failed: d.Status == "failed"}
			case e.Kind == "message" && d.Voice != "" && d.ID != "":
				s.voices[d.ID] = voiceNote{path: d.Voice}
			}
		}
		s.events = append(s.events, e)
		if len(s.events) > memoryEvents {
			s.events = s.events[len(s.events)-memoryEvents:]
		}
	}
}

// record appends an event, writes it to disk, and hands it to every listener.
// Called with s.mu held.
func (s *Session) record(kind, by string, data any) Event {
	var raw json.RawMessage
	switch d := data.(type) {
	case nil:
	case json.RawMessage:
		raw = d
	default:
		raw, _ = json.Marshal(d)
	}
	s.seq++
	e := Event{Seq: s.seq, Time: time.Now(), Kind: kind, By: by, Data: raw}
	s.events = append(s.events, e)
	if len(s.events) > memoryEvents {
		s.events = s.events[len(s.events)-memoryEvents:]
	}
	if f, err := os.OpenFile(s.eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(e)
		f.Write(append(b, '\n'))
		f.Close()
	} else {
		s.m.log.Warn("could not keep an event", "session", s.name, "err", err)
	}
	s.broadcast(e)
	return e
}

// broadcast hands an event to every listener. Called with s.mu held.
func (s *Session) broadcast(e Event) {
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
			// A listener that cannot keep up is dropped rather than allowed
			// to stall the session; it reconnects and catches up by number.
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// observe keeps what the session list shows about a Claude Code message: how
// full the context is, and the start of the last reply. Called with s.mu held,
// for live messages and for those loaded from disk alike.
func (s *Session) observe(raw json.RawMessage) {
	var m struct {
		Type       string  `json:"type"`
		Subtype    string  `json:"subtype"`
		Model      string  `json:"model"`
		ParentTool *string `json:"parent_tool_use_id"`
		Message    struct {
			Model   string                        `json:"model"`
			Content []struct{ Type, Text string } `json:"content"`
			Usage   struct {
				Input       uint64 `json:"input_tokens"`
				CacheRead   uint64 `json:"cache_read_input_tokens"`
				CacheCreate uint64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		ModelUsage map[string]struct {
			ContextWindow uint64 `json:"contextWindow"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" && m.Model != "" {
			s.model = m.Model
		}
	case "assistant":
		// A subagent's messages carry the tool use they belong to; their
		// usage is the subagent's context, not this conversation's.
		if m.ParentTool != nil && *m.ParentTool != "" {
			return
		}
		if m.Message.Model != "" {
			s.model = m.Message.Model
		}
		if u := m.Message.Usage; u.Input+u.CacheRead+u.CacheCreate > 0 {
			s.ctxUsed = u.Input + u.CacheRead + u.CacheCreate
		}
		var b strings.Builder
		for _, c := range m.Message.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		if t := strings.TrimSpace(b.String()); t != "" {
			if r := []rune(t); len(r) > 200 {
				t = string(r[:200]) + "…"
			}
			s.preview = t
		}
	case "result":
		s.turns++
		// The main model has the largest window; a helper model used for a
		// quick task is listed too, with a smaller one.
		for _, u := range m.ModelUsage {
			if u.ContextWindow > s.ctxWindow {
				s.ctxWindow = u.ContextWindow
			}
		}
	}
}

// Since returns the events after seq, and a channel for those that follow.
// The channel is closed when the listener falls behind or Unsubscribe is
// called; either way, asking again from the last seq seen loses nothing.
func (s *Session) Since(seq uint64) ([]Event, chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	if len(s.events) > 0 && s.events[0].Seq > seq+1 {
		out = s.fromDisk(seq, s.events[0].Seq)
	}
	for _, e := range s.events {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	ch := make(chan Event, 256)
	s.subs[ch] = struct{}{}
	return out, ch
}

// Unsubscribe stops sending to a listener.
func (s *Session) Unsubscribe(ch chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

// fromDisk reads the events with after < seq < before.
func (s *Session) fromDisk(after, before uint64) []Event {
	f, err := os.Open(s.eventsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Seq > after && e.Seq < before {
			out = append(out, e)
		}
	}
	return out
}

// ensureRunning starts the process if there is none. Called with s.mu held.
func (s *Session) ensureRunning() error {
	if s.proc != nil {
		return nil
	}
	if mh, ok := s.harness.(missingHarness); ok {
		return fmt.Errorf("this machine has no %s to run this session with", mh.name)
	}
	s.m.mu.Lock()
	bin := s.m.bins[s.harness.Name()]
	s.m.mu.Unlock()
	o := StartOptions{Resume: s.convID, AutoApprove: s.autoApprove && s.harness.Caps().Approve}
	p, err := startProc(s.m.ctx, s.m.log.With("session", s.name), s.harness, bin, s.dir, o)
	if err != nil {
		return err
	}
	s.proc = p
	go s.read(p)
	return nil
}

// Send is a user turn from a device.
func (s *Session) Send(text, by string) error {
	_, err := s.SendID(text, by, "")
	return err
}

// maxIDs bounds the message ids a session remembers: far more than a device
// has waiting in its outbox at once.
const maxIDs = 1000

// seen reports whether a device-made id has been taken already. Called with
// s.mu held.
func (s *Session) seen(id string) bool { return id != "" && s.ids[id] }

// remember takes an id. Called with s.mu held.
func (s *Session) remember(id string) {
	if id == "" || s.ids[id] {
		return
	}
	s.ids[id] = true
	s.idOrder = append(s.idOrder, id)
	if len(s.idOrder) > maxIDs {
		delete(s.ids, s.idOrder[0])
		s.idOrder = s.idOrder[1:]
	}
}

// SendID is Send with the id the device gave the message, which makes sending
// it again harmless: a device whose outbox could not tell whether the first
// try arrived — the answer was lost, the network went — sends again, and an
// id already taken is not sent twice. duplicate says it was.
func (s *Session) SendID(text, by, id string) (duplicate bool, err error) {
	if strings.TrimSpace(text) == "" {
		return false, errors.New("an empty message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen(id) {
		return true, nil
	}
	if len(s.queued) > 0 {
		// Behind a voice note. Starting the process now is what can fail;
		// that is said to the device, which keeps the message to send again.
		if err := s.ensureRunning(); err != nil {
			return false, err
		}
		s.remember(id)
		s.queued = append(s.queued, &queuedTurn{id: id, by: by, text: text, ready: true})
		return false, nil
	}
	return false, s.send(text, by, map[string]any{"id": id})
}

// queuedTurn is a turn waiting its turn: a voice note being transcribed, or a
// message that came after one.
type queuedTurn struct {
	id, by, text string
	voice        string // the recording, for a voice note
	ready        bool   // text is what to send
	failed       error  // the voice note could not be transcribed
}

// drain sends the queued turns from the front for as long as the first is
// ready, and gives up on voice notes that failed. Called with s.mu held.
func (s *Session) drain() {
	for len(s.queued) > 0 && (s.queued[0].ready || s.queued[0].failed != nil) {
		t := s.queued[0]
		s.queued = s.queued[1:]
		err := t.failed
		if err == nil {
			err = s.send(t.text, t.by, map[string]any{"id": t.id, "voice": t.voice})
		}
		switch {
		case err == nil:
		case t.voice != "":
			s.voices[t.id] = voiceNote{path: t.voice, failed: true}
			s.record("voice", t.by, map[string]string{"id": t.id, "path": t.voice, "status": "failed", "error": err.Error()})
		default:
			// A message the device was told was taken: kept in the log with
			// why it did not reach the model, so it is not silently lost.
			s.m.log.Warn("queued message not sent", "session", s.name, "err", err)
			s.record("message", t.by, map[string]string{"id": t.id, "text": t.text, "error": err.Error()})
		}
	}
}

// send writes a turn and records it. Called with s.mu held.
func (s *Session) send(text, by string, extra map[string]any) error {
	if err := s.ensureRunning(); err != nil {
		return err
	}
	if err := s.proc.writeAll(s.proc.codec.Turn(text)); err != nil {
		return err
	}
	data := map[string]any{"text": text}
	for k, v := range extra {
		if v != "" && v != nil {
			data[k] = v
		}
	}
	if id, _ := extra["id"].(string); id != "" {
		s.remember(id)
	}
	s.record("message", by, data)
	s.state = Working
	s.lastUsed = time.Now()
	return nil
}

// Voice takes a voice note recorded on a device and kept at path: it is
// transcribed here, in the background, and what was said is sent as that
// device's turn — nobody waits for the text to read it back first. The
// session's log says what is happening: a "voice" event, transcribing then
// failed if it fails; the message itself, with the recording's path, if not.
// id is the device's, as for SendID.
func (s *Session) Voice(path, by, id string, stt *Transcriber) (duplicate bool) {
	s.mu.Lock()
	if s.seen(id) {
		s.mu.Unlock()
		return true
	}
	s.remember(id)
	s.mu.Unlock()
	s.transcribe(path, by, id, stt)
	return false
}

// voiceNote is a voice note a session has taken.
type voiceNote struct {
	path   string
	failed bool
}

// RetryVoice transcribes a voice note that failed again, from the recording
// kept here — the reason it is kept before anything else is done with it.
func (s *Session) RetryVoice(id, by string, stt *Transcriber) error {
	s.mu.Lock()
	v, ok := s.voices[id]
	s.mu.Unlock()
	switch {
	case !ok:
		return fmt.Errorf("no voice note %q here", id)
	case !v.failed:
		return fmt.Errorf("voice note %q did not fail", id)
	}
	if _, err := os.Stat(v.path); err != nil {
		return fmt.Errorf("its recording is gone: %w", err)
	}
	s.transcribe(v.path, by, id, stt)
	return nil
}

// transcribe turns a kept voice note into the device's turn, in the
// background, saying in the log what happens.
func (s *Session) transcribe(path, by, id string, stt *Transcriber) {
	t := &queuedTurn{id: id, by: by, voice: path}
	s.mu.Lock()
	s.voices[id] = voiceNote{path: path}
	s.record("voice", by, map[string]string{"id": id, "path": path, "status": "transcribing"})
	s.queued = append(s.queued, t)
	s.mu.Unlock()

	go func() {
		start := time.Now()
		text, err := stt.Transcribe(s.m.ctx, path, "auto")
		if err == nil && strings.TrimSpace(text) == "" {
			err = errors.New("nothing was heard in it")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			t.failed = err
		} else {
			t.text, t.ready = text, true
			s.m.log.Info("voice note transcribed", "session", s.name, "took", time.Since(start).Round(time.Millisecond),
				"words", len(strings.Fields(text)), "by", by)
		}
		s.drain()
	}()
}

// Answer answers a permission prompt. Allowing runs the tool with the input
// it asked for; denying tells the model why, so it can do something else.
//
// A question the model asks (the AskUserQuestion tool) arrives the same way,
// as a prompt for that tool, and is answered by allowing it with answers —
// question text to the chosen label, or labels joined by ", ", or what the
// person typed. Allowed without answers it reads to the model as "the user
// did not answer", so that is refused.
func (s *Session) Answer(prompt string, allow bool, message string, answers map[string]string, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answer(prompt, allow, message, answers, by)
}

// QuestionTool is Claude Code's tool for asking the user something.
const QuestionTool = "AskUserQuestion"

// isQuestion reports whether a can_use_tool request is a question rather than
// a permission: never answered by auto-approve, which has nothing to say.
func isQuestion(req json.RawMessage) bool {
	var r struct {
		ToolName string `json:"tool_name"`
	}
	json.Unmarshal(req, &r)
	return r.ToolName == QuestionTool
}

// answer is Answer with s.mu held.
func (s *Session) answer(prompt string, allow bool, message string, answers map[string]string, by string) error {
	req, ok := s.pending[prompt]
	if !ok {
		return fmt.Errorf("no prompt %q is waiting — it may have been answered already", prompt)
	}
	var r struct {
		Input json.RawMessage `json:"input"`
	}
	json.Unmarshal(req, &r)
	question := isQuestion(req)
	if allow && question && len(answers) == 0 {
		return errors.New("this is a question: answer it, or decline it")
	}
	resp := map[string]any{"behavior": "deny", "message": message}
	if allow {
		input := r.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		if question {
			var in map[string]any
			if err := json.Unmarshal(input, &in); err != nil || in == nil {
				in = map[string]any{}
			}
			in["answers"] = answers
			input, _ = json.Marshal(in)
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
	} else if message == "" {
		resp["message"] = "The user declined this from their phone."
	}
	if s.proc == nil {
		return errors.New("the session's process has stopped; the prompt can no longer be answered")
	}
	if err := s.proc.writeAll(s.proc.codec.Respond(prompt, resp)); err != nil {
		return err
	}
	delete(s.pending, prompt)
	rec := map[string]any{"prompt": prompt, "allow": allow, "message": message}
	if question && allow {
		rec["answers"] = answers
	}
	s.record("answer", by, rec)
	if len(s.pending) == 0 {
		s.state = Working
	}
	s.lastUsed = time.Now()
	return nil
}

// Interrupt stops the turn in progress.
func (s *Session) Interrupt(by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return errors.New("nothing is running")
	}
	return s.proc.writeAll(s.proc.codec.Interrupt())
}

// read follows the process's output until it ends.
func (s *Session) read(p *proc) {
	for raw := range p.out {
		var head struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		json.Unmarshal(raw, &head)

		if head.Type == "stream_event" {
			s.partial(raw)
			continue
		}

		s.mu.Lock()
		s.observe(raw)
		autoAnswer := ""
		switch {
		case head.Type == "system" && head.Subtype == "init" && head.SessionID != "" && head.SessionID != s.convID:
			// The id to resume by. Saved at once: a crash before the
			// first turn ends would otherwise lose the conversation.
			s.convID = head.SessionID
			s.mu.Unlock()
			s.m.mu.Lock()
			if err := s.m.save(); err != nil {
				s.m.log.Warn("could not save the session list", "err", err)
			}
			s.m.mu.Unlock()
			s.mu.Lock()
		case head.Type == "control_request" && head.Request.Subtype == "can_use_tool":
			var full struct {
				Request json.RawMessage `json:"request"`
			}
			json.Unmarshal(raw, &full)
			s.pending[head.RequestID] = full.Request
			s.state = Waiting
			if s.autoApprove && !isQuestion(full.Request) {
				autoAnswer = head.RequestID
			}
		case head.Type == "result":
			s.state = Idle
		case s.state == Idle && (head.Type == "assistant" || head.Type == "user" ||
			(head.Type == "system" && head.Subtype == "task_notification")):
			// A turn nobody sent: Claude Code resumes by itself when
			// background work it started finishes. Working, not idle, or the
			// session reads as done while it is busy (2026-10-04).
			s.state = Working
		}
		if s.state != Idle {
			s.lastUsed = time.Now()
		}
		s.record("claude", "", raw)
		if autoAnswer != "" {
			// Switched on after this process started, so it still asks.
			if err := s.answer(autoAnswer, true, "", nil, "auto-approve"); err != nil {
				s.m.log.Warn("could not auto-approve", "session", s.name, "err", err)
			}
		}
		s.mu.Unlock()
	}
	<-p.done
	defer close(p.read)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == p {
		s.proc = nil
	}
	reason := "finished"
	if p.err != nil {
		reason = p.err.Error()
	}
	// A prompt cannot be answered by a process that has gone; leaving it
	// listed would invite an answer that goes nowhere.
	for id := range s.pending {
		delete(s.pending, id)
	}
	s.state = Idle
	s.record("stopped", "", map[string]string{"reason": reason})
}

// partial passes reply text on to listeners as it is written. Only text: the
// model's thinking and tool input arrive whole in the message that follows.
func (s *Session) partial(raw json.RawMessage) {
	var m struct {
		Event struct {
			Type  string                      `json:"type"`
			Delta struct{ Type, Text string } `json:"delta"`
		} `json:"event"`
		ParentTool *string `json:"parent_tool_use_id"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Event.Type != "content_block_delta" ||
		m.Event.Delta.Type != "text_delta" || m.Event.Delta.Text == "" ||
		(m.ParentTool != nil && *m.ParentTool != "") {
		return
	}
	data, _ := json.Marshal(map[string]string{"text": m.Event.Delta.Text})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcast(Event{Seq: s.seq, Time: time.Now(), Kind: "partial", Data: data})
}

// SetAutoApprove switches approvals off for this session — the desktop's
// --dangerously-skip-permissions — or back on. Prompts already waiting are
// allowed at once; a running process keeps going, and the session answers
// whatever it still asks (see read).
func (s *Session) SetAutoApprove(on bool, by string) error {
	s.mu.Lock()
	s.autoApprove = on
	if on {
		for id, req := range s.pending {
			if isQuestion(req) {
				continue
			}
			if err := s.answer(id, true, "", nil, "auto-approve"); err != nil {
				s.m.log.Warn("could not auto-approve", "session", s.name, "err", err)
			}
		}
	}
	s.record("setting", by, map[string]bool{"auto_approve": on})
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}

// SetStarred stars or unstars the session, and keeps that.
func (s *Session) SetStarred(on bool) error {
	s.mu.Lock()
	s.starred = on
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}

// stopIfIdle ends the process if nothing has happened for longer than after.
// A turn in progress or a prompt waiting is not idle, however long it takes.
func (s *Session) stopIfIdle(now time.Time, after time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil && s.state == Idle && now.Sub(s.lastUsed) >= after {
		s.proc.close()
	}
}

// stop ends the process now.
func (s *Session) stop() {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		p.close()
		if p.cmd.Process != nil {
			select {
			case <-p.read:
			case <-time.After(10 * time.Second):
				p.cmd.Process.Kill()
				<-p.read
			}
		}
	}
}
