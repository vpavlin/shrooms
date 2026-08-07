package mesh

import "testing"

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
			// Two machines on one LAN have nothing else, and it works.
			name: "private is better than nothing",
			in:   []string{"10.0.0.5:51820", "192.168.1.9:51820"},
			want: "10.0.0.5:51820",
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
