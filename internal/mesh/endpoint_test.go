package mesh

import (
	"net/netip"
	"testing"

	"github.com/vpavlin/shrooms/internal/v4"
)

// The bug that took a mesh down for fourteen minutes. A peer announces every
// address it has, including LAN ones, and taking the head of the list meant a
// VPS could choose 192.168.0.151 — routable only from the announcer's house —
// over the public address sitting next to it.
func TestBootstrapPrefersRoutableAddress(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{
			name: "lan listed first",
			in:   []string{"192.168.0.151:51820", "178.213.45.235:51820"},
			want: "178.213.45.235:51820",
		},
		{
			name: "public already first",
			in:   []string{"178.213.45.235:51820", "192.168.0.151:51820"},
			want: "178.213.45.235:51820",
		},
		{
			name: "link-local skipped",
			in:   []string{"169.254.3.4:51820", "203.0.113.9:51820"},
			want: "203.0.113.9:51820",
		},
		{
			// A private address the guess cannot justify.
			//
			// This case used to expect "10.0.0.5:51820", under the heading
			// "private is better than nothing" and the reasoning that two
			// machines on one LAN have nothing else. The reasoning is right and
			// is still honoured — see the same-LAN case in
			// bootstrapguess_test.go — but this test never set that situation
			// up: neither candidate is on the test machine's network, so it was
			// asserting "take the first private address, wherever it is".
			//
			// That is how a laptop came to dial a pi5 at 10.222.140.253, a
			// carrier-NAT address first in its announce and reachable from
			// nowhere. Announcing nothing is the better answer: SetPeers then
			// leaves the endpoint WireGuard learned alone.
			name: "a private address on nobody's network is not a guess",
			in:   []string{"10.0.0.5:51820", "192.168.1.9:51820"},
			want: "",
		},
		{
			name: "ipv6 global preferred over private v4",
			in:   []string{"10.0.0.5:51820", "[2001:db8::1]:51820"},
			want: "[2001:db8::1]:51820",
		},
		{name: "none", in: nil, want: ""},
		{
			name: "unparseable ignored",
			in:   []string{"not-an-address", "203.0.113.9:51820"},
			want: "203.0.113.9:51820",
		},
	}

	for _, c := range cases {
		if got := bootstrapEndpoint(c.in); got != c.want {
			t.Errorf("%s: bootstrapEndpoint(%v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Our own addresses must never be announced as places to reach us: the overlay
// address is circular, and so is the synthetic IPv4 alias, which lives on the
// same tunnel interface. A peer that tried one would be dialling through the
// tunnel it is trying to build.
func TestLocalAddrsSkipOurOwnOverlay(t *testing.T) {
	for _, addr := range []string{"fd7b:15fb:5ec1:f228::1", "198.19.56.185", "198.18.0.1"} {
		ip := netip.MustParseAddr(addr)
		skipped := (ip.Is6() && ip.As16()[0] == 0xfd) || v4.Prefix.Contains(ip)
		if !skipped {
			t.Errorf("%s would be announced as an endpoint", addr)
		}
	}
	// An ordinary LAN address still is.
	if v4.Prefix.Contains(netip.MustParseAddr("192.168.0.125")) {
		t.Error("a LAN address was treated as a synthetic alias")
	}
}
