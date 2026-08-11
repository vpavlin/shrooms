package state

import "testing"

const otherKey = "D4R5TBDQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCA"

// The single-mesh form is not legacy: it bootstraps a mesh, recovers one, and
// is what every config in the field looks like. It must keep meaning exactly
// what it meant.
func TestSingleMeshFormIsOneMesh(t *testing.T) {
	c, err := parseConfig("network_key = \"" + validKey + "\"\nname = \"laptop\"\n")
	if err != nil {
		t.Fatal(err)
	}
	meshes := c.Meshes()
	if len(meshes) != 1 {
		t.Fatalf("got %d meshes, want 1", len(meshes))
	}
	if meshes[0].Label != DefaultLabel || meshes[0].NetworkKey != validKey {
		t.Errorf("mesh is %+v", meshes[0])
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a plain single-mesh config no longer validates: %v", err)
	}
}

func TestPrefixedKeysMakeSeveralMeshes(t *testing.T) {
	c, err := parseConfig(`
name = "laptop"
mesh.home.key = "` + validKey + `"
mesh.home.relay = "true"
mesh.shared.key = "` + otherKey + `"
mesh.shared.admin_keys = ["L3JS74BU74ICIUEKOB37H4ZYC3DDENTDT4TYOGVEXSQLFZPU3I2Q"]
mesh.shared.services = ["immich:2283"]
`)
	if err != nil {
		t.Fatal(err)
	}
	meshes := c.Meshes()
	if len(meshes) != 2 {
		t.Fatalf("got %d meshes, want 2", len(meshes))
	}
	// Sorted, so anything derived from the list is stable between runs.
	if meshes[0].Label != "home" || meshes[1].Label != "shared" {
		t.Fatalf("labels are %q and %q", meshes[0].Label, meshes[1].Label)
	}
	if !meshes[0].Relay {
		t.Error("home should relay")
	}
	if meshes[1].Relay {
		t.Error("shared should not relay: relaying for one set of people does not imply the other")
	}
	if len(meshes[1].Services) != 1 {
		t.Errorf("shared has %d services", len(meshes[1].Services))
	}
	auth, err := meshes[1].Authority()
	if err != nil || auth == nil {
		t.Fatalf("shared has no authority: %v", err)
	}
	if a, _ := meshes[0].Authority(); a != nil {
		t.Error("home gained an authority it never had; credentials are per mesh")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Both forms at once: the single-mesh key is one mesh alongside the named ones.
func TestBothFormsTogether(t *testing.T) {
	c, err := parseConfig("network_key = \"" + validKey + "\"\nname = \"laptop\"\nmesh.shared.key = \"" + otherKey + "\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Meshes()); got != 2 {
		t.Fatalf("got %d meshes, want 2", got)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Two meshes with one key is a config that would present as a mesh where half
// the peers never appear. Better to refuse it at load.
func TestDuplicateKeysAreRefused(t *testing.T) {
	c, err := parseConfig("name = \"laptop\"\nmesh.a.key = \"" + validKey + "\"\nmesh.b.key = \"" + validKey + "\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Error("accepted two meshes sharing a network key")
	}
}

// The label becomes a DNS label in vps.home.mesh.
func TestLabelsWithDotsAreRefused(t *testing.T) {
	c, err := parseConfig("name = \"laptop\"\nmesh.home.key = \"" + validKey + "\"\n")
	if err != nil {
		t.Fatal(err)
	}
	c.MeshSet["not a label"] = c.MeshSet["home"]
	if err := c.Validate(); err == nil {
		t.Error("accepted a mesh name with a space in it")
	}
}

func TestUnknownMeshOptionIsAnError(t *testing.T) {
	if _, err := parseConfig("mesh.home.wat = \"1\"\n"); err == nil {
		t.Error("accepted an unknown mesh option")
	}
}

// The network id keys per-mesh derivation and must not be confused with
// cred.MeshID, which comes from the admin keys and does not always exist.
func TestNetworkIDIsPerKey(t *testing.T) {
	a, _ := Mesh{NetworkKey: validKey}.NetworkID()
	b, _ := Mesh{NetworkKey: otherKey}.NetworkID()
	if a == "" || a == b {
		t.Errorf("network ids %q and %q", a, b)
	}
	again, _ := Mesh{NetworkKey: validKey}.NetworkID()
	if again != a {
		t.Error("network id is not stable for one key")
	}
}
