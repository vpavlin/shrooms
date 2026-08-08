package mesh

import (
	"net/netip"
	"strings"
)

// Resolve maps a device name to its overlay address.
//
// Used by the DNS server. Case-insensitive and sanitised the same way the hosts
// file renders names, so what resolves is exactly what is written there —
// otherwise a name would work in one and not the other.
//
// A name claimed by more than one peer resolves to none of them. Names are
// self-asserted (ADR-008), and silently picking one would send traffic to a
// machine the user did not mean; the qualified `<peer>.<mesh>` form and the
// address itself both still work.
func (m *Mesh) Resolve(host string) (netip.Addr, bool) {
	want := sanitiseName(host)
	if want == "" {
		return netip.Addr{}, false
	}

	var found netip.Addr
	n := 0
	for _, p := range m.roster.Peers() {
		if sanitiseName(p.Name) == want {
			found = p.Overlay
			n++
		}
	}
	if n != 1 {
		return netip.Addr{}, false
	}
	return found, true
}

// ResolveSelf answers for this device's own name, so `ping <myname>.mesh`
// behaves like every other name rather than being the one that fails.
func (m *Mesh) ResolveSelf(host string) (netip.Addr, bool) {
	if sanitiseName(host) == sanitiseName(m.cfg.Name) {
		return m.self, true
	}
	return netip.Addr{}, false
}

// sanitiseName mirrors internal/hosts: lowercase, and anything that is not a
// hostname character becomes a dash.
func sanitiseName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// DNSName is the name a peer answers to, as the resolver and the hosts file
// both render it. Empty when the name sanitises to nothing.
//
// Computed here so the app does not reimplement the sanitising and drift from
// what actually resolves.
func DNSName(name, suffix string) string {
	h := sanitiseName(name)
	if h == "" {
		return ""
	}
	if suffix == "" {
		suffix = "mesh"
	}
	return h + "." + strings.Trim(suffix, ".")
}

// Lookup is the resolver handed to the DNS server: this device first, then
// peers.
func (m *Mesh) Lookup(host string) (netip.Addr, bool) {
	if addr, ok := m.ResolveSelf(host); ok {
		return addr, true
	}
	return m.Resolve(host)
}
