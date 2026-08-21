package hosts

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The failure this exists for, taken from a real machine: /etc/hosts said a
// peer was at an address it had not held for weeks, and because
// systemd-resolved answers from that file ahead of any registered resolver, the
// stale answer won. Nothing anywhere reported it.
func TestAMovedPeerIsReported(t *testing.T) {
	p := write(t, Begin+"\n"+
		"fd3b::old  nothing nothing.mesh\n"+
		End+"\n")

	bad := Stale(p, []Entry{{Name: "nothing", Addr: "fd3b::new"}}, "mesh")
	if len(bad) != 1 {
		t.Fatalf("reported %d disagreements, want 1: %+v", len(bad), bad)
	}
	if bad[0].Has != "fd3b::old" || bad[0].Wants != "fd3b::new" {
		t.Errorf("got %+v", bad[0])
	}
}

// One device appears under several names. Counting each as its own problem
// makes a single stale entry read as two or three, which misrepresents how much
// is wrong.
func TestNameVariantsCollapseToOneProblem(t *testing.T) {
	// The four names a labelled entry really renders as, so the test exercises
	// what the writer produces rather than a shape invented here.
	was := []Entry{{Name: "nas", Mesh: "default", Addr: "fd3b::old"}}
	p := write(t, Render(was, "mesh"))

	bad := Stale(p, []Entry{{Name: "nas", Mesh: "default", Addr: "fd3b::new"}}, "mesh")
	if len(bad) != 1 {
		t.Fatalf("one moved device produced %d problems: %+v", len(bad), bad)
	}
	if bad[0].Name != "nas" {
		t.Errorf("reported under %q, want the shortest name", bad[0].Name)
	}
}

// A block that agrees is silent. This is the common case and a warning that
// cried wolf would be worse than none.
func TestAnAgreeingBlockIsSilent(t *testing.T) {
	entries := []Entry{{Name: "nas", Addr: "fd3b::1", AddrV4: "198.19.0.1"}}
	p := write(t, Render(entries, "mesh"))
	if bad := Stale(p, entries, "mesh"); len(bad) != 0 {
		t.Errorf("a freshly written block reported as stale: %+v", bad)
	}
}

// A name the mesh no longer serves at all — a peer that left, or a block
// written under a different suffix. Still worth reporting: the file is still
// answering for it.
func TestANameTheMeshNoLongerServesIsReported(t *testing.T) {
	p := write(t, Begin+"\n"+"fd3b::1  ghost ghost.mesh\n"+End+"\n")
	bad := Stale(p, []Entry{{Name: "nas", Addr: "fd3b::2"}}, "mesh")
	if len(bad) != 1 {
		t.Fatalf("reported %d, want 1: %+v", len(bad), bad)
	}
	if bad[0].Wants != "" {
		t.Errorf("a departed peer should want nothing, got %q", bad[0].Wants)
	}
}

// The self entry deliberately omits the bare name, because it would shadow the
// machine's own hostname — a host resolves that to 127.0.1.1 and daemons expect
// a local address there. A block from before that rule still carries it, and
// that is worth reporting rather than treating as cosmetic.
func TestABareSelfNameLeftByAnOlderBuildIsReported(t *testing.T) {
	p := write(t, Begin+"\n"+"fd3b::1  laptop laptop.mesh\n"+End+"\n")
	bad := Stale(p, []Entry{{Name: "laptop", Addr: "fd3b::1", Self: true}}, "mesh")
	if len(bad) != 1 {
		t.Fatalf("reported %d, want 1: %+v", len(bad), bad)
	}
	if bad[0].Name != "laptop" {
		t.Errorf("reported %q, want the bare name", bad[0].Name)
	}
}

// No file, no block, no problem. A machine that has never had one managed must
// not be told anything is wrong with it.
func TestNoFileAndNoBlockAreQuiet(t *testing.T) {
	if bad := Stale(filepath.Join(t.TempDir(), "absent"), nil, "mesh"); bad != nil {
		t.Errorf("a missing file reported %+v", bad)
	}
	p := write(t, "127.0.0.1 localhost\n")
	if bad := Stale(p, []Entry{{Name: "nas", Addr: "fd3b::1"}}, "mesh"); bad != nil {
		t.Errorf("a file with no managed block reported %+v", bad)
	}
}
