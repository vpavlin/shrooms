package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The status file is written into a directory of the daemon's own, never
// through a symlink, and comes out 0640 (2026-10-25).
func TestTheStatusFileRefusesADirectoryNotItsOwn(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "real")
	link := filepath.Join(d, "link")
	os.Mkdir(real, 0o750)
	os.Symlink(real, link)
	if err := ownDir(real); err != nil {
		t.Fatalf("its own directory refused: %v", err)
	}
	if err := ownDir(link); err == nil {
		t.Fatal("a symlinked directory was taken")
	}

	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(real, "status.json")
	done := make(chan error, 1)
	go func() {
		done <- writeStatusFile(ctx, slog.New(slog.DiscardHandler), path, "", func() statusPayload { return statusPayload{Name: "x"} })
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(path); err == nil {
			if fi.Mode().Perm() != 0o640 {
				t.Errorf("mode %v, want 0640", fi.Mode().Perm())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no status file written")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if err := writeStatusFile(context.Background(), slog.New(slog.DiscardHandler), filepath.Join(link, "s.json"), "", func() statusPayload { return statusPayload{} }); err == nil {
		t.Error("a status file was written through a symlinked directory")
	}
}
