package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A harness installed after the agent started is found without a restart.
func TestFindHarnessLater(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan string, 1)
	findHarnessEvery(ctx, "pi", 10*time.Millisecond, func(bin string) { got <- bin })
	select {
	case b := <-got:
		t.Fatalf("found %s before it was installed", b)
	case <-time.After(50 * time.Millisecond):
	}

	bin := filepath.Join(dir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		if b != bin {
			t.Fatalf("found %s, want %s", b, bin)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pi installed while running was never found")
	}
	select {
	case b := <-got:
		t.Fatalf("found twice: %s", b)
	case <-time.After(50 * time.Millisecond):
	}
}

// One already there is found at once, before findHarness returns.
func TestFindHarnessNow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	bin := filepath.Join(dir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var got string
	findHarnessEvery(context.Background(), "pi", time.Hour, func(b string) { got = b })
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}
}
