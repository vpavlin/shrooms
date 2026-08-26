package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The guard, not the chown. A test cannot become root to watch the ownership
// change, but it can pin the two conditions that decide whether the chown is
// attempted at all — and those are what a regression would break for every
// user, because a giveToUser that mis-fires runs on the ordinary non-sudo path
// that every command takes.

func TestGiveToUserDoesNothingWithoutSudo(t *testing.T) {
	t.Setenv("SUDO_USER", "")

	// A path that does not exist: proof the no-op returns before touching the
	// filesystem, since a chown here would fail loudly.
	if err := giveToUser(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("no SUDO_USER should be a no-op, got %v", err)
	}
}

func TestGiveToUserDoesNothingWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which is the case this test excludes")
	}
	// SUDO_USER set while not root is the shape a plain shell inherits after
	// any earlier sudo in the same session. Chowning somebody's file from an
	// unprivileged process fails, so the euid check has to carry it.
	t.Setenv("SUDO_USER", "nobody")

	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := giveToUser(f); err != nil {
		t.Fatalf("not root should be a no-op, got %v", err)
	}
}

func TestEnsureUserDirCreatesPrivateDir(t *testing.T) {
	t.Setenv("SUDO_USER", "")

	dir := filepath.Join(t.TempDir(), "cfg")
	if err := ensureUserDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 0700 because an admin key lives here.
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %o, want 700", perm)
	}
	// Twice, because init re-runs against an existing directory.
	if err := ensureUserDir(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
}
