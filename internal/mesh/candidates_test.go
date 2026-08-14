package mesh

import (
	"strings"
	"testing"
)

// A node behind endpoint-dependent NAT collects a reflexive address per peer,
// and those are ordered ahead of local addresses. With the cap at four, its LAN
// address fell off the end — so peers in the same building never learned it,
// could not probe it, and reached a machine one room away through a relay.
func TestLANAddressSurvivesTruncation(t *testing.T) {
	in := []string{
		"198.51.100.7:51823", // reflexive, one per peer under symmetric NAT
		"198.51.100.7:51824",
		"198.51.100.7:51825",
		"198.51.100.7:51826",
		"192.168.0.125:51820", // the one that matters at home, ordered last
	}

	out := reserveLocal(in)
	if len(out) > 4 {
		out = out[:4]
	}

	if !contains(out, "192.168.0.125:51820") {
		t.Fatalf("LAN address did not survive: %v", out)
	}
	// The first slot stays with an address reachable from outside: a peer that
	// cannot reach us at all is worse off than one paying for a hairpin.
	if isPrivate(out[0]) {
		t.Errorf("first candidate should be an outside address, got %q", out[0])
	}
}

// Only the ordering changes. Dropping a reflexive address to make room would
// cost reachability for a peer that can only use that one.
func TestReserveLocalKeepsEveryAddress(t *testing.T) {
	in := []string{"198.51.100.7:51823", "198.51.100.7:51824", "10.0.0.5:51820"}
	out := reserveLocal(in)

	if len(out) != len(in) {
		t.Fatalf("lost an address: %v -> %v", in, out)
	}
	for _, want := range in {
		if !contains(out, want) {
			t.Errorf("%s missing from %v", want, out)
		}
	}
}

// A private address already near the front needs no help, and reordering it
// would churn the announce for nothing.
func TestReserveLocalLeavesSafeOrdersAlone(t *testing.T) {
	for _, in := range [][]string{
		{"192.168.0.125:51820", "198.51.100.7:51823"},
		{"198.51.100.7:51823", "192.168.0.125:51820", "198.51.100.7:51824"},
		{"198.51.100.7:51823"}, // nothing private at all
	} {
		out := reserveLocal(in)
		if strings.Join(out, ",") != strings.Join(in, ",") {
			t.Errorf("reordered %v into %v", in, out)
		}
	}
}

// A peer on the same LAN observes us at a private address and reports it, so a
// reflexive address is not necessarily an outside one — classifying by source
// rather than by address would count it as one and reserve a second slot.
func TestPrivateReflexiveCountsAsLocal(t *testing.T) {
	in := []string{"192.168.0.125:51820", "198.51.100.7:51823", "198.51.100.7:51824", "198.51.100.7:51825"}
	out := reserveLocal(in)
	if strings.Join(out, ",") != strings.Join(in, ",") {
		t.Errorf("reordered an already-safe list: %v", out)
	}
}

// Carrier-grade NAT space is not a LAN: reserving a slot for it would spend the
// reservation on an address no peer can reach us at.
func TestCGNATIsNotPrivate(t *testing.T) {
	if isPrivate("100.64.0.1:51820") {
		t.Error("100.64.0.0/10 is carrier NAT, not a private network")
	}
	if !isPrivate("[fd00:1234::1]:51820") {
		t.Error("unique local addresses are private")
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
