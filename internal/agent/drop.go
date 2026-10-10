package agent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Files between agents (docs/agents-files.md): an agent's session sends a
// file to another's, which keeps it in a folder of its own for that sender,
// and only from senders it allows. Push only: nothing is published for others
// to read, and a receiver always knows what arrived and from whom.
//
// Kept under the session's uploads, uploads/<session>/drop/from-<machine>_
// <session>/, which a cage — sealed or not — already sees read-only at the
// same path: a sealed reviewer is handed code without reaching for it.

// The limits: the most one file may be, and the most one sender's folder may
// hold. Variables for the tests.
var (
	MaxDrop          int64 = 100 << 20
	MaxDropPerSender int64 = 1 << 30
)

const (
	// FromSessionHeader is the sending session, as its agent says it.
	FromSessionHeader = "X-Shrooms-From-Session"
	maxFileAsks       = 10
)

// FileAsk is a sender refused because it is not on the allow list: the apps
// offer to allow it.
type FileAsk struct {
	From string    `json:"from"`
	Name string    `json:"name"`
	Size int64     `json:"size,omitempty"`
	At   time.Time `json:"at"`
}

// allowsFiles is whether the session takes files from sender, "machine/
// session": an entry names it, or its machine with "/*". Nothing is allowed
// until someone allows it. Caller holds s.mu.
func (s *Session) allowsFiles(sender string) bool {
	machine, _, _ := strings.Cut(sender, "/")
	for _, a := range s.acceptFiles {
		if a == sender || a == machine+"/*" {
			return true
		}
	}
	return false
}

// SetAcceptFiles replaces who the session takes files from; asks from those
// now allowed are dropped.
func (s *Session) SetAcceptFiles(list []string, by string) error {
	clean := []string{}
	for _, a := range list {
		a = strings.TrimSpace(a)
		machine, session, ok := strings.Cut(a, "/")
		if !ok || machine == "" || session == "" || strings.ContainsAny(a, " \t\n") {
			return fmt.Errorf("%q: want MACHINE/SESSION or MACHINE/*", a)
		}
		clean = append(clean, a)
	}
	s.mu.Lock()
	s.acceptFiles = clean
	kept := s.fileAsks[:0]
	for _, a := range s.fileAsks {
		if !s.allowsFiles(a.From) {
			kept = append(kept, a)
		}
	}
	s.fileAsks = kept
	s.record("setting", by, map[string][]string{"accept_files_from": clean})
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}

// noteFileAsk keeps a refused sender for the apps, the latest once each.
func (s *Session) noteFileAsk(a FileAsk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []FileAsk{a}
	for _, o := range s.fileAsks {
		if o.From != a.From && len(out) < maxFileAsks {
			out = append(out, o)
		}
	}
	s.fileAsks = out
	s.record("file-request", a.From, a)
}

// dropDir is where a sender's files to the session are kept.
func (s *Session) dropDir(sender string) string {
	return filepath.Join(s.m.dir, "uploads", s.Name(), "drop", "from-"+strings.ReplaceAll(safeName(sender), "/", "_"))
}

func safeName(n string) string {
	return strings.Trim(unsafeName.ReplaceAllString(n, "_"), "._")
}

// Drop keeps a file from sender in its folder: under its own name, numbered
// if it is taken, within the limits. The path it was kept at.
func (s *Session) Drop(sender, name string, body io.Reader) (string, int64, error) {
	base := safeName(filepath.Base(strings.TrimSpace(name)))
	if base == "" {
		return "", 0, errors.New("a file needs a name")
	}
	dir := s.dropDir(sender)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	used := dirSize(dir)
	if used >= MaxDropPerSender {
		return "", 0, fmt.Errorf("%s already holds %d MB from %s; delete some first", s.Name(), used>>20, sender)
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	var path string
	var f *os.File
	for i := 0; ; i++ {
		path = filepath.Join(dir, base)
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		}
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return "", 0, err
		}
	}
	limit := MaxDrop
	if room := MaxDropPerSender - used; room < limit {
		limit = room
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("more than %d MB", limit>>20)
	}
	if err != nil {
		os.Remove(path)
		return "", 0, err
	}
	return path, n, nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// fileNote is what the receiving session reads when a file arrives.
func fileNote(sender, name, path string, size int64, note string) string {
	s := fmt.Sprintf("[shrooms file from %s: %s, %s, kept at %s]", sender, name, humanSize(size), path)
	if note = strings.TrimSpace(note); note != "" {
		s += "\n" + note
	}
	return s
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// sender is who sends a file: the machine the mesh names, and the session its
// agent says — or, from a cage, the session its own agent's proxy says.
func (h *handler) sender(r *http.Request) (string, bool) {
	machine := h.caller(r)
	if machine == "" {
		// This machine: one of its own sessions, or a cage's proxy on it.
		if ms, err := h.m.machines(); err == nil && len(ms) > 0 {
			machine = ms[0].Name
		}
	}
	machine, _, _ = strings.Cut(machine, ".")
	session := r.Header.Get(FromSessionHeader)
	caged := r.Header.Get(cagedHeader) != ""
	if caged {
		session = r.Header.Get(cagedHeader)
	}
	if machine == "" || session == "" {
		return "", caged
	}
	return machine + "/" + session, caged
}

// drop takes a file for a session: POST /v1/sessions/{name}/drop?name=&note=
// &tell=0, the body the file.
func (h *handler) drop(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sender, caged := h.sender(r)
	if sender == "" {
		fail(w, http.StatusBadRequest, errors.New("files come from an agent's session: say which ("+FromSessionHeader+")"))
		return
	}
	s.mu.Lock()
	allowed := s.allowsFiles(sender) && (!caged || s.acceptsCaged())
	s.mu.Unlock()
	if !allowed {
		s.noteFileAsk(FileAsk{From: sender, Name: q.Get("name"), Size: r.ContentLength, At: time.Now()})
		why := fmt.Sprintf("%s does not take files from %s: allow it in %s's settings (files from), "+
			"or with shrooms-agent files allow %s %s on its machine", s.Name(), sender, s.Name(), s.Name(), sender)
		if caged && s.allowsFiles(sender) {
			why = fmt.Sprintf("%s does not take anything from caged agents (accept_caged)", s.Name())
		}
		fail(w, http.StatusForbidden, errors.New(why))
		return
	}
	path, n, err := s.Drop(sender, q.Get("name"), http.MaxBytesReader(w, r.Body, MaxDrop+1))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	name := filepath.Base(path)
	s.mu.Lock()
	s.record("file", sender, map[string]any{"from": sender, "name": name, "path": path, "size": n})
	s.mu.Unlock()
	h.log.Info("file dropped", "session", s.Name(), "from", sender, "path", path, "size", n)
	// The session is told, as a turn, unless the sender says it will say
	// what to do with it itself.
	if q.Get("tell") != "0" {
		if err := s.Send(fileNote(sender, name, path, n, q.Get("note")), sender); err != nil {
			h.log.Warn("file note not sent", "session", s.Name(), "err", err)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": path, "size": n})
}
