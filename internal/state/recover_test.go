package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A zero-length state.json is what a power loss leaves behind: the rename
// reached the journal, the data blocks did not. On minipc-k11 on 2026-08-25 it
// produced 1385 restarts in a loop against "parse state: unexpected end of JSON
// input", and the identity in that file is not regenerable.
func TestAnEmptyStateFileRecoversFromItsBackup(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := first.Identity.DevicePub
	// A few announces, so the backup's counter is behind the live one.
	for i := 0; i < 5; i++ {
		if _, err := first.NextSeq(); err != nil {
			t.Fatal(err)
		}
	}

	// Reopen so the backup is written from a file that parsed.
	if _, err := LoadOrCreateState(dir); err != nil {
		t.Fatal(err)
	}
	sd := StateDir(dir)
	if _, err := os.Stat(filepath.Join(sd, "state.json.bak")); err != nil {
		t.Fatalf("no backup was kept: %v", err)
	}

	// The power cut.
	if err := os.WriteFile(filepath.Join(sd, "state.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatalf("an empty state.json was not recovered: %v", err)
	}
	if !got.Identity.DevicePub.Equal(want) {
		t.Fatal("recovered a DIFFERENT identity, which is a stranger to every peer " +
			"and holds a credential nobody signed")
	}

	// Put back, so the next start does not depend on the backup again.
	live, err := os.ReadFile(filepath.Join(sd, "state.json"))
	if err != nil || len(live) == 0 {
		t.Fatalf("the live file was left broken after recovery: %v", err)
	}

	// And ahead of where it was, or every announce is refused as a replay until
	// the counter climbs back past what peers already recorded.
	if got.Seq <= 5 {
		t.Errorf("sequence resumed at %d, which is not clear of what peers have seen", got.Seq)
	}
}

// The one thing that must never happen quietly. A fresh identity has no
// credential, cannot be given one without an admin, and joins at a different
// address — so the node comes up looking healthy and is invisible to the mesh.
func TestABrokenStateWithNoBackupRefusesRatherThanMintingANewIdentity(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateState(dir); err != nil {
		t.Fatal(err)
	}
	sd := StateDir(dir)
	if err := os.WriteFile(filepath.Join(sd, "state.json"), []byte("{trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sd, "state.json.bak")); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrCreateState(dir)
	if err == nil {
		t.Fatal("started anyway; a new identity was minted over a broken state file")
	}
	// The message is the whole mitigation here: the obvious fix is to delete
	// the file, and that is the one action that cannot be undone.
	if !contains(err.Error(), "DO NOT DELETE") {
		t.Errorf("the error does not warn against deleting it: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The backup must be a copy of what was read, not a re-serialisation: a field
// this build does not know about would be dropped on the way through, and the
// backup would quietly become worse than the thing it is backing up.
func TestTheBackupKeepsFieldsThisBuildDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateState(dir); err != nil {
		t.Fatal(err)
	}
	sd := StateDir(dir)
	path := filepath.Join(sd, "state.json")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["from_a_later_version"] = "keep me"
	edited, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateState(dir); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(sd, "state.json.bak"))
	if err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	if err := json.Unmarshal(backup, &b); err != nil {
		t.Fatal(err)
	}
	if b["from_a_later_version"] != "keep me" {
		t.Error("the backup dropped a field it did not understand")
	}
}
