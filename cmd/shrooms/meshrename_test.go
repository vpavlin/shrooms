package main

import (
	"crypto/rand"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

func aKey(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b[:]), "=")
}

func aConfig(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "config.toml")
	body := "network_key = \"" + aKey(t) + "\"\nname = \"laptop\"\n" +
		"interface = \"logos0\"\nlisten_port = 51820\n\n" +
		"mesh.office.key = \"" + aKey(t) + "\"\n" +
		"mesh.test.key   = \"" + aKey(t) + "\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The whole reason this is a command and not a sed.
//
// Interface names and ports come from a mesh's position in a label-sorted list,
// so renaming test to home re-sorts it: home lands before office and the two
// swap both. Firewall rules, port forwards and every peer's cached endpoint
// would then point at the wrong mesh, for a change meant to be cosmetic.
func TestRenamingAMeshMovesNoInterfaceOrPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)

	before, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	was := map[string][2]any{}
	for i, m := range before.Meshes() {
		iface, port := ifaceAndPort(before, m, i)
		was[m.Label] = [2]any{iface, port}
	}
	if was["test"][0] == was["office"][0] {
		t.Fatal("the fixture gives two meshes the same interface")
	}

	if err := cmdMeshRename([]string{"--config", cfgPath, "--admin-dir", dir, "test", "home"}); err != nil {
		t.Fatal(err)
	}

	after, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, gone := after.MeshSet["test"]; gone {
		t.Error("the old label is still there")
	}
	for i, m := range after.Meshes() {
		iface, port := ifaceAndPort(after, m, i)
		old := m.Label
		if old == "home" {
			old = "test" // the same mesh, under its new name
		}
		want, known := was[old]
		if !known {
			continue
		}
		if iface != want[0] || port != want[1] {
			t.Errorf("%s moved from %v/%v to %v/%v", m.Label, want[0], want[1], iface, port)
		}
	}
}

// The admin key file is named after the label, so a mesh whose authority this
// device holds would otherwise stop finding it — and "no admin key" on a mesh
// you minted is an alarming thing to see.
func TestRenamingAMeshTakesItsAdminKeyWithIt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "admin-test.json"),
		[]byte(`{"priv":"x","keys":["AAAA"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdMeshRename([]string{"--config", cfgPath, "--admin-dir", dir, "test", "home"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "admin-home.json")); err != nil {
		t.Errorf("the admin key did not follow the rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "admin-test.json")); !os.IsNotExist(err) {
		t.Error("the old admin key file is still there, so two files now claim the same mesh")
	}
}

// Refusals, each of which would otherwise produce a config that loads and is
// wrong.
func TestRenamingRefusesTheCasesThatWouldBreakThings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := aConfig(t, dir)

	for _, c := range []struct{ name, from, to string }{
		{"onto a label already in use", "test", "office"},
		{"a mesh that is not there", "nope", "home"},
		{"a label with a dot, which would make a config key nobody meant", "test", "home.two"},
		{"a label with an underscore, which cannot be queried as DNS", "test", "home_two"},
		{"onto the top-level form", "test", "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := cmdMeshRename([]string{"--config", cfgPath, "--admin-dir", dir, c.from, c.to}); err == nil {
				t.Errorf("renaming %q to %q was allowed", c.from, c.to)
			}
		})
	}

	// And none of that wrote anything.
	after, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.MeshSet["test"]; !ok {
		t.Error("a refused rename still changed the config")
	}
}
