package keycard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// One authority per mesh, at its own place on the card.
//
// Sharing one key across meshes is not only linkable through admin_keys, which
// is what ADR-022 named. Authority.ID() hashes the admin keys and nothing else,
// so two meshes minted from one account share a MESH ID — and a credential or
// revocation issued for one verifies against the other.

func TestPathAtIsHardenedAndPerMesh(t *testing.T) {
	if got := PathAt(0); got != Path {
		t.Errorf("account 0 must be the path every existing mesh already uses: %q", got)
	}
	if got, want := PathAt(1), "m/64265'/1'/0'"; got != want {
		t.Errorf("PathAt(1) = %q, want %q", got, want)
	}
	// Distinct per account, or the whole exercise is decorative.
	seen := map[string]bool{}
	for n := uint32(0); n < 8; n++ {
		p := PathAt(n)
		if seen[p] {
			t.Fatalf("account %d reuses path %q", n, p)
		}
		seen[p] = true
	}
}

// The cache holds one key per path. It used to hold exactly one, so enrolling a
// second authority would have silently replaced the first — and the first is
// the one an existing mesh signs with.
func TestTheKeyCacheKeepsOneKeyPerPath(t *testing.T) {
	dir := t.TempDir()

	if err := writeCardKey(dir, PathAt(0), "02aa"); err != nil {
		t.Fatal(err)
	}
	if err := writeCardKey(dir, PathAt(1), "02bb"); err != nil {
		t.Fatal(err)
	}
	if got := readCardKey(dir, PathAt(0)); got != "02aa" {
		t.Errorf("the first mesh's key came back as %q after a second was added", got)
	}
	if got := readCardKey(dir, PathAt(1)); got != "02bb" {
		t.Errorf("the second mesh's key came back as %q", got)
	}
	if got := readCardKey(dir, PathAt(2)); got != "" {
		t.Errorf("offered %q for an account nothing was written at", got)
	}
}

// An enrolment written by an older build stored one path and one key. It has to
// keep working, or upgrading silently unpairs every card mesh.
func TestTheOldSingleEntryCacheStillReads(t *testing.T) {
	dir := t.TempDir()
	old, err := json.Marshal(storedKey{Path: Path, Key: "02cc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keycard-key"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCardKey(dir, Path); got != "02cc" {
		t.Errorf("an enrolment from an older build came back as %q", got)
	}
	// And writing a second one must not lose it.
	if err := writeCardKey(dir, PathAt(1), "02dd"); err != nil {
		t.Fatal(err)
	}
	if got := readCardKey(dir, Path); got != "02cc" {
		t.Errorf("adding a second authority lost the first: %q", got)
	}
}
