package state

import "testing"

// Interface and port are resolved in one place, over one ordering.
//
// They used to be derived by each caller from a mesh's position in a list, and
// the callers disagreed about which list: the daemon numbered the ACTIVE
// meshes, `mesh list` and pinning numbered ALL of them. Those give different
// answers as soon as one mesh is disabled — so a node reported one port and
// bound another.

func cfgWithMeshes(disabled string) Config {
	return Config{
		Interface: "logos0", ListenPort: 51820, NetworkKey: "first",
		MeshSet: map[string]Mesh{
			"home":   {NetworkKey: "h", Disabled: disabled == "home"},
			"office": {NetworkKey: "o", Disabled: disabled == "office"},
		},
	}
}

// Disabling a mesh must not move the others. It used to: switching one off
// renumbered every later mesh onto a different interface and port at the next
// restart, dropping their tunnels and invalidating the endpoints their peers
// had remembered.
func TestDisablingAMeshDoesNotRenumberTheOthers(t *testing.T) {
	was := map[string][2]any{}
	for _, m := range cfgWithMeshes("").Meshes() {
		was[m.Label] = [2]any{m.Interface, m.ListenPort}
	}

	// "home" sorts between "default" and "office", so disabling it is exactly
	// the case that used to shift "office" down a slot.
	for _, m := range cfgWithMeshes("home").Meshes() {
		if m.Label == "home" {
			continue
		}
		if got, want := [2]any{m.Interface, m.ListenPort}, was[m.Label]; got != want {
			t.Errorf("%s moved from %v to %v when another mesh was disabled",
				m.Label, want, got)
		}
	}
}

// Active() must report what Meshes() resolved, or the daemon binds one thing
// and every other command reports another.
func TestActiveAgreesWithMeshes(t *testing.T) {
	cfg := cfgWithMeshes("home")
	all := map[string][2]any{}
	for _, m := range cfg.Meshes() {
		all[m.Label] = [2]any{m.Interface, m.ListenPort}
	}
	for _, m := range cfg.Active() {
		if got, want := [2]any{m.Interface, m.ListenPort}, all[m.Label]; got != want {
			t.Errorf("%s: Active says %v, Meshes says %v", m.Label, got, want)
		}
		if m.Disabled {
			t.Errorf("%s is disabled and still active", m.Label)
		}
	}
}

// A pinned interface or port wins, so a mesh keeps what it had when something
// before it in the order was renamed or removed.
func TestAPinnedInterfaceAndPortWin(t *testing.T) {
	cfg := Config{
		Interface: "logos0", ListenPort: 51820, NetworkKey: "first",
		MeshSet: map[string]Mesh{
			"home": {NetworkKey: "h", Interface: "logos07", ListenPort: 51999},
		},
	}
	for _, m := range cfg.Meshes() {
		if m.Label != "home" {
			continue
		}
		if m.Interface != "logos07" || m.ListenPort != 51999 {
			t.Errorf("pin ignored: got %s:%d", m.Interface, m.ListenPort)
		}
	}
}

// The identity fact is recorded rather than inferred from config shape. Only
// the mesh in the top-level fields carries it, and a config with none has no
// mesh claiming it.
func TestOnlyTheTopLevelMeshInheritsIdentity(t *testing.T) {
	for _, m := range cfgWithMeshes("").Meshes() {
		if m.Label == DefaultLabel && !m.InheritsIdentity {
			t.Error("the top-level mesh does not claim the device identity")
		}
		if m.Label != DefaultLabel && m.InheritsIdentity {
			t.Errorf("%s claims the device identity as well", m.Label)
		}
	}

	only := Config{
		Interface: "logos0", ListenPort: 51820,
		MeshSet: map[string]Mesh{"home": {NetworkKey: "h"}},
	}
	for _, m := range only.Meshes() {
		if m.InheritsIdentity {
			t.Errorf("%s claims an identity no mesh should own here", m.Label)
		}
	}
}
