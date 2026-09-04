package mobile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The question these answer is "why did it stop", because that is the one the
// user cannot answer today and the one that decides whose bug it is: a panic is
// ours, and Android killing the service is a setting on the phone.

func TestAnUncleanStopIsNoticedAtTheNextStart(t *testing.T) {
	dir := t.TempDir()

	// A session starts and is killed — nothing calls SessionStopped.
	if note := SessionStarted(dir, "v1"); note != "" {
		t.Errorf("first ever start reported a previous session: %q", note)
	}
	appendLog(dir, "INFO", "doing something")

	// The next start finds the marker still there.
	note := SessionStarted(dir, "v2")
	if note == "" {
		t.Fatal("a session that never stopped was not noticed")
	}
	for _, want := range []string{"did not stop cleanly", "running"} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not say %q: %s", want, note)
		}
	}
	if !strings.Contains(note, "v1") {
		t.Errorf("the note blames the wrong version: %s", note)
	}
}

func TestACleanStopIsNotReportedAsAKill(t *testing.T) {
	dir := t.TempDir()
	SessionStarted(dir, "v1")
	SessionStopped(dir)

	if note := SessionStarted(dir, "v2"); note != "" {
		t.Errorf("a deliberate stop was reported as a kill: %q", note)
	}
	// And it is still recorded, because "it stopped when I asked" is worth
	// seeing next to the times it did not.
	if s := tail(stopsPath(dir), 10); !strings.Contains(s, "stopped after") {
		t.Errorf("the clean stop was not recorded: %q", s)
	}
}

// The history is what shows a pattern — always two minutes after the screen
// locks looks nothing like once a day.
func TestStopHistoryAccumulates(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		SessionStarted(dir, "v1")
		SessionStopped(dir)
	}
	SessionStarted(dir, "v1") // killed
	SessionStarted(dir, "v1")

	s := tail(stopsPath(dir), 20)
	if n := strings.Count(s, "stopped after"); n != 3 {
		t.Errorf("clean stops recorded: %d, want 3\n%s", n, s)
	}
	if n := strings.Count(s, "KILLED"); n != 1 {
		t.Errorf("kills recorded: %d, want 1\n%s", n, s)
	}
}

// A phone's config directory has nobody to rotate it, so the file has to bound
// itself — and drop the OLD half, since what happened just before a death is
// the part worth keeping.
func TestTheLogFileBoundsItself(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 200)
	for i := 0; i < 4000; i++ {
		appendLog(dir, "INFO", line)
	}
	fi, err := os.Stat(logPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 2*maxLogBytes {
		t.Errorf("log grew to %d bytes, cap is %d", fi.Size(), maxLogBytes)
	}
	// The newest line survived the trimming.
	appendLog(dir, "INFO", "the last thing that happened")
	if !strings.Contains(tail(logPath(dir), 5), "the last thing that happened") {
		t.Error("trimming dropped the newest lines")
	}
}

// One blob, with the answer near the top.
func TestDiagnosticsLeadsWithHowItStopped(t *testing.T) {
	dir := t.TempDir()
	SessionStarted(dir, "v1")
	appendLog(dir, "WARN", "something odd")
	SessionStarted(dir, "v2") // the previous one was killed

	d := Diagnostics(dir)
	stops := strings.Index(d, "how it stopped")
	logs := strings.Index(d, "recent log")
	if stops < 0 || logs < 0 {
		t.Fatalf("sections missing:\n%s", d)
	}
	if stops > logs {
		t.Error("the log tail comes before how it stopped; the answer should be first")
	}
	if !strings.Contains(d, "KILLED") {
		t.Errorf("the kill is not in the diagnostics:\n%s", d)
	}
	if !strings.Contains(d, "something odd") {
		t.Error("the log tail is missing")
	}
	// Says so plainly rather than leaving a confusing empty section.
	if !strings.Contains(d, "leaves no panic") {
		t.Error("an absent panic is not explained")
	}
}

func TestClearDiagnosticsForgetsEverything(t *testing.T) {
	dir := t.TempDir()
	SessionStarted(dir, "v1")
	appendLog(dir, "INFO", "noise")
	SessionStarted(dir, "v2")

	if err := ClearDiagnostics(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{logPath(dir), stopsPath(dir)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived", filepath.Base(p))
		}
	}
}

// Nothing here may take down a run: a diagnostic that can fail is worse than
// none at all.
func TestDiagnosticsSurviveNoConfigDir(t *testing.T) {
	if note := SessionStarted("", "v1"); note != "" {
		t.Error("reported something with nowhere to read it from")
	}
	SessionStopped("")
	appendLog("", "INFO", "x")
	if err := ClearDiagnostics(""); err != nil {
		t.Errorf("clearing with no directory errored: %v", err)
	}
	_ = Diagnostics("")
}
