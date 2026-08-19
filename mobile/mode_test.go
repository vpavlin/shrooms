package mobile

import (
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

// A phone must not wake up as a Core node.
//
// Core carries gossip for the whole network at roughly 20 MB/h — somebody's
// mobile data — and holding a gossip mesh needs stable connectivity, which is
// what a phone has least of. It inherited Core from state.DefaultConfig, where
// that default is right because a server should carry the network.
//
// Through Init rather than phoneDefaults, because what matters is what a fresh
// install actually writes, not what a helper returns.
func TestAFreshPhoneIsAnEdgeNode(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("test-phone", dir); err != nil {
		t.Fatal(err)
	}
	if got := Mode(dir); got != state.ModeEdge {
		t.Errorf("a fresh install runs in %q mode, want %q", got, state.ModeEdge)
	}
}

// And a device already configured keeps what it has: changing the default must
// not silently re-mode a phone that deliberately runs Core.
func TestAnExistingChoiceSurvives(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("test-phone", dir); err != nil {
		t.Fatal(err)
	}
	if err := SetMode(dir, state.ModeCore); err != nil {
		t.Fatal(err)
	}
	if got := Mode(dir); got != state.ModeCore {
		t.Fatalf("SetMode did not stick: %q", got)
	}
}
