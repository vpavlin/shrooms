package mobile

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Panics, and why they are caught here rather than left to crash.
//
// A gomobile binding runs inside the app's process, so a panic anywhere in Go —
// one nil map, one slice bound, in code reached by a message a peer sent — takes
// the whole app down. There was no recover() anywhere in this package or in the
// Kotlin service, and no log the user could reach afterwards, so a crash left
// nothing behind but "shrooms crashed on me again".
//
// The trade is deliberate and worth stating. Swallowing panics can hide a bug
// that a crash would have made obvious. But the failing paths here are driven
// by messages arriving from other devices, and the honest comparison is not
// "crash versus continue" — it is "the VPN dies with no explanation" versus
// "this one message is dropped and the reason is written down". A node that
// panics on every announce will fill the record and say so.
//
// The record goes to a file rather than a log, because the process is expected
// to be gone by the time anybody looks.

// panicCount is how many panics this process has swallowed, so a caller can
// tell "it happened once" from "it happens constantly".
var panicCount atomic.Int64

// crashFile is where the last panic is recorded, inside the config directory so
// it survives the process and the app can read it back.
func crashFile(configDir string) string {
	return filepath.Join(configDir, "last-panic.txt")
}

// guard runs fn and records a panic instead of letting it kill the app.
//
// where should say what was happening, because a stack alone does not say which
// peer's message or which mesh was involved.
func guard(log *slog.Logger, configDir, where string, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		n := panicCount.Add(1)
		stack := debug.Stack()
		record := fmt.Sprintf("%s\npanic #%d in %s: %v\n\n%s\n",
			time.Now().UTC().Format(time.RFC3339), n, where, r, stack)

		// Best effort, and deliberately not checked: this runs while something
		// is already wrong, and failing to write the note must not become a
		// second panic on top of the first.
		if configDir != "" {
			_ = os.WriteFile(crashFile(configDir), []byte(record), 0o600)
		}
		if log != nil {
			log.Error("recovered from a panic", "where", where, "count", n,
				"panic", fmt.Sprint(r))
		}
	}()
	fn()
}

// LastPanic returns what was recorded the last time Go code panicked, or an
// empty string if it never has.
//
// For a settings or about screen: the alternative to showing this is a user
// reporting "it crashed" with nothing to attach, which is where this started.
func LastPanic(configDir string) string {
	b, err := os.ReadFile(crashFile(configDir))
	if err != nil {
		return ""
	}
	return string(b)
}

// ClearLastPanic forgets the recorded panic, so a user can tell a fresh one
// from the one they already reported.
func ClearLastPanic(configDir string) error {
	err := os.Remove(crashFile(configDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PanicsSwallowed is how many panics this process has recovered from. Zero on a
// healthy run; a number that keeps climbing means a peer is reliably tripping
// something rather than a one-off.
func PanicsSwallowed() int { return int(panicCount.Load()) }
