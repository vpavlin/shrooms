package disco

import (
	"net/netip"
	"testing"
)

// A pong carries the address the peer says it observed us at, and whatever it
// said was stored and then advertised to every other peer as somewhere we can
// be reached. Disco authenticates mesh membership, not device identity, so a
// hostile member could seed addresses of its choosing — and if it named a third
// party, every honest peer would probe there.
//
// This does not make the value trustworthy. It refuses the ones that could not
// be an external view of this device, which removes the arbitrary-target case.
func TestReflexiveAddressesThatCannotBeOurs(t *testing.T) {
	refuse := []string{
		"0.0.0.0:51820",      // unspecified
		"127.0.0.1:51820",    // loopback: aims a probe at the prober
		"[::1]:51820",        //
		"224.0.0.1:51820",    // multicast
		"169.254.10.1:51820", // link-local
		"[fe80::1]:51820",    //
		"198.51.100.7:0",     // no port to reach us on
	}
	for _, s := range refuse {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if usableReflexive(ap) {
			t.Errorf("%s was accepted as an address peers observe us at", s)
		}
	}
	if usableReflexive(netip.AddrPort{}) {
		t.Error("the zero address was accepted")
	}
}

// Private and carrier-NAT addresses are kept deliberately: a peer on the same
// LAN observes us at a private address, and that is the most useful candidate
// this ever collects — the one whose loss sends two machines in one room
// through a relay.
func TestReflexiveKeepsTheAddressesThatMatter(t *testing.T) {
	keep := []string{
		"192.168.0.151:51820", // a peer on our LAN sees this
		"10.1.2.3:51820",      //
		"100.64.0.5:51820",    // carrier NAT: what a phone is behind
		"198.51.100.7:51820",  // an ordinary public address
		"[2001:db8::1]:51820", // global v6
	}
	for _, s := range keep {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !usableReflexive(ap) {
			t.Errorf("%s was refused; it is exactly what reflexive discovery is for", s)
		}
	}
}
