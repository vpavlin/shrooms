package mobile

import (
	"log/slog"
	"strings"
	"testing"
)

// The point of the guard: a panic must not take the process with it, and it
// must leave something behind. A crash with no record is where this started.
func TestAPanicIsRecordedRatherThanFatal(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.DiscardHandler)

	before := PanicsSwallowed()
	guard(log, dir, "a test", func() { panic("deliberate") })

	if PanicsSwallowed() != before+1 {
		t.Errorf("panic count is %d, want %d", PanicsSwallowed(), before+1)
	}
	rec := LastPanic(dir)
	if rec == "" {
		t.Fatal("nothing was recorded; a user would have nothing to report")
	}
	for _, want := range []string{"deliberate", "a test", "goroutine"} {
		if !strings.Contains(rec, want) {
			t.Errorf("the record does not mention %q:\n%s", want, rec)
		}
	}

	if err := ClearLastPanic(dir); err != nil {
		t.Fatal(err)
	}
	if LastPanic(dir) != "" {
		t.Error("the record survived being cleared")
	}
	// Clearing again is not an error: a user pressing the button twice, or an
	// app clearing on startup, must not see a failure.
	if err := ClearLastPanic(dir); err != nil {
		t.Errorf("clearing an absent record failed: %v", err)
	}
}

// A guarded call that does not panic must be invisible.
func TestTheGuardIsTransparentWhenNothingGoesWrong(t *testing.T) {
	dir := t.TempDir()
	before := PanicsSwallowed()
	ran := false
	guard(slog.New(slog.DiscardHandler), dir, "fine", func() { ran = true })
	if !ran {
		t.Error("the guarded function did not run")
	}
	if PanicsSwallowed() != before {
		t.Error("counted a panic that did not happen")
	}
	if LastPanic(dir) != "" {
		t.Error("wrote a record when nothing went wrong")
	}
}

// A missing config directory must not turn one problem into two.
func TestAPanicWithNowhereToWriteIsStillSurvived(t *testing.T) {
	before := PanicsSwallowed()
	guard(slog.New(slog.DiscardHandler), "", "no config dir", func() { panic("x") })
	if PanicsSwallowed() != before+1 {
		t.Error("the panic was not recovered")
	}
}
