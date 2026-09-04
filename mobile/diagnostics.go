package mobile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Evidence that outlives the process.
//
// The Logger interface exists so "the app can see what the daemon is doing
// without a log file", which is right for a live view and precisely wrong for a
// crash: an in-memory tail dies with the thing being diagnosed. When shrooms is
// killed on a phone there is currently nothing left to read — the log is gone
// with the process, and last-panic.txt is written by recover.go and shown
// nowhere.
//
// The distinction that matters most here is **why it stopped**, because the two
// causes need opposite fixes:
//
//   - a Go panic, which recover.go already records, is ours to fix
//   - Android killing the service — battery optimisation, background limits,
//     memory pressure — is a setting on the phone, and leaves no trace at all
//
// Nothing today can tell those apart, so "it keeps dying" has been
// undiagnosable. A session marker separates them: written when a session
// starts, removed when one stops cleanly. Finding it at the next start means
// the last run did not get to stop, and the log file's timestamp says roughly
// when it went.

const (
	// Bounded, because this lives in a phone's config directory and nobody is
	// going to rotate it. Half is dropped when the cap is hit rather than a
	// line at a time: trimming per line turns every write into a rewrite.
	maxLogBytes  = 256 * 1024
	maxStopBytes = 16 * 1024
)

func logPath(configDir string) string     { return filepath.Join(configDir, "log.txt") }
func sessionPath(configDir string) string { return filepath.Join(configDir, "session.json") }
func stopsPath(configDir string) string   { return filepath.Join(configDir, "stops.txt") }

// sessionRecord is what a running session leaves behind so the NEXT start can
// tell whether this one ended on purpose.
type sessionRecord struct {
	Started int64  `json:"started"`
	Version string `json:"version"`
}

// appendCapped writes a line, keeping the file under a limit.
func appendCapped(path, line string, max int64) {
	if path == "" {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		// Keep the newest half. Losing the oldest half of a diagnostic tail is
		// the right direction: what happened just before the death is the part
		// worth having.
		if b, err := os.ReadFile(path); err == nil {
			keep := b[len(b)/2:]
			if i := strings.IndexByte(string(keep), '\n'); i >= 0 {
				keep = keep[i+1:]
			}
			_ = os.WriteFile(path, keep, 0o600)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// appendLog records one line where a crash cannot take it with it.
//
// Called from the log bridge, so everything the app sees is also on disk. Best
// effort throughout: a diagnostic that can fail a run is worse than no
// diagnostic.
func appendLog(configDir, level, message string) {
	if configDir == "" {
		return
	}
	appendCapped(logPath(configDir),
		fmt.Sprintf("%s %-5s %s\n", time.Now().Format("15:04:05"), level, message),
		maxLogBytes)
}

// SessionStarted marks a session as running and reports on the previous one.
//
// The return value is empty when the last session stopped cleanly, and
// otherwise says how long it had been running when it went — which is the
// number that identifies the cause. A minute or two after the screen locks is
// battery optimisation; hours in is more likely memory pressure; immediately is
// ours.
func SessionStarted(configDir, version string) string {
	if configDir == "" {
		return ""
	}
	note := ""
	if b, err := os.ReadFile(sessionPath(configDir)); err == nil {
		var prev sessionRecord
		if json.Unmarshal(b, &prev) == nil && prev.Started > 0 {
			// The log file's last write is the closest thing to a time of
			// death: the process did not get to record one itself.
			died := time.Now()
			if fi, err := os.Stat(logPath(configDir)); err == nil {
				died = fi.ModTime()
			}
			ran := died.Sub(time.Unix(prev.Started, 0)).Round(time.Second)
			note = fmt.Sprintf("previous session (%s) did not stop cleanly — "+
				"it had been running %s, last seen %s",
				prev.Version, ran, died.Format("2006-01-02 15:04:05"))
			appendCapped(stopsPath(configDir),
				fmt.Sprintf("%s  KILLED after %s (was %s)\n",
					died.Format("2006-01-02 15:04:05"), ran, prev.Version),
				maxStopBytes)
		}
	}
	rec, _ := json.Marshal(sessionRecord{Started: time.Now().Unix(), Version: version})
	_ = os.WriteFile(sessionPath(configDir), rec, 0o600)
	appendLog(configDir, "INFO", "session started ("+version+")")
	return note
}

// SessionStopped records that this session ended on purpose.
//
// Whatever calls this is saying "we meant it" — so the absence of the call is
// the signal, and it must be made on every path that ends a session
// deliberately, including the one where the user turns the VPN off.
func SessionStopped(configDir string) {
	if configDir == "" {
		return
	}
	if b, err := os.ReadFile(sessionPath(configDir)); err == nil {
		var prev sessionRecord
		if json.Unmarshal(b, &prev) == nil && prev.Started > 0 {
			ran := time.Since(time.Unix(prev.Started, 0)).Round(time.Second)
			appendCapped(stopsPath(configDir),
				fmt.Sprintf("%s  stopped after %s\n",
					time.Now().Format("2006-01-02 15:04:05"), ran),
				maxStopBytes)
		}
	}
	appendLog(configDir, "INFO", "session stopped")
	_ = os.Remove(sessionPath(configDir))
}

// Diagnostics is everything worth sending to somebody who can read it, as one
// blob of text: what the app would share.
//
// One string rather than a file path, because a gomobile binding cannot hand
// back a file and the app has to build the share intent anyway. Ordered with
// the answer first — how it stopped, then the panic if there was one — because
// the log tail is the part nobody reads unless the first two are unhelpful.
func Diagnostics(configDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "shrooms diagnostics — %s\n\n", time.Now().Format(time.RFC3339))

	fmt.Fprintf(&b, "== how it stopped, most recent last ==\n")
	if s := tail(stopsPath(configDir), 20); s != "" {
		b.WriteString(s)
	} else {
		b.WriteString("(no record — this is the first run since diagnostics were added)\n")
	}

	fmt.Fprintf(&b, "\n== last panic ==\n")
	if p := LastPanic(configDir); p != "" {
		b.WriteString(p)
		if !strings.HasSuffix(p, "\n") {
			b.WriteString("\n")
		}
	} else {
		b.WriteString("(none recorded — a kill by Android leaves no panic)\n")
	}
	fmt.Fprintf(&b, "panics swallowed this run: %d\n", PanicsSwallowed())

	fmt.Fprintf(&b, "\n== recent log ==\n")
	if l := tail(logPath(configDir), 200); l != "" {
		b.WriteString(l)
	} else {
		b.WriteString("(empty)\n")
	}
	return b.String()
}

// tail returns the last n lines of a file, or "" if there is nothing to read.
func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ClearDiagnostics forgets everything collected so far, so a fresh problem can
// be told from an old one.
func ClearDiagnostics(configDir string) error {
	if configDir == "" {
		return nil
	}
	for _, p := range []string{logPath(configDir), stopsPath(configDir)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return ClearLastPanic(configDir)
}
