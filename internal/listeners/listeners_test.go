package listeners

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// The byte order is the part that is easy to get wrong and impossible to
// notice: /proc prints each 32-bit word in host order, so on a little-endian
// machine every group of four bytes is reversed relative to the address as
// written. This case is taken from a real /proc/net/tcp6 next to the address
// the daemon reported for the same interface.
func TestParseLocalUnscramblesTheAddress(t *testing.T) {
	addr, port, ok := parseLocal("E9FF3BFDA781810FB169BC18697EBB09:0050")
	if !ok {
		t.Fatal("did not parse a line taken from a live kernel")
	}
	want := netip.MustParseAddr("fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69")
	if addr != want {
		t.Errorf("address is %v, wanted %v", addr, want)
	}
	if port != 80 {
		t.Errorf("port is %d", port)
	}
}

func TestParseLocalRejectsRubbish(t *testing.T) {
	for _, s := range []string{"", "nonsense", "ABCD:0050", "E9FF3BFD:zzzz"} {
		if _, _, ok := parseLocal(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
}

// The whole safety property: a socket on :: is reachable from every network the
// device is on, so announcing it as a mesh service would be a lie about who can
// reach it.
func TestOnlySocketsOnOurOwnAddressesCount(t *testing.T) {
	mine := netip.MustParseAddr("fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69")
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp6")
	// Column 1 is the local address, column 3 the state. 0A is LISTEN.
	fixture := "  sl  local_address remote_address st\n" +
		// ours, listening, port 22
		"   0: E9FF3BFDA781810FB169BC18697EBB09:0016 00000000000000000000000000000000:0000 0A\n" +
		// the wildcard, listening, port 8080 — not ours
		"   1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A\n" +
		// ours, but an established connection rather than a listener
		"   2: E9FF3BFDA781810FB169BC18697EBB09:0050 E9FF3BFDA781810FB169BC18697EBB09:9999 01\n"
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := parseProc(path, "tcp", map[netip.Addr]bool{mine: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("found %v, wanted only the listener on our own address", got)
	}
	if got[0].Port != 22 || got[0].Name != "ssh" {
		t.Errorf("found %+v", got[0])
	}
	if got[0].Spec() != "ssh:22" {
		t.Errorf("spec is %q", got[0].Spec())
	}
}

// A port with no well-known name is still announced, by number: the port is
// what a peer connects to, and the name only helps it read.
func TestAnUnnamedPortIsStillUsable(t *testing.T) {
	l := Listener{Port: 4711, Proto: "tcp"}
	if l.Spec() != "port-4711:4711" {
		t.Errorf("spec is %q", l.Spec())
	}
}

// Missing tables mean "nothing is bound", not an error: a container without
// IPv6 in /proc is an ordinary place to run this.
func TestAMissingTableIsNotAnError(t *testing.T) {
	got, err := parseProc(filepath.Join(t.TempDir(), "absent"), "tcp", nil, true)
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}
