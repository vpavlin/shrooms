// Package logtail keeps the last few hundred log lines in memory so a UI can
// show them.
//
// The daemon logs to stderr and systemd files that away, which is the right
// place for it and unreachable from a desktop app: Basecamp's QML sandbox
// cannot read a file, cannot run journalctl, and cannot open a socket. The
// Android app has had a log pane since the beginning for exactly this reason —
// when a mesh will not come up, the log is the only thing that says why — and
// the desktop having no equivalent is the difference people notice first.
//
// So the same bounded tail, on the daemon side of the control socket. A tail,
// not a log: it holds a fixed number of recent lines, drops the oldest, and is
// gone when the process ends. journalctl remains the record.
//
// What goes in it is what goes to stderr, which is worth stating because it
// decides who may read it. The daemon does not log secrets — not the network
// key, not an invite token, not a private key — and this has no way to log
// anything stderr does not already carry, so exposing it over the socket
// discloses nothing that reaching the socket did not already disclose.
package logtail

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Line is one record, already rendered. Rendering here rather than in the
// reader keeps the wire shape simple and means every front-end shows the same
// text as the journal does, rather than each inventing its own layout.
type Line struct {
	// Time is when it was logged, as unix milliseconds. A number rather than a
	// formatted stamp so a viewer can say "12s ago" — which is what a log pane
	// is actually read for — without parsing anything.
	Time int64 `json:"t"`

	Level string `json:"level"`
	Msg   string `json:"msg"`

	// Attrs is the structured part, flattened to "key=value key=value". Kept
	// separate from Msg so a narrow pane can show the message and let the
	// detail wrap or elide, which is the difference between a log you can scan
	// and a wall of text.
	Attrs string `json:"attrs,omitempty"`
}

// Ring is a bounded tail of log lines, safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []Line
	next int
	full bool
}

// NewRing returns a ring holding at most n lines.
//
// Two hundred is what the Android app keeps and it has proved to be the right
// order of magnitude: enough to cover a start-up and the first minute of
// discovery, which is where the interesting failures are, and small enough
// that holding it costs nothing worth measuring.
func NewRing(n int) *Ring {
	if n <= 0 {
		n = 200
	}
	return &Ring{buf: make([]Line, n)}
}

// Add appends a line, dropping the oldest when full.
func (r *Ring) Add(l Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = l
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Lines returns what is held, oldest first.
//
// A copy, always: the caller encodes it to JSON while the daemon keeps
// logging, and handing out the backing array would be a data race with the
// next line written.
func (r *Ring) Lines() []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return slices.Clone(r.buf[:r.next])
	}
	out := make([]Line, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}

// Since returns the lines logged strictly after t (unix milliseconds).
//
// So a UI that polls can ask for what it has not seen rather than re-fetching
// two hundred lines every two seconds. Same-millisecond ties go to "already
// seen", which can drop a line that shared a millisecond with the last one
// delivered; the alternative is showing that line twice, and a duplicate in a
// log pane reads as a repeated event, which is a lie about what happened.
func (r *Ring) Since(t int64) []Line {
	all := r.Lines()
	for i, l := range all {
		if l.Time > t {
			return all[i:]
		}
	}
	return nil
}

// Handler tees records into a ring on their way to another handler.
//
// A wrapper rather than a second logger, so there is one call site for every
// log line and no way for the two to disagree about what was logged.
type Handler struct {
	inner slog.Handler
	ring  *Ring

	// Attrs carried by WithAttrs, already flattened. Flattened once here
	// rather than on every record, because WithAttrs is called a handful of
	// times at startup and Handle is called for every line.
	prefix string
	// group is the WithGroup path, applied to record attrs as a key prefix.
	group string
}

// New returns a handler that writes to inner and keeps a copy in r.
func New(inner slog.Handler, r *Ring) *Handler {
	return &Handler{inner: inner, ring: r}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	var b strings.Builder
	b.WriteString(h.prefix)
	rec.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.group, a)
		return true
	})
	h.ring.Add(Line{
		Time:  rec.Time.UnixMilli(),
		Level: rec.Level.String(),
		Msg:   rec.Message,
		Attrs: strings.TrimSpace(b.String()),
	})
	return h.inner.Handle(ctx, rec)
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.prefix)
	for _, a := range as {
		appendAttr(&b, h.group, a)
	}
	return &Handler{inner: h.inner.WithAttrs(as), ring: h.ring, prefix: b.String(), group: h.group}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	g := h.group
	if name != "" {
		g += name + "."
	}
	return &Handler{inner: h.inner.WithGroup(name), ring: h.ring, prefix: h.prefix, group: g}
}

// appendAttr flattens one attribute, recursing into groups.
func appendAttr(b *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := group
		if a.Key != "" {
			inner += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			appendAttr(b, inner, g)
		}
		return
	}
	b.WriteString(group)
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	// Quoted only when it would otherwise run into the next pair. The common
	// case — an address, a name, a duration — stays unquoted and readable,
	// which matters in a pane six words wide.
	if strings.ContainsAny(v, " \t\"") {
		b.WriteString(quoted(v))
	} else {
		b.WriteString(v)
	}
	b.WriteByte(' ')
}

func quoted(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "'") + "\""
}

// Stamp is the millisecond clock the ring records with, exported so a test can
// reason about Since without duplicating the conversion.
func Stamp(t time.Time) int64 { return t.UnixMilli() }
