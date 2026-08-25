package state

import (
	"bytes"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
)

// A credential written only to the top-level field is stored successfully and
// never read, once the mesh has a per-mesh entry — MeshState returns that entry
// and does not consult the top level again.
//
// That is exactly what "installed a credential, and status still says no
// credential" was on k11, 2026-08-25: `shrooms keys` reads the top level and
// showed it, the daemon reads the mesh entry and did not.
func TestACredentialReachesTheMeshTheDaemonReads(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}

	// The legacy adoption: the first start on a single-mesh node copies the
	// top-level identity into a per-mesh entry. After this, the daemon reads
	// only the entry.
	ms, err := st.MeshState("utcma2fjpvaqk", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms.Credential) != 0 {
		t.Fatal("the fixture already has a credential; it proves nothing")
	}

	blob := []byte("a credential")
	if err := st.SetCredential(st.Identity.DevicePub, blob); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(st.Meshes["utcma2fjpvaqk"].Credential, blob) {
		t.Error("the mesh entry the daemon reads did not get the credential")
	}
	if !bytes.Equal(st.Credential, blob) {
		t.Error("the top-level field did not get it either")
	}

	// And it survives the round trip, because the daemon reads it after a
	// restart and not from this process.
	again, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	back, err := again.MeshState("utcma2fjpvaqk", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Credential, blob) {
		t.Error("the credential did not survive a restart on the mesh entry")
	}
}

// Each mesh has its own derived identity (ADR-015), so a credential belongs to
// exactly the mesh whose device key it names — and must not be written to the
// others, where it would be announced as membership of a mesh that never
// signed it.
func TestACredentialGoesOnlyToTheMeshItNames(t *testing.T) {
	st, err := LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.MeshState("aaaaaaaaaaaaa", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.MeshState("bbbbbbbbbbbbb", false)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Identity.DevicePub.Equal(other.Identity.DevicePub) {
		t.Fatal("two meshes derived the same identity; the test proves nothing")
	}

	blob := []byte("for the first mesh only")
	if err := st.SetCredential(mine.Identity.DevicePub, blob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(st.Meshes["aaaaaaaaaaaaa"].Credential, blob) {
		t.Error("the named mesh did not get it")
	}
	if len(st.Meshes["bbbbbbbbbbbbb"].Credential) != 0 {
		t.Error("a mesh that never signed this credential was given it")
	}
}

// A credential for some other machine must be refused rather than stored: the
// mistake otherwise shows up as a mesh that silently ignores this device.
func TestACredentialForAnotherDeviceIsRefused(t *testing.T) {
	st, err := LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCredential(stranger.DevicePub, []byte("not ours")); err == nil {
		t.Error("stored a credential naming a device this machine is not")
	}
	if len(st.Credential) != 0 {
		t.Error("it was stored anyway")
	}
}
