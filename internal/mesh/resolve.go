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
//
// The name arrives with the suffix already stripped, so it is one of:
//
//	laptop                  a device
//	laptop.home             a device in a named mesh (ADR-015)
//	immich.home-server      a service on a device
//
// The last two are indistinguishable by shape, so the leftmost label is tried
// as a device name first — which keeps the mesh-qualified form working exactly
// as before — and only if that fails is it read as a service on the label to
// its right.
//
// A service resolves to its host's address and is not checked for existence:
// only the device running it knows what it publishes, and that is not
// announced (a service list would push the announce past its fixed 512-byte
// padding). So `typo.home-server.mesh` resolves and then refuses the
// connection, rather than returning NXDOMAIN. Honest enough — the name does
// point at a real machine — and it is what makes adding a service require no
// coordination with any other device.
func (m *Mesh) Lookup(host string) (netip.Addr, bool) {
	labels := strings.Split(host, ".")

	// A device, by its own name.
	if addr, ok := m.lookupDevice(labels[0]); ok {
		return addr, true
	}
	// A service on a device: the label to the right is the host.
	if len(labels) >= 2 {
		if addr, ok := m.lookupDevice(labels[1]); ok {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func (m *Mesh) lookupDevice(name string) (netip.Addr, bool) {
	if addr, ok := m.ResolveSelf(name); ok {
		return addr, true
	}
	return m.Resolve(name)
}
