package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// The daemon falls back to the last config that started when the one in
// place is refused, at load or at startup, and never touches the edited file
// (ADR-080).

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAGoodConfigIsUsedAndKept(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	cfg, raw, fallback, err := loadConfigOrLastGood(discardLog(), cfgPath)
	if err != nil || fallback != "" || cfg.Name != "laptop" {
		t.Fatalf("got %q, fallback %q, err %v", cfg.Name, fallback, err)
	}
	if err := saveLastGood(cfgPath, raw); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(lastGoodPath(cfgPath))
	if err != nil || string(good) != string(raw) {
		t.Fatalf("last good copy %q, err %v", good, err)
	}
	fi, _ := os.Stat(lastGoodPath(cfgPath))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the copy holds the mesh keys: mode %v, want the config's 0600", fi.Mode().Perm())
	}
}

func TestARefusedEditRunsOnTheLastGoodAndIsLeftAlone(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	raw, _ := os.ReadFile(cfgPath)
	if err := saveLastGood(cfgPath, raw); err != nil {
		t.Fatal(err)
	}
	edited := string(raw) + "listen_port = \"not a port\n"
	if err := os.WriteFile(cfgPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, fallback, err := loadConfigOrLastGood(discardLog(), cfgPath)
	if err != nil {
		t.Fatalf("refused with a last good copy there: %v", err)
	}
	if fallback == "" || cfg.Name != "laptop" {
		t.Fatalf("fallback %q, name %q: want the last good config and why", fallback, cfg.Name)
	}
	if now, _ := os.ReadFile(cfgPath); string(now) != edited {
		t.Fatal("the edited config was changed")
	}
}

func TestARefusedConfigWithNoCopyFailsAsBefore(t *testing.T) {
	cfgPath := aConfig(t, t.TempDir())
	if err := os.WriteFile(cfgPath, []byte("name = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadConfigOrLastGood(discardLog(), cfgPath); err == nil {
		t.Fatal("a refused config with no last good copy started")
	}
}

// Loads, but failed to start a mesh: noted, and the next start falls back.
// An edit since then clears the note.
func TestAConfigThatDidNotStartFallsBackNextTime(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)
	good, _ := os.ReadFile(cfgPath)
	if err := saveLastGood(cfgPath, good); err != nil {
		t.Fatal(err)
	}
	if noteRefused(cfgPath, good, errors.New("x")) {
		t.Fatal("noted the last good config itself as refused: nothing to fall back to")
	}

	edited := strings.Replace(string(good), `name = "laptop"`, `name = "k11"`, 1)
	if err := os.WriteFile(cfgPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if !noteRefused(cfgPath, []byte(edited), errors.New("mesh \"home\": lan_hosts: nope")) {
		t.Fatal("a config that did not start was not noted")
	}
	cfg, _, fallback, err := loadConfigOrLastGood(discardLog(), cfgPath)
	if err != nil || cfg.Name != "laptop" || !strings.Contains(fallback, "lan_hosts: nope") {
		t.Fatalf("name %q, fallback %q, err %v: want the last good config", cfg.Name, fallback, err)
	}

	if err := os.WriteFile(cfgPath, []byte(edited+"\n# fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, fallback, err = loadConfigOrLastGood(discardLog(), cfgPath)
	if err != nil || fallback != "" || cfg.Name != "k11" {
		t.Fatalf("name %q, fallback %q, err %v: an edit since the refusal is tried", cfg.Name, fallback, err)
	}
	if _, err := os.Stat(refusedPath(cfgPath)); err == nil {
		t.Fatal("the note about another file was kept")
	}
}
