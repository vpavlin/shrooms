package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Files older than the keeping go, newer ones stay, and the folders they
// leave empty go too; 0 keeps everything.
func TestUploadsArePrunedByAge(t *testing.T) {
	m := &Manager{dir: t.TempDir(), log: slog.New(slog.DiscardHandler)}
	now := time.Now()
	put := func(rel string, age time.Duration) string {
		p := filepath.Join(m.uploadsRoot(), rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("x"), 0o600)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	old := put("foray-home/20261001-photo.jpg", 20*24*time.Hour)
	dropped := put("reviewer/drop/from-laptop_shrooms/talks.tgz", 15*24*time.Hour)
	fresh := put("foray-home/20261030-note.m4a", 2*24*time.Hour)

	defer SetUploadsKeep(DefaultUploadsKeep)
	SetUploadsKeep(0)
	if n, _ := m.pruneUploads(now); n != 0 {
		t.Fatalf("keep 0 removed %d files", n)
	}
	SetUploadsKeep(14 * 24 * time.Hour)
	if n, b := m.pruneUploads(now); n != 2 || b != 2 {
		t.Fatalf("removed %d files, %d bytes; want 2, 2", n, b)
	}
	for _, p := range []string{old, dropped} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was kept", p)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a two-day-old file was removed")
	}
	if _, err := os.Stat(filepath.Join(m.uploadsRoot(), "reviewer")); err == nil {
		t.Error("the emptied drop folders were left behind")
	}
}
