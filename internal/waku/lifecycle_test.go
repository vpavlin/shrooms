package waku

import (
	"testing"
)

// A node can be replaced inside a running process: closed, and another made
// and started. Destroying without stopping left the library's persistency
// singleton behind, and the next node failed to start ("Persistency already
// initialised …"), which is why a node was only ever replaced by ending the
// whole process. Needs the library, as every test of this package does; it
// does not need to reach any peer.
func TestANodeCanBeMadeAgainAfterClose(t *testing.T) {
	for round := 1; round <= 3; round++ {
		n, err := New(Config{"mode": "Edge", "preset": "logos.test"})
		if err != nil {
			t.Fatalf("round %d: new: %v", round, err)
		}
		if err := n.Start(); err != nil {
			t.Fatalf("round %d: start: %v", round, err)
		}
		if err := n.Close(); err != nil {
			t.Fatalf("round %d: close: %v", round, err)
		}
	}
}
