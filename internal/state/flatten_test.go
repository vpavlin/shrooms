package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
)

// Real keys, because LoadConfig parses them: a fixture that only looks like a
// key passes Flatten and fails at the round trip, which is the one test here
// that has to exercise the real reader.
func aKey(t *testing.T) string {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	return nk.String()
}

func aTwoShapeConfig(t *testing.T) Config {
	t.Helper()
	topKey, homeKey := aKey(t), aKey(t)
	// 32 bytes, base32 with no padding — the length is what is checked.
	anAdminKey := strings.Repeat("A", 52)
	return Config{
		Name: "laptop", Interface: "logos0", ListenPort: 51820,
		Preset: "logos.test", Mode: ModeEdge,
		// The top-level mesh, with settings of its own.
		NetworkKey: topKey, Relay: true,
		AdminKeys: []string{anAdminKey}, Services: []string{"web:80"},
		AnnounceServices: true,
		MeshSet: map[string]Mesh{
			"home": {NetworkKey: homeKey, QuietRevocations: true},
		},
	}
}

// Flattening must not move any mesh's interface or port. That is the whole
// risk: a mesh that comes back on a different port loses every tunnel and
// every endpoint its peers remembered.
func TestFlattenMovesNoInterfaceOrPort(t *testing.T) {
	before := aTwoShapeConfig(t)
	was := map[string][2]any{}
	for _, m := range before.Meshes() {
		was[m.Label] = [2]any{m.Interface, m.ListenPort}
	}

	after, err := before.Flatten()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range after.Meshes() {
		if got, want := [2]any{m.Interface, m.ListenPort}, was[m.Label]; got != want {
			t.Errorf("%s moved from %v to %v", m.Label, want, got)
		}
	}
	if len(after.Meshes()) != len(before.Meshes()) {
		t.Fatalf("mesh count changed: %d -> %d", len(before.Meshes()), len(after.Meshes()))
	}
}

// The identity fact has to survive, or the node re-derives keys for its
// original mesh, changes its overlay address and voids its credential.
func TestFlattenKeepsTheIdentityMesh(t *testing.T) {
	after, err := aTwoShapeConfig(t).Flatten()
	if err != nil {
		t.Fatal(err)
	}
	if after.NetworkKey != "" {
		t.Error("the top-level mesh is still there")
	}
	found := false
	for _, m := range after.Meshes() {
		if m.InheritsIdentity {
			found = true
			if m.Label != DefaultLabel {
				t.Errorf("%s inherited the identity, want %s", m.Label, DefaultLabel)
			}
		}
	}
	if !found {
		t.Error("no mesh claims the device identity after flattening")
	}
}

// Every per-mesh setting has to land on the mesh that had it.
func TestFlattenCarriesEverySetting(t *testing.T) {
	after, err := aTwoShapeConfig(t).Flatten()
	if err != nil {
		t.Fatal(err)
	}
	d := after.MeshSet[DefaultLabel]
	if d.NetworkKey == "" || !d.Relay || !d.AnnounceServices {
		t.Errorf("default lost settings: %+v", d)
	}
	if len(d.AdminKeys) != 1 || d.AdminKeys[0] != strings.Repeat("A", 52) {
		t.Errorf("default lost its admin keys: %v", d.AdminKeys)
	}
	if len(d.Services) != 1 || d.Services[0] != "web:80" {
		t.Errorf("default lost its services: %v", d.Services)
	}
	if h := after.MeshSet["home"]; !h.QuietRevocations || h.NetworkKey == "" {
		t.Errorf("home was disturbed: %+v", h)
	}
	// Device settings stay device settings.
	if after.Name != "laptop" || after.Interface != "logos0" || after.ListenPort != 51820 {
		t.Errorf("device settings changed: %+v", after)
	}
	if after.Relay || len(after.AdminKeys) != 0 || len(after.Services) != 0 {
		t.Error("a mesh's settings are still on the device")
	}
}

// Two meshes cannot both be "default".
func TestFlattenRefusesALabelClash(t *testing.T) {
	c := aTwoShapeConfig(t)
	c.MeshSet[DefaultLabel] = Mesh{NetworkKey: "OTHER"}
	if _, err := c.Flatten(); err == nil {
		t.Fatal("flattened two meshes onto one label")
	}
}

// Already-flat and prepared configs are left exactly as they are.
func TestFlattenLeavesFlatConfigsAlone(t *testing.T) {
	flat := Config{
		Name: "laptop", Interface: "logos0", ListenPort: 51820,
		MeshSet: map[string]Mesh{"home": {NetworkKey: "H"}},
	}
	out, err := flat.Flatten()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.MeshSet) != 1 || out.MeshSet["home"].NetworkKey != "H" {
		t.Errorf("a flat config was changed: %+v", out.MeshSet)
	}

	prepared := Config{Name: "n", Interface: "logos0", ListenPort: 51820, NetworkKey: KeyPlaceholder}
	if out, err := prepared.Flatten(); err != nil || out.NetworkKey != KeyPlaceholder {
		t.Errorf("a prepared config was disturbed: %+v %v", out, err)
	}
}

// The written file has to load back as the same meshes. This is the step that
// rewrites somebody's /etc/shrooms/config.toml, so a field the writer forgets
// is a setting silently dropped.
func TestFlattenSurvivesAWriteAndRead(t *testing.T) {
	after, err := aTwoShapeConfig(t).Flatten()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, after); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)

	back, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("%v\n---\n%s", err, raw)
	}
	if back.NetworkKey != "" {
		t.Error("a top-level mesh came back")
	}
	got := map[string]Mesh{}
	for _, m := range back.Meshes() {
		got[m.Label] = m
	}
	if len(got) != 2 {
		t.Fatalf("got %d meshes back:\n%s", len(got), raw)
	}
	d := got[DefaultLabel]
	if !d.InheritsIdentity {
		t.Errorf("inherits_identity did not survive:\n%s", raw)
	}
	if !d.Relay || !d.AnnounceServices || d.NetworkKey == "" {
		t.Errorf("default's settings did not survive: %+v", d)
	}
	if h := got["home"]; !h.QuietRevocations {
		t.Errorf("announce_revocations did not survive:\n%s", raw)
	}
	if d.Interface != "logos0" || d.ListenPort != 51820 {
		t.Errorf("default's pin did not survive: %s:%d", d.Interface, d.ListenPort)
	}
}
