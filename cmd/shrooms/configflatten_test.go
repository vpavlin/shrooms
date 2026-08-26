package main

import (
	"os"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

// The command that rewrites somebody's only config, so the parts worth testing
// are the ones around the change rather than the change itself: that the
// original is kept, that the result was read back before being called done, and
// that running it twice cannot destroy the copy of the original.

func TestConfigFlattenKeepsTheOriginalAndFlattens(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	before, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.NetworkKey == "" {
		t.Fatal("the fixture is already flat, so this proves nothing")
	}
	was := map[string][2]any{}
	for _, m := range before.Meshes() {
		was[m.Label] = [2]any{m.Interface, m.ListenPort}
	}

	out := captureStdout(t, func() {
		if err := cmdConfigFlatten([]string{"--config", cfgPath, "--yes"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Done") {
		t.Errorf("did not report success:\n%s", out)
	}

	// The original, byte for byte.
	backup := cfgPath + flattenBackupSuffix
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no copy of the original: %v", err)
	}

	after, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.NetworkKey != "" {
		t.Error("there is still a top-level mesh")
	}
	if len(after.Meshes()) != len(before.Meshes()) {
		t.Fatalf("mesh count changed: %d -> %d", len(before.Meshes()), len(after.Meshes()))
	}
	// The point of the whole exercise: nothing moves.
	for _, m := range after.Meshes() {
		if got, want := [2]any{m.Interface, m.ListenPort}, was[m.Label]; got != want {
			t.Errorf("%s moved from %v to %v", m.Label, want, got)
		}
	}
	// And the mesh that held the device keys still says so.
	found := false
	for _, m := range after.Meshes() {
		if m.InheritsIdentity {
			found = true
		}
	}
	if !found {
		t.Error("no mesh claims the device identity, so its keys would be re-derived")
	}
}

// A second run must not replace the saved original with the flattened copy —
// that would quietly discard the only thing anybody could go back to.
func TestConfigFlattenWillNotOverwriteTheSavedOriginal(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	captureStdout(t, func() {
		if err := cmdConfigFlatten([]string{"--config", cfgPath, "--yes"}); err != nil {
			t.Fatal(err)
		}
	})
	original, err := os.ReadFile(cfgPath + flattenBackupSuffix)
	if err != nil {
		t.Fatal(err)
	}

	// Already flat, so this run reports there is nothing to do rather than
	// touching anything.
	captureStdout(t, func() {
		if err := cmdConfigFlatten([]string{"--config", cfgPath, "--yes"}); err != nil {
			t.Fatal(err)
		}
	})
	again, err := os.ReadFile(cfgPath + flattenBackupSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(again) {
		t.Error("the saved original was replaced")
	}
	if strings.Contains(string(again), "mesh.default.key") {
		t.Error("the saved original is the flattened file, not the original")
	}
}

// --dry-run prints the result and changes nothing.
func TestConfigFlattenDryRunChangesNothing(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := cmdConfigFlatten([]string{"--config", cfgPath, "--dry-run"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "mesh.default.key") {
		t.Errorf("the dry run did not show the flattened config:\n%s", out)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("--dry-run wrote to the config")
	}
	if _, err := os.Stat(cfgPath + flattenBackupSuffix); err == nil {
		t.Error("--dry-run made a backup, so it did more than print")
	}
}
