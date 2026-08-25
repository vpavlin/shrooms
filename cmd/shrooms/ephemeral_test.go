package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Outside a container a home directory on the root filesystem is exactly where
// an admin key belongs, so this must never fire there. A false positive would
// refuse to mint a mesh on an ordinary laptop.
func TestMintingIsNotRefusedOutsideAContainer(t *testing.T) {
	if inContainer() {
		t.Skip("running in a container; this asserts the host case")
	}
	dir := t.TempDir()
	if ephemeralAdminDir(dir) {
		t.Error("called a normal directory ephemeral")
	}
	if err := refuseEphemeralMint(dir); err != nil {
		t.Errorf("refused to mint on a host: %v", err)
	}
}

// The device comparison has to work on a path that does not exist yet, because
// the admin directory usually does not: `admin init` creates it.
func TestTheDeviceOfAPathThatDoesNotExistYet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "created", "yet")
	got, ok := deviceOf(dir)
	if !ok {
		t.Fatal("could not resolve a filesystem for a path with missing parents")
	}
	parent, ok := deviceOf(os.TempDir())
	if !ok {
		t.Fatal("could not resolve the parent")
	}
	if got != parent {
		t.Errorf("resolved %d, want the nearest existing ancestor's %d", got, parent)
	}
}

// The message is the whole mitigation: somebody hitting this is about to lose a
// mesh permanently and needs to know that, not just that a command failed.
func TestTheRefusalExplainsWhatWouldBeLost(t *testing.T) {
	err := refuseEphemeralMint("/definitely/not/mounted")
	if err == nil {
		if !inContainer() {
			t.Skip("not in a container, so nothing to refuse")
		}
		t.Fatal("did not refuse an unmounted path inside a container")
	}
	for _, want := range []string{"not recoverable", "never admit another device", "--dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
