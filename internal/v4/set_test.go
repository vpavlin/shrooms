package v4

import "testing"

// A mesh's block is a property of the SET it was assigned in, not of the mesh.
//
// Two meshes can prefer the same tag — there are only 1<<MeshBits of them — and
// the loser is probed onto the next free one. So adding or removing any mesh,
// including one that is switched off, can move a different mesh's block.
//
// That is fine as long as every caller assigns over the same set, and it is a
// silent catastrophe when they do not: the Android tunnel installs the route
// for one block while the running mesh translates into another, so every
// synthetic IPv4 address the resolver hands out points somewhere the tun does
// not carry. IPv6 keeps working, which is what makes it so hard to see — the
// terminal resolves and connects over v6 and looks perfect, while a browser
// races A against AAAA and fails whenever the A wins.
func TestABlockDependsOnTheWholeSet(t *testing.T) {
	// Two ids that want the same tag, found by search rather than asserted:
	// the constants would rot the moment blockTag changed.
	a, b := "", ""
	seen := map[uint32]string{}
	for i := 0; i < 1000 && b == ""; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		if other, ok := seen[blockTag(id)]; ok {
			a, b = other, id
			break
		}
		seen[blockTag(id)] = id
	}
	if b == "" {
		t.Skip("no collision found; MeshBits must have grown")
	}
	if a > b {
		a, b = b, a // Blocks probes in sorted order, so the later one moves
	}

	alone := Blocks([]string{b})[b]
	together := Blocks([]string{a, b})[b]

	if alone == together {
		t.Fatalf("%q kept %v in both sets; this test no longer demonstrates anything", b, alone)
	}
	t.Logf("%q: %v alone, %v alongside %q — callers must agree on the set", b, alone, together, a)
}
