package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Which key on the card a mesh signs with has to be recorded, because the card
// will derive any path asked of it and says nothing about which was used.

func writeAdmin(t *testing.T, dir, label string, account uint32) {
	t.Helper()
	af := adminFile{Keys: []string{"AAAA"}, Account: account}
	raw, err := json.Marshal(af)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminPathFor(dir, label), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An authority minted before accounts existed has no field, and must keep
// signing with exactly the key it always did.
func TestAnAuthorityWithNoAccountIsZero(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"priv":"","keys":["AAAA"]}`)
	if err := os.WriteFile(filepath.Join(dir, "admin.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := accountFor(dir, ""); got != 0 {
		t.Errorf("account = %d; an existing authority must stay at 0 or it signs with a different key", got)
	}
}

func TestAccountForReadsWhatWasRecorded(t *testing.T) {
	dir := t.TempDir()
	writeAdmin(t, dir, "kc", 3)
	if got := accountFor(dir, "kc"); got != 3 {
		t.Errorf("account = %d, want 3", got)
	}
	// A mesh with no file at all is not an error here: signing fails later with
	// a better message than this function could give.
	if got := accountFor(dir, "absent"); got != 0 {
		t.Errorf("account = %d for a mesh with no admin file", got)
	}
}

// Max plus one, not the first gap. A gap means a mesh that was removed, and
// reusing its account mints a mesh with the same admin key — and so the same
// mesh id — as one this device may still hold credentials for.
func TestNextAccountDoesNotReuseARemovedMeshs(t *testing.T) {
	dir := t.TempDir()
	if got := nextAccount(dir); got != 0 {
		t.Errorf("first mesh on a fresh machine got account %d, want 0", got)
	}

	writeAdmin(t, dir, "", 0)
	writeAdmin(t, dir, "home", 1)
	writeAdmin(t, dir, "office", 2)
	if got := nextAccount(dir); got != 3 {
		t.Errorf("next = %d, want 3", got)
	}

	// Remove the middle one: the gap must not be filled.
	if err := os.Remove(adminPathFor(dir, "home")); err != nil {
		t.Fatal(err)
	}
	if got := nextAccount(dir); got != 3 {
		t.Errorf("next = %d after a removal, want 3 — reusing 1 would mint the "+
			"same admin key, and so the same mesh id, as the removed mesh", got)
	}
}

// A file that does not parse must not silently drop the machine back to 0,
// which would mint over an authority that already exists.
func TestNextAccountIgnoresJunkWithoutResetting(t *testing.T) {
	dir := t.TempDir()
	writeAdmin(t, dir, "kc", 5)
	if err := os.WriteFile(filepath.Join(dir, "admin-broken.json"), []byte("{oh no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := nextAccount(dir); got != 6 {
		t.Errorf("next = %d, want 6", got)
	}
}
