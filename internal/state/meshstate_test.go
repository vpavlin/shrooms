package state

import (
	"bytes"
	"sync"
	"testing"
)

// The mesh a device already belonged to must keep its keys exactly. Re-deriving
// them would change its overlay address and tunnel key, so every peer would see
// a stranger while the old device lingered until it timed out.
func TestLegacyMeshKeepsItsIdentity(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Seq = 42
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	was := st.Identity

	ms, err := st.MeshState("homeid", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ms.Identity.DevicePriv, was.DevicePriv) || ms.Identity.WGPriv != was.WGPriv {
		t.Error("the original mesh was given new keys")
	}
	if ms.Seq != 42 {
		t.Errorf("seq is %d, want the 42 it was announcing with", ms.Seq)
	}

	// And it survives a reload, which is the only thing that actually matters.
	again, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.MeshState("homeid", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Identity.DevicePriv, was.DevicePriv) {
		t.Error("the identity did not survive a reload")
	}
	if got.Seq != 42 {
		t.Errorf("seq after reload is %d, want 42", got.Seq)
	}
}

// A second mesh gets its own identity, so that nobody in both can tell it is
// the same device.
func TestSecondMeshIsDerivedAndDistinct(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadOrCreateState(dir)

	home, err := st.MeshState("homeid", true)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := st.MeshState("sharedid", false)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(home.Identity.DevicePub, shared.Identity.DevicePub) {
		t.Error("both meshes see the same device key")
	}
	if home.Identity.WGPub == shared.Identity.WGPub {
		t.Error("both meshes see the same tunnel key")
	}
	if st.Master == ([32]byte{}) {
		t.Error("no master secret was generated for the derived mesh")
	}

	// Stable across a reload: derived once and then stored, so a peer never
	// sees this device change keys underneath it.
	again, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.MeshState("sharedid", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Identity.DevicePriv, shared.Identity.DevicePriv) {
		t.Error("the second mesh's identity changed across a restart")
	}
}

// A device that only ever has one mesh must not grow a master secret it does
// not use, so that state.json stays exactly what it was.
func TestSingleMeshGrowsNoMaster(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadOrCreateState(dir)
	if _, err := st.MeshState("homeid", true); err != nil {
		t.Fatal(err)
	}
	if st.Master != ([32]byte{}) {
		t.Error("generated a master secret for a device with one mesh")
	}
}

// Credentials are per mesh, because an authority is.
func TestCredentialsArePerMesh(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadOrCreateState(dir)
	if _, err := st.MeshState("homeid", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MeshState("sharedid", false); err != nil {
		t.Fatal(err)
	}

	if err := st.SetMeshCredential("sharedid", []byte("shared-credential")); err != nil {
		t.Fatal(err)
	}
	home, _ := st.MeshState("homeid", false)
	if len(home.Credential) != 0 {
		t.Error("a credential for one mesh appeared in another")
	}

	// The device's ORIGINAL mesh keeps the single-mesh field in step, so a
	// daemon that knows nothing about several meshes reads what it always did.
	if err := st.SetMeshCredentialFor("homeid", true, []byte("home-credential")); err != nil {
		t.Fatal(err)
	}
	if string(st.Credential) != "home-credential" {
		t.Errorf("single-mesh credential is %q", st.Credential)
	}

	// And an additional mesh must not touch it — even though it shares the
	// device's identity, which every invite-joined mesh does (ADR-017). Getting
	// this wrong left the first mesh announcing a credential belonging to
	// another mesh, which its peers correctly refuse.
	if err := st.SetMeshCredentialFor("sharedid", false, []byte("shared-again")); err != nil {
		t.Fatal(err)
	}
	if string(st.Credential) != "home-credential" {
		t.Errorf("a second mesh overwrote the first mesh's credential: %q", st.Credential)
	}
}

// Several meshes advance their sequence numbers independently and all write
// one file. Two saving at once wrote the same temporary path and raced to
// rename it: one won, the other failed with "no such file or directory" and
// lost its announce — a mesh that then never announced at all.
func TestConcurrentSavesDoNotRace(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.MeshState("meshaaa", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.MeshState("meshbbb", false)
	if err != nil {
		t.Fatal(err)
	}

	// One goroutine per mesh, as the daemon has: each advances its own
	// sequence number and saves. What is shared is the file, which is exactly
	// what raced.
	views := []*State{st.View(a), st.View(b)}
	errs := make(chan error, 200)
	var wg sync.WaitGroup
	for _, v := range views {
		wg.Add(1)
		go func(v *State) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				v.Seq++
				errs <- v.Save()
			}
		}(v)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent save failed: %v", err)
		}
	}

	// And the file is still readable afterwards, with both meshes in it.
	back, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatalf("state.json is unreadable after concurrent saves: %v", err)
	}
	if len(back.Meshes) != 2 {
		t.Errorf("read back %d meshes, want 2", len(back.Meshes))
	}
}
