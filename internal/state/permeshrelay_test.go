package state

import "testing"

// advertise carries a port, so it cannot be shared between meshes. The relay
// settings do not, so sharing them is the useful default. These two rules are
// deliberately different and the tests say which is which.

func aDeviceWithTwoMeshes() Config {
	return Config{
		Name: "vps", Interface: "logos0", ListenPort: 51820,
		Advertise:  []string{"203.0.113.5:51820"},
		RelayBlind: []string{"198.51.100.7:31760"}, RelayToken: "shared",
		NetworkKey: "first",
		MeshSet:    map[string]Mesh{"home": {NetworkKey: "h"}},
	}
}

// The bug: every mesh announced the device's advertise, so a peer dialling the
// second mesh reached the first mesh's WireGuard socket.
func TestAdvertiseDoesNotLeakToOtherMeshes(t *testing.T) {
	c := aDeviceWithTwoMeshes()
	for _, m := range c.Meshes() {
		got := c.ForMesh(m, m.ListenPort).Advertise
		if m.ListenPort == c.ListenPort {
			if len(got) != 1 || got[0] != "203.0.113.5:51820" {
				t.Errorf("%s is on the device's port and lost the advertise: %v", m.Label, got)
			}
			continue
		}
		if len(got) != 0 {
			t.Errorf("%s announces %v, which names port %d — it listens on %d",
				m.Label, got, c.ListenPort, m.ListenPort)
		}
	}
}

func TestAMeshsOwnAdvertiseWins(t *testing.T) {
	c := aDeviceWithTwoMeshes()
	c.MeshSet["home"] = Mesh{NetworkKey: "h", Advertise: []string{"203.0.113.5:51821"}}
	for _, m := range c.Meshes() {
		if m.Label != "home" {
			continue
		}
		got := c.ForMesh(m, m.ListenPort).Advertise
		if len(got) != 1 || got[0] != "203.0.113.5:51821" {
			t.Errorf("home got %v", got)
		}
	}
}

// A single-mesh config must be untouched: its one mesh is on the device's port.
func TestASingleMeshKeepsTheDeviceAdvertise(t *testing.T) {
	c := Config{
		Name: "vps", Interface: "logos0", ListenPort: 51820,
		Advertise: []string{"203.0.113.5:51820"}, NetworkKey: "only",
	}
	ms := c.Meshes()
	if len(ms) != 1 {
		t.Fatalf("got %d meshes", len(ms))
	}
	if got := c.ForMesh(ms[0], ms[0].ListenPort).Advertise; len(got) != 1 {
		t.Errorf("a single-mesh config lost its advertise: %v", got)
	}
}

// Relays inherit, because a relay address means the same to every mesh.
func TestRelaySettingsInherit(t *testing.T) {
	c := aDeviceWithTwoMeshes()
	for _, m := range c.Meshes() {
		got := c.ForMesh(m, m.ListenPort)
		if len(got.RelayBlind) != 1 || got.RelayToken != "shared" {
			t.Errorf("%s did not inherit the device's relay: %v %q",
				m.Label, got.RelayBlind, got.RelayToken)
		}
	}
}

func TestAMeshCanOverrideOrRefuseTheRelay(t *testing.T) {
	c := aDeviceWithTwoMeshes()
	c.MeshSet["home"] = Mesh{NetworkKey: "h", RelayBlind: []string{"192.0.2.9:31760"}, RelayToken: "own"}
	for _, m := range c.Meshes() {
		if m.Label != "home" {
			continue
		}
		got := c.ForMesh(m, m.ListenPort)
		if len(got.RelayBlind) != 1 || got.RelayBlind[0] != "192.0.2.9:31760" || got.RelayToken != "own" {
			t.Errorf("override ignored: %v %q", got.RelayBlind, got.RelayToken)
		}
	}

	// "none" is how a mesh opts out entirely. An empty list cannot say this:
	// it parses the same as an absent line, which means inherit.
	c.MeshSet["home"] = Mesh{NetworkKey: "h", RelayNone: true}
	for _, m := range c.Meshes() {
		if m.Label != "home" {
			continue
		}
		got := c.ForMesh(m, m.ListenPort)
		if len(got.RelayBlind) != 0 || got.RelayToken != "" || got.RelayAddr != "" {
			t.Errorf("relay_blind = none did not opt out: %+v", got.RelayBlind)
		}
	}
}

// "Correctly nothing" and "you forgot" look identical, so the node says which.
func TestMeshesMissingAdvertiseNamesThem(t *testing.T) {
	c := aDeviceWithTwoMeshes()
	got := c.MeshesMissingAdvertise()
	if len(got) != 1 || got[0] != "home" {
		t.Errorf("got %v, want [home]", got)
	}

	c.MeshSet["home"] = Mesh{NetworkKey: "h", Advertise: []string{"203.0.113.5:51821"}}
	if got := c.MeshesMissingAdvertise(); len(got) != 0 {
		t.Errorf("warned about a mesh that has its own: %v", got)
	}

	// Nothing to warn about with no device-wide advertise at all.
	c.Advertise = nil
	if got := c.MeshesMissingAdvertise(); len(got) != 0 {
		t.Errorf("warned with no device advertise: %v", got)
	}
}
