package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Port is where shrooms-agent listens on each mesh address. Fixed, so a phone
// finds agents by looking for it among the ports peers announce (ADR-026).
const Port = 7387

// Who names the device a request came from, by its source address.
//
// On the mesh the source address is the overlay address, which is derived
// from the device's key — so this is attribution, not a claim the caller
// makes. Unknown addresses get "" and are still served: only members can
// reach the address this listens on at all (docs/agents.md).
type Who func(netip.Addr) string

// Handler serves the agent API.
func Handler(log *slog.Logger, m *Manager, who Who) http.Handler {
	h := &handler{log: log, m: m, who: who}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions", h.list)
	// What the model did, turn by turn, by day, session, the device that asked
	// and model (usage.go); ?since=2006-01-02 limits it.
	mux.HandleFunc("GET /v1/usage", func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		// Rows first: reading them is what brings the limits up to date.
		rows := m.Usage(r.URL.Query().Get("since"))
		writeJSON(w, http.StatusOK, map[string]any{"machine": host, "rows": rows, "limits": m.Limits()})
	})
	mux.HandleFunc("GET /v1/harnesses", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"harnesses": m.Harnesses()})
	})
	mux.HandleFunc("POST /v1/sessions", h.create)
	mux.HandleFunc("DELETE /v1/sessions/{name}", h.remove)
	mux.HandleFunc("PATCH /v1/sessions/{name}", h.update)
	// The same, for clients that cannot send PATCH (Android's HttpURLConnection).
	mux.HandleFunc("POST /v1/sessions/{name}/settings", h.update)
	mux.HandleFunc("GET /v1/sessions/{name}/history", h.history)
	mux.HandleFunc("GET /v1/sessions/{name}/search", h.search)
	mux.HandleFunc("POST /v1/sessions/{name}/files", h.upload)
	mux.HandleFunc("POST /v1/sessions/{name}/transcribe", h.transcribe)
	mux.HandleFunc("POST /v1/sessions/{name}/voice", h.voice)
	mux.HandleFunc("POST /v1/sessions/{name}/voice/{id}/retry", h.retryVoice)
	mux.HandleFunc("GET /v1/conversations", h.conversations)
	mux.HandleFunc("POST /v1/terminals/{pid}/stop", h.stopTerminal)
	mux.HandleFunc("GET /v1/sessions/{name}/events", h.events)
	mux.HandleFunc("POST /v1/sessions/{name}/messages", h.message)
	mux.HandleFunc("POST /v1/sessions/{name}/prompts/{id}", h.answer)
	mux.HandleFunc("POST /v1/sessions/{name}/interrupt", h.interrupt)
	mux.HandleFunc("POST /v1/sessions/{name}/restart", h.restart)
	mux.HandleFunc("POST /v1/sessions/{name}/rename", h.rename)
	// A2A (a2a.go): the machine's card, each session's, and JSON-RPC.
	mux.HandleFunc("GET /.well-known/agent-card.json", h.machineCard)
	mux.HandleFunc("GET /a2a/{name}/.well-known/agent-card.json", h.a2aCard)
	mux.HandleFunc("POST /a2a/{name}", h.a2a)
	mux.HandleFunc("POST /a2a", h.a2a)
	return mux
}

type handler struct {
	log *slog.Logger
	m   *Manager
	who Who
}

func (h *handler) caller(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	a, err := netip.ParseAddr(host)
	if err != nil || h.who == nil {
		return ""
	}
	return h.who(a.Unmap())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	s, ok := h.m.Get(r.PathValue("name"))
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no session called %q", r.PathValue("name")))
	}
	return s, ok
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	// With where the subscription stands, so an app shows it at a glance
	// without asking for usage (Limits).
	writeJSON(w, http.StatusOK, map[string]any{"sessions": h.m.List(), "limits": h.m.Limits()})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Dir         string `json:"dir"`
		AutoApprove *bool  `json:"auto_approve"`
		// Resume names an existing conversation to continue — one started in
		// a terminal, say (GET /v1/conversations).
		Resume string `json:"resume"`
		// Harness runs it: "claude" (the default), "pi", … (GET /v1/harnesses).
		Harness string `json:"harness"`
		// KeepRunning: never stopped as idle, and started again if it ends.
		KeepRunning *bool `json:"keep_running"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	var in Info
	var err error
	switch {
	case req.Resume != "":
		in, err = h.m.AdoptWith(req.Name, req.Dir, req.Resume, req.Harness)
	default:
		in, err = h.m.CreateWith(req.Name, req.Dir, req.Harness)
	}
	if err == nil && req.AutoApprove != nil {
		if s, ok := h.m.Get(in.Name); ok {
			err = s.SetAutoApprove(*req.AutoApprove, h.caller(r))
			in = s.Info()
		}
	}
	if err == nil && req.KeepRunning != nil {
		if s, ok := h.m.Get(in.Name); ok {
			err = s.SetKeepRunning(*req.KeepRunning, h.caller(r))
			in = s.Info()
		}
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	h.log.Info("session created", "session", in.Name, "dir", in.Dir, "by", h.caller(r))
	writeJSON(w, http.StatusCreated, in)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Remove(r.PathValue("name")); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	h.log.Info("session removed", "session", r.PathValue("name"), "by", h.caller(r))
	w.WriteHeader(http.StatusNoContent)
}

// update changes a session's settings: for now, whether it asks.
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	var req struct {
		AutoApprove *bool `json:"auto_approve"`
		Starred     *bool `json:"starred"`
		KeepRunning *bool `json:"keep_running"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if req.AutoApprove != nil {
		if err := s.SetAutoApprove(*req.AutoApprove, h.caller(r)); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		h.log.Info("auto-approve changed", "session", s.Name(), "on", *req.AutoApprove, "by", h.caller(r))
	}
	if req.Starred != nil {
		if err := s.SetStarred(*req.Starred); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.KeepRunning != nil {
		if err := s.SetKeepRunning(*req.KeepRunning, h.caller(r)); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		h.log.Info("keep running changed", "session", s.Name(), "on", *req.KeepRunning, "by", h.caller(r))
	}
	writeJSON(w, http.StatusOK, s.Info())
}

// MaxUpload bounds one uploaded file: photos, screenshots, logs and PDFs, not
// disk images.
const MaxUpload = 50 << 20

// upload keeps a file sent from a device on this machine, for the session to
// read: ?name=<original name>, the bytes as the body. Returns {"path"}, which
// the device puts in its next message — Claude Code reads images, PDFs and
// text from a path like any other file.
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	path, err := s.Upload(r.URL.Query().Get("name"), http.MaxBytesReader(w, r.Body, MaxUpload))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	h.log.Info("file received", "session", s.Name(), "path", path, "by", h.caller(r))
	writeJSON(w, http.StatusCreated, map[string]string{"path": path})
}

// transcribe keeps a voice note like any other file and returns its text,
// transcribed on this machine: ?name=<file name>&lang=<code or auto>. The
// phone puts the text in the message box, to be read and corrected first.
func (h *handler) transcribe(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if h.m.STT == nil {
		fail(w, http.StatusNotImplemented, fmt.Errorf("no speech-to-text on this machine: see shrooms-agent --help (-stt-model)"))
		return
	}
	path, err := s.Upload(r.URL.Query().Get("name"), http.MaxBytesReader(w, r.Body, MaxUpload))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	start := time.Now()
	text, err := h.m.STT.Transcribe(r.Context(), path, r.URL.Query().Get("lang"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	h.log.Info("voice note transcribed", "session", s.Name(), "took", time.Since(start).Round(time.Millisecond),
		"words", len(strings.Fields(text)), "by", h.caller(r))
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "text": text})
}

// conversations lists this machine's Claude Code conversations, newest first,
// with any terminal claude open in the same directory: ?limit=N (default 30).
func (h *handler) conversations(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = min(n, 200)
		}
	}
	cs, err := h.m.Conversations(limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": cs})
}

// stopTerminal ends a terminal's claude so a session can take its conversation
// over. Only processes Terminals reports: Claude Code run by this user, by hand.
func (h *handler) stopTerminal(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := StopTerminal(pid); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	h.log.Info("terminal claude stopped", "pid", pid, "by", h.caller(r))
	w.WriteHeader(http.StatusNoContent)
}

// history is the conversation before this agent's own events, from Claude
// Code's transcript: ?before=<RFC 3339>&limit=N (default 30, at most 200).
func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, fmt.Errorf("limit: %q", v))
			return
		}
		limit = min(n, 200)
	}
	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			fail(w, http.StatusBadRequest, fmt.Errorf("before: %w", err))
			return
		}
		before = t
	}
	said, err := s.History(before, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if said == nil {
		said = []Said{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": said})
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, fmt.Errorf("limit: %q", v))
			return
		}
		limit = min(n, 200)
	}
	found, err := s.Search(r.URL.Query().Get("q"), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if found == nil {
		found = []Found{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": found})
}

func (h *handler) message(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	// ID, from the device's outbox, makes sending again harmless: one
	// already taken answers 200 {"duplicate":true} and is not sent twice.
	var req struct{ Text, ID string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	dup, err := s.SendID(req.Text, h.caller(r), req.ID)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	if dup {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// retryVoice transcribes a failed voice note again, from its kept recording.
func (h *handler) retryVoice(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if h.m.STT == nil {
		fail(w, http.StatusNotImplemented, fmt.Errorf("no speech-to-text on this machine"))
		return
	}
	if err := s.RetryVoice(r.PathValue("id"), h.caller(r), h.m.STT); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// voice takes a voice note as a turn: ?name=<file name>&id=<the device's id>,
// the audio as the body. It is kept like any upload and answered at once
// (202, {"path"}); the transcription and the turn follow in the background
// (Session.Voice). The same id again answers 200 {"duplicate":true}.
func (h *handler) voice(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if h.m.STT == nil {
		fail(w, http.StatusNotImplemented, fmt.Errorf("no speech-to-text on this machine: see shrooms-agent --help (-stt-model)"))
		return
	}
	id := r.URL.Query().Get("id")
	s.mu.Lock()
	dup := s.seen(id)
	s.mu.Unlock()
	if dup {
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	path, err := s.Upload(r.URL.Query().Get("name"), http.MaxBytesReader(w, r.Body, MaxUpload))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.Voice(path, h.caller(r), id, h.m.STT) {
		os.Remove(path) // the same note arrived twice at once
		writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"path": path})
}

func (h *handler) answer(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	var req struct {
		Allow   bool              `json:"allow"`
		Message string            `json:"message"`
		Answers map[string]string `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Answer(r.PathValue("id"), req.Allow, req.Message, req.Answers, h.caller(r)); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) interrupt(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if err := s.Interrupt(h.caller(r)); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *handler) rename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	in, err := h.m.Rename(r.PathValue("name"), strings.TrimSpace(req.Name), h.caller(r))
	if err != nil {
		code := http.StatusConflict
		if _, ok := h.m.Get(r.PathValue("name")); !ok {
			code = http.StatusNotFound
		}
		fail(w, code, err)
		return
	}
	h.log.Info("session renamed", "from", r.PathValue("name"), "to", in.Name, "by", h.caller(r))
	writeJSON(w, http.StatusOK, in)
}

func (h *handler) restart(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if err := s.Restart(h.caller(r)); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// events is the session's history after ?after=N, then everything that
// follows, as server-sent events: one `data:` line of JSON per event, with
// the sequence number as its id. A comment is sent every 20 seconds so a
// phone on mobile data notices a dead connection.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	after := uint64(0)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			fail(w, http.StatusBadRequest, fmt.Errorf("after: %w", err))
			return
		}
		after = n
	}
	// The standard reconnect header wins over the query: it is what an SSE
	// client sends on its own after a drop.
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
			after = n
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	backlog, ch := s.Since(after)
	defer s.Unsubscribe(ch)
	// ?tail=N on a first connection: only the last N events of the backlog.
	// A long session holds thousands — this conversation passed 1,500 in a
	// day, much of it tool output — and a client that replays them all
	// scrolls through its own history for seconds (the phone) or receives
	// megabytes in one message (Basecamp). The client says what it skipped
	// and can ask for everything. A reconnect (after > 0) is never trimmed:
	// it is catching up, and must not lose anything.
	if v := r.URL.Query().Get("tail"); v != "" && after == 0 {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && len(backlog) > n {
			backlog = backlog[len(backlog)-n:]
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(e Event) error {
		b, _ := json.Marshal(e)
		_, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, b)
		return err
	}
	last := after
	for _, e := range backlog {
		if err := send(e); err != nil {
			return
		}
		last = e.Seq
	}
	flusher.Flush()

	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				// Fell behind: end the stream; the client reconnects with
				// Last-Event-ID and loses nothing.
				return
			}
			if e.Kind == "partial" {
				// Live only and unnumbered: no id, so a reconnect does not
				// resume from it.
				b, _ := json.Marshal(e)
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
					return
				}
				flusher.Flush()
				continue
			}
			if e.Seq <= last {
				continue // already in the backlog
			}
			if err := send(e); err != nil {
				return
			}
			last = e.Seq
			flusher.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
