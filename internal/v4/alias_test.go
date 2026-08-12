package v4

import (
	"fmt"
	"testing"
)


// Two meshes on one device must never route the same block. Deriving the block
// from the network id alone collides about once in sixteen, which is rare
// enough to be mistaken for something else and common enough to happen — it is
// what turned a CI run red and what "the browser cannot reach it but curl can"
// looks like from the outside.
func TestBlocksNeverCollide(t *testing.T) {
	// Two ids that prefer the same block. Found rather than assumed, so this
	// test still means something if the derivation changes.
	var a, b string
	for i := 0; i < 4096 && b == ""; i++ {
		id := fmt.Sprintf("mesh-%d", i)
		switch {
		case a == "":
			a = id
		case Block(id) == Block(a):
			b = id
		}
	}
	if b == "" {
		t.Skip("no colliding pair found; the derivation must have changed")
	}

	got := Blocks([]string{a, b})
	if got[a] == got[b] {
		t.Errorf("both meshes were given %s", got[a])
	}
	// The first one keeps what it preferred, so a device that adds a second
	// mesh does not renumber the one it already had.
	if got[a] != Block(a) && got[b] != Block(b) {
		t.Error("neither mesh kept its preferred block")
	}
}

// The same set must always produce the same answer, whatever order it arrives
// in: a block is a routing decision, and one that moved between restarts would
// change every synthetic address on the device.
func TestBlocksAreStable(t *testing.T) {
	ids := []string{"one", "two", "three", "four"}
	first := Blocks(ids)
	second := Blocks([]string{"four", "one", "three", "two"})
	for _, id := range ids {
		if first[id] != second[id] {
			t.Errorf("%s moved from %s to %s with a different input order",
				id, first[id], second[id])
		}
	}
}
