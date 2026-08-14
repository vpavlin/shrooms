package state

import (
	"testing"
)

// The bug this exists for, in the shape it actually took.
//
// `init --mesh office` created the mesh identity and enrolled it while the
// daemon was running. The daemon announces every 45 seconds and persists a
// sequence number each time, writing the whole file from a copy loaded before
// that mesh existed — so the identity and its credential were erased within the
// minute. The next restart minted a fresh identity for the mesh, which no admin
// had signed for, and every peer refused it.
func TestSaveKeepsAMeshItNeverHeardOf(t *testing.T) {
	dir := t.TempDir()

	daemon, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Another process adds a mesh and enrols this device on it.
	admin, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.MeshState("office-net-id", false); err != nil {
		t.Fatal(err)
	}
	if err := admin.SetMeshCredentialFor("office-net-id", false, []byte("signed-by-the-admin")); err != nil {
		t.Fatal(err)
	}

	// The daemon announces. It has never heard of that mesh.
	if _, err := daemon.NextSeq(); err != nil {
		t.Fatal(err)
	}

	after, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	ms, ok := after.Meshes["office-net-id"]
	if !ok {
		t.Fatal("the mesh was erased by an unrelated announce")
	}
	if string(ms.Credential) != "signed-by-the-admin" {
		t.Errorf("credential lost: %q", ms.Credential)
	}
	if ms.Identity == nil || len(ms.Identity.DevicePriv) == 0 {
		t.Error("the mesh identity was lost, so a restart would mint a new one")
	}
}

// A credential installed by `credential set` on a mesh the daemon does know
// about must survive the next announce too — same race, different field.
func TestSaveKeepsACredentialItDoesNotHold(t *testing.T) {
	dir := t.TempDir()

	daemon, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin.Credential = []byte("issued-elsewhere")
	if err := admin.Save(); err != nil {
		t.Fatal(err)
	}

	if _, err := daemon.NextSeq(); err != nil {
		t.Fatal(err)
	}

	after, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Credential) != "issued-elsewhere" {
		t.Errorf("credential lost: %q", after.Credential)
	}
}

// Ours wins where both hold a value: the running process is the one the value
// is being used by, and a stale disk copy must not be resurrected over it.
func TestOursWinsOverDisk(t *testing.T) {
	disk := stateFile{Credential: "old", Seq: 2}
	ours := stateFile{Credential: "new", Seq: 3}

	got := mergeStateFiles(disk, ours)
	if got.Credential != "new" {
		t.Errorf("credential = %q, want the one we hold", got.Credential)
	}
	if got.Seq != 3 {
		t.Errorf("seq = %d, want 3", got.Seq)
	}
}

// A sequence number never goes backwards. A device whose Seq regresses is
// rejected by every peer's replay guard until they forget it, which is
// indistinguishable from the device having vanished.
func TestSeqNeverRegresses(t *testing.T) {
	got := mergeStateFiles(stateFile{Seq: 99}, stateFile{Seq: 7})
	if got.Seq != 99 {
		t.Errorf("seq = %d, want the higher 99", got.Seq)
	}

	got = mergeStateFiles(
		stateFile{Meshes: map[string]meshStateFile{"m": {Seq: 40}}},
		stateFile{Meshes: map[string]meshStateFile{"m": {Seq: 12}}},
	)
	if got.Meshes["m"].Seq != 40 {
		t.Errorf("per-mesh seq = %d, want the higher 40", got.Meshes["m"].Seq)
	}
}

// Concurrent saves from two State objects in one process already shared a
// mutex; across processes the lock is what makes read-merge-write atomic. The
// test that matters is that saving twice never loses the other's mesh.
func TestMergeIsStableBothWays(t *testing.T) {
	dir := t.TempDir()

	a, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.MeshState("mesh-a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.MeshState("mesh-b", false); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}

	after, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mesh-a", "mesh-b"} {
		if _, ok := after.Meshes[want]; !ok {
			t.Errorf("%s did not survive", want)
		}
	}
}
