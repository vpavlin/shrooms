package v4

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
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

// The table is fed the current roster on every change, and the roster prunes.
// Nothing here ever did, so the table grew for the life of the process and the
// block filled with devices that had long gone.
func TestAliasesAreReclaimedAfterTheGracePeriod(t *testing.T) {
	self := Entry{Overlay: netip.MustParseAddr("fd00::1"), DevicePub: []byte("self")}
	peer := Entry{Overlay: netip.MustParseAddr("fd00::2"), DevicePub: []byte("peer")}
	tab := NewTableIn(netip.MustParsePrefix("198.18.0.0/19"), self, []Entry{self, peer})

	if _, ok := tab.Alias(peer.Overlay); !ok {
		t.Fatal("the peer never got an alias")
	}
	start := time.Now()

	// Gone from the roster, but only just. An alias must not move underneath a
	// live connection, and a flap or a reboot must not count.
	tab.UpdateAt([]Entry{self}, start.Add(AliasGrace-time.Minute))
	if _, ok := tab.Alias(peer.Overlay); !ok {
		t.Error("an alias was reclaimed while the peer had only briefly left")
	}

	// Long gone: the address goes back to the block.
	tab.UpdateAt([]Entry{self}, start.Add(AliasGrace+time.Minute))
	if a, ok := tab.Alias(peer.Overlay); ok {
		t.Errorf("the peer kept %v long after leaving the roster", a)
	}

	// Coming back gets an alias again, and this device's own never moves.
	own, _ := tab.Alias(self.Overlay)
	tab.UpdateAt([]Entry{self, peer}, start.Add(AliasGrace+2*time.Minute))
	if _, ok := tab.Alias(peer.Overlay); !ok {
		t.Error("a returning peer got no alias")
	}
	if again, _ := tab.Alias(self.Overlay); again != own {
		t.Errorf("this device's own alias moved from %v to %v", own, again)
	}
}

// A full block must be countable. The symptom otherwise is a name that answers
// over IPv6 and returns NODATA for A, which reads as a browser problem.
func TestAFullBlockIsCounted(t *testing.T) {
	self := Entry{Overlay: netip.MustParseAddr("fd00::1"), DevicePub: []byte("self")}
	// A /30 leaves almost nothing to hand out, so the peers below cannot fit.
	tab := NewTableIn(netip.MustParsePrefix("198.18.0.0/30"), self, []Entry{self})
	var peers []Entry
	for i := 2; i < 40; i++ {
		peers = append(peers, Entry{
			Overlay:   netip.MustParseAddr(fmt.Sprintf("fd00::%d", i)),
			DevicePub: []byte(fmt.Sprintf("peer-%d", i)),
		})
	}
	tab.Update(peers)
	if tab.Exhausted() == 0 {
		t.Error("the block filled and nothing counted it")
	}
}

// Alias used to recurse on nonce+1 with no termination. nonce is a uint8, so on
// a block with no usable address it wrapped and recursed until the stack ran
// out — a crash, not a bad answer. Found by trying to fill a small block.
func TestAliasGivesUpOnABlockWithNothingToGive(t *testing.T) {
	done := make(chan netip.Addr, 1)
	go func() {
		done <- Alias(netip.MustParsePrefix("198.18.0.0/30"), []byte("peer"), 0)
	}()
	select {
	case a := <-done:
		if a.IsValid() {
			t.Errorf("invented %v inside a /30 that cannot hold it", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Alias did not terminate on a block it cannot satisfy")
	}
}
