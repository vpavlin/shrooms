package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
)

// Two meshes on one device, each with its own authority, identity and
// credential.
//
// This is the test that was missing. Nearly every multi-mesh bug found in the
// field lived in the wiring between the config, the state and the credentials
// rather than in any of them alone: a credential issued against the wrong
// identity, a second mesh overwriting the first one's, both meshes claiming one
// address range. Each was invisible to a unit test of any single package and
// obvious the moment two real meshes existed.
func TestTwoMeshesAreIndependent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shrooms.toml")
	stateDir := filepath.Join(dir, "state")
	adminDir := filepath.Join(dir, "admin")

	quiet(t)
	withStdin(t, "first pass\nfirst pass\nsecond pass\nsecond pass\n")

	if err := cmdInit([]string{
		"--name", "laptop", "--config", cfgPath, "--state", stateDir, "--admin-dir", adminDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cmdInit([]string{
		"--mesh", "shared", "--config", cfgPath, "--state", stateDir, "--admin-dir", adminDir,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	meshes := cfg.Meshes()
	if len(meshes) != 2 {
		t.Fatalf("got %d meshes, want the original and the added one", len(meshes))
	}
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	type resolved struct {
		mesh  state.Mesh
		auth  *cred.Authority
		ms    *state.MeshState
		c     *cred.Credential
		netID string
		block string
	}
	var all []resolved

	for _, m := range meshes {
		auth, err := m.Authority()
		if err != nil || auth == nil {
			t.Fatalf("mesh %q has no authority: %v", m.Label, err)
		}
		netID, err := m.NetworkID()
		if err != nil {
			t.Fatal(err)
		}
		ms, err := st.MeshState(netID, isLegacyMesh(cfg, m))
		if err != nil {
			t.Fatal(err)
		}
		if len(ms.Credential) == 0 {
			t.Fatalf("mesh %q has no credential; this device could not join its own mesh", m.Label)
		}
		c, err := cred.UnmarshalCredential(ms.Credential)
		if err != nil {
			t.Fatalf("mesh %q: %v", m.Label, err)
		}

		// The credential must verify against this mesh's authority...
		if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
			t.Errorf("mesh %q: own credential does not verify: %v", m.Label, err)
		}
		// ...and name the identity this mesh actually announces with. Getting
		// this wrong is silent locally and fatal remotely: every peer refuses
		// the announce and the only evidence is a line in their log.
		if !bytes.Equal(c.DevicePub, ms.Identity.DevicePub) {
			t.Errorf("mesh %q: credential names %x, mesh announces as %x",
				m.Label, c.DevicePub[:8], ms.Identity.DevicePub[:8])
		}
		if !bytes.Equal(c.WGPub, ms.Identity.WGPub[:]) {
			t.Errorf("mesh %q: credential names another tunnel key", m.Label)
		}

		all = append(all, resolved{mesh: m, auth: auth, ms: ms, c: c, netID: netID})
	}

	// Blocks across the set, as the daemon does: two network ids land on the
	// same preferred block about once in sixteen, and assigning per mesh would
	// make this test fail that often for a reason that is not this test's.
	ids := make([]string, 0, len(all))
	for _, r := range all {
		ids = append(ids, r.netID)
	}
	blocks := v4.Blocks(ids)
	for i := range all {
		all[i].block = blocks[all[i].netID].String()
	}

	a, b := all[0], all[1]

	// Separate authorities: being admitted to one mesh must say nothing about
	// the other, or a shared mesh would be a way into a private one.
	if a.auth.ID() == b.auth.ID() {
		t.Error("both meshes share an authority")
	}
	if err := cred.VerifyBy(b.auth, a.c, time.Now()); err == nil {
		t.Error("one mesh's credential verifies against the other's authority")
	}

	// Separate identities: the overlay's host bits are a hash of the device
	// key, so a reused identity would carry the same suffix into both meshes
	// and let anyone in both correlate them.
	if bytes.Equal(a.ms.Identity.DevicePub, b.ms.Identity.DevicePub) {
		t.Error("both meshes use the same device identity")
	}
	if a.ms.Identity.WGPub == b.ms.Identity.WGPub {
		// And WireGuard could not tell the peers apart at all: one preshared
		// key per peer, and ours is per mesh.
		t.Error("both meshes use the same tunnel key")
	}

	// Separate IPv4 blocks: the range is routed at an interface and there is
	// one interface per mesh, so an overlap sends one mesh's packets into the
	// other's translator, which drops them.
	if a.block == b.block {
		t.Errorf("both meshes route %s", a.block)
	}

	// Separate interfaces and ports, with the original mesh keeping exactly
	// what it had.
	if iface, port := ifaceAndPort(cfg, state.Mesh{}, 0); iface != cfg.Interface || port != cfg.ListenPort {
		t.Errorf("the first mesh moved to %s:%d", iface, port)
	}
	i0, p0 := ifaceAndPort(cfg, state.Mesh{}, 0)
	i1, p1 := ifaceAndPort(cfg, state.Mesh{}, 1)
	if i0 == i1 || p0 == p1 {
		t.Errorf("both meshes would use %s:%d", i0, p0)
	}
}

// Adding a mesh must not disturb the one already there. Every field of the
// first mesh survived in the field except the ones that did not, which is how
// the day went.
func TestAddingAMeshLeavesTheFirstAlone(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shrooms.toml")
	stateDir := filepath.Join(dir, "state")
	adminDir := filepath.Join(dir, "admin")

	quiet(t)
	withStdin(t, "first pass\nfirst pass\nsecond pass\nsecond pass\n")
	if err := cmdInit([]string{
		"--name", "laptop", "--config", cfgPath, "--state", stateDir, "--admin-dir", adminDir,
	}); err != nil {
		t.Fatal(err)
	}

	before, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	stBefore, _ := state.LoadOrCreateState(stateDir)
	netID, _ := before.Meshes()[0].NetworkID()
	msBefore, _ := stBefore.MeshState(netID, true)
	credBefore := append([]byte(nil), msBefore.Credential...)
	idBefore := append([]byte(nil), msBefore.Identity.DevicePub...)

	if err := cmdInit([]string{
		"--mesh", "shared", "--config", cfgPath, "--state", stateDir, "--admin-dir", adminDir,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name {
		t.Errorf("the device was renamed from %q to %q", before.Name, after.Name)
	}
	if after.NetworkKey != before.NetworkKey {
		t.Error("the first mesh's network key changed")
	}
	stAfter, _ := state.LoadOrCreateState(stateDir)
	msAfter, _ := stAfter.MeshState(netID, true)
	if !bytes.Equal(msAfter.Identity.DevicePub, idBefore) {
		t.Error("the first mesh's identity changed; every peer would see a stranger")
	}
	if !bytes.Equal(msAfter.Credential, credBefore) {
		t.Error("the first mesh's credential was overwritten by the second mesh's")
	}
}
