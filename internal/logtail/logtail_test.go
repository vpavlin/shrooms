package logtail

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRingKeepsTheNewest(t *testing.T) {
	r := NewRing(3)
	for i, msg := range []string{"a", "b", "c", "d", "e"} {
		r.Add(Line{Time: int64(i), Msg: msg})
	}
	got := r.Lines()
	if len(got) != 3 {
		t.Fatalf("kept %d lines, want 3", len(got))
	}
	// Oldest first, and the two that fell off are gone rather than reordered:
	// a tail that wraps in place shows the newest line in the middle, which
	// reads as the daemon logging out of order.
	for i, want := range []string{"c", "d", "e"} {
		if got[i].Msg != want {
			t.Errorf("line %d is %q, want %q", i, got[i].Msg, want)
		}
	}
}

func TestRingPartiallyFilled(t *testing.T) {
	r := NewRing(5)
	r.Add(Line{Msg: "one"})
	r.Add(Line{Msg: "two"})
	got := r.Lines()
	if len(got) != 2 || got[0].Msg != "one" || got[1].Msg != "two" {
		t.Fatalf("got %+v, want the two lines added", got)
	}
}

func TestSinceReturnsOnlyWhatIsNewer(t *testing.T) {
	r := NewRing(10)
	for i := 1; i <= 5; i++ {
		r.Add(Line{Time: int64(i), Msg: "x"})
	}
	if got := r.Since(3); len(got) != 2 {
		t.Fatalf("Since(3) returned %d lines, want 2", len(got))
	}
	if got := r.Since(99); got != nil {
		t.Errorf("Since past the end returned %d lines, want none", len(got))
	}
	if got := r.Since(0); len(got) != 5 {
		t.Errorf("Since(0) returned %d lines, want all 5", len(got))
	}
}

// The handler must not swallow anything: whatever reaches the ring must also
// reach the real destination, or the daemon's log gains a hole nobody notices
// until they read the journal for something else.
func TestHandlerTees(t *testing.T) {
	var sb strings.Builder
	r := NewRing(10)
	log := slog.New(New(slog.NewTextHandler(&sb, nil), r))

	log.Info("tunnel up", "peer", "k11", "rtt", 12*time.Millisecond)

	lines := r.Lines()
	if len(lines) != 1 {
		t.Fatalf("ring holds %d lines, want 1", len(lines))
	}
	if lines[0].Msg != "tunnel up" {
		t.Errorf("msg is %q", lines[0].Msg)
	}
	if lines[0].Level != "INFO" {
		t.Errorf("level is %q", lines[0].Level)
	}
	if want := "peer=k11 rtt=12ms"; lines[0].Attrs != want {
		t.Errorf("attrs are %q, want %q", lines[0].Attrs, want)
	}
	if !strings.Contains(sb.String(), "tunnel up") {
		t.Errorf("the wrapped handler never saw the record: %q", sb.String())
	}
}

// WithAttrs is how the daemon tags a logger per mesh, so those attributes have
// to survive into the tail — a line that says "deaf" without saying which mesh
// is the least useful line in the log.
func TestHandlerCarriesWithAttrs(t *testing.T) {
	r := NewRing(10)
	log := slog.New(New(slog.NewTextHandler(io.Discard, nil), r)).With("mesh", "home")
	log.Warn("deaf", "peers", 0)

	got := r.Lines()[0].Attrs
	if want := "mesh=home peers=0"; got != want {
		t.Errorf("attrs are %q, want %q", got, want)
	}
}

func TestHandlerFlattensGroups(t *testing.T) {
	r := NewRing(10)
	log := slog.New(New(slog.NewTextHandler(io.Discard, nil), r))
	log.Info("path", slog.Group("best", "addr", "1.2.3.4", "rtt_ms", 9))

	if want := "best.addr=1.2.3.4 best.rtt_ms=9"; r.Lines()[0].Attrs != want {
		t.Errorf("attrs are %q, want %q", r.Lines()[0].Attrs, want)
	}
}

// A value containing a space must not run into the next pair, or "hint" — the
// daemon's most useful attribute and always a sentence — turns the rest of the
// line into nonsense.
func TestHandlerQuotesSpacedValues(t *testing.T) {
	r := NewRing(10)
	log := slog.New(New(slog.NewTextHandler(io.Discard, nil), r))
	log.Info("no", "hint", "restart the daemon", "n", 1)

	if want := `hint="restart the daemon" n=1`; r.Lines()[0].Attrs != want {
		t.Errorf("attrs are %q, want %q", r.Lines()[0].Attrs, want)
	}
}

func TestHandlerRespectsLevel(t *testing.T) {
	r := NewRing(10)
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})
	log := slog.New(New(inner, r))
	log.Debug("chatter")
	log.Error("real")

	lines := r.Lines()
	if len(lines) != 1 || lines[0].Msg != "real" {
		t.Fatalf("ring holds %+v; a level the handler is not enabled for must not be kept", lines)
	}
}

// The daemon logs from every mesh goroutine at once while the control socket
// reads the tail. Run with -race.
func TestConcurrentAddAndRead(t *testing.T) {
	r := NewRing(64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Add(Line{Time: int64(j), Msg: "x"})
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			_ = r.Lines()
			_ = r.Since(5)
		}
	}()
	wg.Wait()
}

func TestEnabledDelegates(t *testing.T) {
	h := New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}), NewRing(4))
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info is enabled though the wrapped handler only takes errors")
	}
}
