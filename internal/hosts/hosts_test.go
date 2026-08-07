package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() []Entry {
	return []Entry{
		{Name: "laptop", Addr: "fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69"},
		{Name: "vps", Addr: "fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf"},
	}
}

func TestRenderIncludesSelfAndPeers(t *testing.T) {
	out := Render(sample(), "mesh")

	for _, want := range []string{
		"fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69  laptop laptop.mesh",
		"fd3b:ffe9:f81:6f18:41e:c574:c529:5bbf  vps vps.mesh",
		Begin, End,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Names are self-asserted in the announce, so two devices can claim the same
// one. Silently letting the last win would point ssh at the wrong machine.
func TestDuplicateNamesDisambiguated(t *testing.T) {
	st := append(sample(), Entry{Name: "vps", Addr: "fd3b:ffe9:f81:aaaa:1:2:3:dead"})

	out := Render(st, "mesh")
	if strings.Count(out, " vps ") > 1 {
		t.Fatalf("two entries claim the bare name 'vps':\n%s", out)
	}
	if !strings.Contains(out, "dead") {
		t.Errorf("duplicate was not disambiguated by address:\n%s", out)
	}
}

func TestNamesSanitised(t *testing.T) {
	st := []Entry{{Name: "My Laptop_2!", Addr: "fd00::1"}}

	out := Render(st, "mesh")
	if !strings.Contains(out, "my-laptop-2") {
		t.Errorf("name not sanitised into a usable hostname:\n%s", out)
	}
	if strings.Contains(out, "!") || strings.Contains(out, "My") {
		t.Errorf("unsafe characters survived:\n%s", out)
	}
}

func TestSuffixOptional(t *testing.T) {
	out := Render(sample(), "")
	if strings.Contains(out, "laptop.") {
		t.Errorf("suffix applied when none was asked for:\n%s", out)
	}
	if !strings.Contains(out, "  laptop\n") {
		t.Errorf("bare name missing:\n%s", out)
	}
}

// The block must be replaced in place. Mangling the rest of /etc/hosts would
// break name resolution for the whole machine.
func TestReplaceBlockPreservesSurroundings(t *testing.T) {
	existing := "127.0.0.1 localhost\n::1 localhost\n\n" +
		Begin + "\nfd00::9  stale stale.mesh\n" + End + "\n" +
		"10.0.0.5 something-else\n"

	out, err := ReplaceBlock(existing, Begin+"\nfd00::1  fresh fresh.mesh\n"+End+"\n")
	if err != nil {
		t.Fatalf("replaceBlock: %v", err)
	}
	for _, want := range []string{"127.0.0.1 localhost", "10.0.0.5 something-else", "fresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale") {
		t.Errorf("old entry survived:\n%s", out)
	}
	if strings.Count(out, Begin) != 1 {
		t.Errorf("block duplicated:\n%s", out)
	}
}

func TestReplaceBlockAppendsWhenAbsent(t *testing.T) {
	out, err := ReplaceBlock("127.0.0.1 localhost\n", Begin+"\nfd00::1  a\n"+End+"\n")
	if err != nil {
		t.Fatalf("replaceBlock: %v", err)
	}
	if !strings.HasPrefix(out, "127.0.0.1 localhost\n") {
		t.Errorf("existing content not preserved at the top:\n%s", out)
	}
	if !strings.Contains(out, Begin) {
		t.Errorf("block not appended:\n%s", out)
	}
}

// A half-written block must be reported, not silently duplicated — that would
// leave two conflicting sets of entries.
func TestReplaceBlockRejectsTruncated(t *testing.T) {
	if _, err := ReplaceBlock("x\n"+Begin+"\nfd00::1 a\n", "new"); err == nil {
		t.Fatal("accepted a block with no end marker")
	}
}

func TestReplaceBlockIsIdempotent(t *testing.T) {
	block := Begin + "\nfd00::1  a\n" + End + "\n"
	once, _ := ReplaceBlock("127.0.0.1 localhost\n", block)
	twice, _ := ReplaceBlock(once, block)
	if once != twice {
		t.Errorf("running twice changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// applyForTest adapts Apply's two return values for the existing assertions.
func applyForTest(path, block string) error {
	_, err := Apply(path, block)
	return err
}

func TestUpdateHostsFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	block := Render(sample(), "mesh")
	if err := applyForTest(path, block); err != nil {
		t.Fatalf("updateHostsFile: %v", err)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "vps.mesh") || !strings.Contains(string(got), "127.0.0.1 localhost") {
		t.Errorf("unexpected result:\n%s", got)
	}

	// No temporary files left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("expected only the hosts file, found %d entries", len(ents))
	}
}
