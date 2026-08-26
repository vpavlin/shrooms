package state

import "testing"

// The bug this exists for: a mesh binds one port and announced another.
//
// startInstance worked out the nth mesh's port, bound WireGuard to it, and then
// handed the mesh package a config still carrying the DEVICE's port. Every mesh
// except the first therefore told peers to reach it where the first mesh was
// listening. On a LAN, where the local address is the only candidate that
// matters, that is the whole connection: the peer dials, hits the wrong
// WireGuard socket, the handshake is rejected without comment, and both sides
// retry until a relay carries it instead.

func TestForMeshTakesTheMeshsOwnPort(t *testing.T) {
	dev := Config{Name: "laptop", ListenPort: 51820}
	m := Mesh{Label: "kc", NetworkKey: "k"}

	got := dev.ForMesh(m, 51824)
	if got.ListenPort != 51824 {
		t.Fatalf("ListenPort = %d, want 51824 — the port this mesh actually bound", got.ListenPort)
	}
	// The device's own config must not be altered: it is reused for the next
	// mesh in the loop.
	if dev.ListenPort != 51820 {
		t.Errorf("the device config was mutated: ListenPort = %d", dev.ListenPort)
	}
}

// The first mesh is the one that accidentally worked, because its port IS the
// device's. It has to keep working.
func TestForMeshLeavesTheFirstMeshAlone(t *testing.T) {
	dev := Config{Name: "laptop", ListenPort: 51820}
	got := dev.ForMesh(Mesh{Label: DefaultLabel}, 51820)
	if got.ListenPort != 51820 {
		t.Fatalf("ListenPort = %d, want 51820", got.ListenPort)
	}
}

// Two copies of this had drifted — the phone applied four of the seven per-mesh
// settings. Naming them here is what stops a field being added to one caller.
func TestForMeshCarriesEveryPerMeshSetting(t *testing.T) {
	dev := Config{
		Name: "laptop", ListenPort: 51820,
		NetworkKey: "device", AdminKeys: []string{"device"},
		Relay: false, AnnounceServices: false, AnnounceBound: false,
		QuietRevocations: false, Services: []string{"device"},
	}
	m := Mesh{
		Label: "kc", NetworkKey: "mesh", AdminKeys: []string{"mesh"},
		Relay: true, AnnounceServices: true, AnnounceBound: true,
		QuietRevocations: true, Services: []string{"mesh"},
	}

	got := dev.ForMesh(m, 51824)
	if got.NetworkKey != "mesh" {
		t.Error("NetworkKey not taken from the mesh")
	}
	if len(got.AdminKeys) != 1 || got.AdminKeys[0] != "mesh" {
		t.Error("AdminKeys not taken from the mesh")
	}
	if !got.Relay {
		t.Error("Relay not taken from the mesh")
	}
	if !got.AnnounceServices {
		t.Error("AnnounceServices not taken from the mesh — the phone was missing this one")
	}
	if !got.AnnounceBound {
		t.Error("AnnounceBound not taken from the mesh — the phone was missing this one")
	}
	if !got.QuietRevocations {
		t.Error("QuietRevocations not taken from the mesh — the phone was missing this one")
	}
	if len(got.Services) != 1 || got.Services[0] != "mesh" {
		t.Error("Services not taken from the mesh")
	}
	// Device-wide settings survive.
	if got.Name != "laptop" {
		t.Error("the device's name was lost")
	}
}
