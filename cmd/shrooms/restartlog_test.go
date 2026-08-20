package main

import (
	"os"
	"testing"
	"time"
)

const base = 10 * time.Minute

// The first restart is not delayed. Whatever else this does, it must not make
// the common case — a momentary fault that a restart fixes — any slower.
func TestTheFirstRestartIsImmediate(t *testing.T) {
	r := loadRestartLog(t.TempDir())
	if ok, left := r.ready(time.Now(), base); !ok {
		t.Errorf("a node with no history waited %s before its first restart", left)
	}
}

// Consecutive restarts back off. A node whose fleet is gone was tearing down
// every tunnel on every mesh six times an hour to retry something that was not
// coming back.
func TestConsecutiveRestartsBackOff(t *testing.T) {
	dir := t.TempDir()
	r := loadRestartLog(dir)
	now := time.Now()

	r.note(now)
	// Immediately after the first, the second must wait.
	if ok, _ := r.ready(now, base); ok {
		t.Fatal("a second restart was allowed immediately after the first")
	}
	// And it must be allowed once the wait has passed.
	if ok, _ := r.ready(now.Add(base+time.Second), base); !ok {
		t.Error("the second restart was still refused after the full interval")
	}

	// Each one waits longer than the last.
	prev := r.wait(base)
	for i := 0; i < 4; i++ {
		r.note(now)
		got := r.wait(base)
		if got < prev {
			t.Errorf("restart %d waits %s, less than the previous %s", r.Count, got, prev)
		}
		prev = got
	}
	if prev > restartBackoffCeiling {
		t.Errorf("backoff reached %s, past the %s ceiling", prev, restartBackoffCeiling)
	}
}

// The wait is bounded. A node that has been failing for days should still check
// occasionally, because the fleet may come back.
func TestBackoffIsCapped(t *testing.T) {
	r := loadRestartLog(t.TempDir())
	now := time.Now()
	for i := 0; i < 50; i++ {
		r.note(now)
	}
	if w := r.wait(base); w != restartBackoffCeiling {
		t.Errorf("after 50 restarts the wait is %s, want the ceiling %s", w, restartBackoffCeiling)
	}
}

// It survives the process exiting, which is the entire difficulty: the watchdog
// restarts by exiting, so an in-memory counter would always read zero.
func TestHistorySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	first := loadRestartLog(dir)
	first.note(time.Now())
	first.note(time.Now())

	// A new process, same state directory.
	second := loadRestartLog(dir)
	if second.Count != 2 {
		t.Errorf("after two restarts a fresh load saw %d", second.Count)
	}
	if ok, _ := second.ready(time.Now(), base); ok {
		t.Error("a fresh process ignored the backoff its predecessor recorded")
	}
}

// A node that recovers stops being penalised, or one bad hour would slow every
// restart for the rest of the week.
func TestRecoveryForgetsTheHistory(t *testing.T) {
	dir := t.TempDir()
	r := loadRestartLog(dir)
	r.note(time.Now())
	r.note(time.Now())

	r.clear()
	if r.Count != 0 {
		t.Errorf("clear left %d restarts", r.Count)
	}
	if ok, _ := r.ready(time.Now(), base); !ok {
		t.Error("a recovered node was still waiting")
	}
	if again := loadRestartLog(dir); again.Count != 0 {
		t.Errorf("the cleared history came back as %d from disk", again.Count)
	}
}

// Time passing counts as recovery too: a node whose last restart was long ago
// is not in a loop, whatever its count says.
func TestAnOldHistoryDoesNotDelay(t *testing.T) {
	r := loadRestartLog(t.TempDir())
	r.note(time.Now().Add(-restartForgetAfter - time.Hour))
	r.Count = 9
	if ok, left := r.ready(time.Now(), base); !ok {
		t.Errorf("a node whose last restart was hours ago waited %s", left)
	}
}

// A corrupt file must not stop the daemon starting. The worst acceptable
// outcome is one un-delayed restart.
func TestACorruptHistoryIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	r := loadRestartLog(dir)
	if err := writeCorrupt(r.path); err != nil {
		t.Fatal(err)
	}
	again := loadRestartLog(dir)
	if again.Count != 0 {
		t.Errorf("a corrupt history parsed as %d restarts", again.Count)
	}
	if ok, _ := again.ready(time.Now(), base); !ok {
		t.Error("a corrupt history blocked a restart")
	}
}

func writeCorrupt(path string) error {
	return os.WriteFile(path, []byte("{not json"), 0o600)
}
