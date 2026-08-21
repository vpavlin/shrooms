package state

import (
	"os"
	"path/filepath"
	"testing"
)

// The identity must survive a restart, which is the entire point: a bootstrap
// address names a peer id, so a node that mints a new one each start invalidates
// every address it has ever published.
func TestNodeKeySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	first, err := newStateIn(t, dir).NodeKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != NodeKeyLen*2 {
		t.Fatalf("key is %d chars, the library wants %d", len(first), NodeKeyLen*2)
	}

	// A new process over the same state directory.
	again, err := newStateIn(t, dir).NodeKey()
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("the node key changed across a restart; every published bootstrap address would be dead")
	}
}

// Two nodes must not share one, or they claim the same identity on the shard.
func TestEachNodeGetsItsOwn(t *testing.T) {
	a, err := newStateIn(t, t.TempDir()).NodeKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newStateIn(t, t.TempDir()).NodeKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two separate nodes were given the same identity")
	}
}

// A corrupt file is replaced rather than refused. The identity is not a
// credential anybody else depends on, and a node that will not start because of
// a mangled file is worse than one under a new name.
func TestACorruptNodeKeyIsReplaced(t *testing.T) {
	dir := t.TempDir()
	st := newStateIn(t, dir)
	if _, err := st.NodeKey(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nodekey")
	if err := os.WriteFile(path, []byte("not hex, and too short"), 0o600); err != nil {
		t.Fatal(err)
	}

	k, err := newStateIn(t, dir).NodeKey()
	if err != nil {
		t.Fatalf("a corrupt key stopped the node: %v", err)
	}
	if !isNodeKey(k) {
		t.Errorf("replacement is not a valid key: %q", k)
	}
	// And the replacement is what is now stored, so it is stable from here.
	back, err := newStateIn(t, dir).NodeKey()
	if err != nil || back != k {
		t.Error("the replacement was not persisted")
	}
}

func newStateIn(t *testing.T, dir string) *State {
	t.Helper()
	st, err := LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
