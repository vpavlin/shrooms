package mesh

import (
	"net/netip"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/v4"
)

// Resolve maps a device name to its overlay address.
//
// Used by the DNS server. Case-insensitive and sanitised the same way the hosts
// file renders names, so what resolves is exactly what is written there —
// otherwise a name would work in one and not the other.
//
// A name claimed by more than one peer resolves to the one heard from most
// recently.
//
// This used to resolve to none of them, reasoning that names are self-asserted
// (ADR-008) and picking one silently could send traffic to a machine the user
// did not mean. What that missed is the ordinary way a name ends up claimed
// twice: one device, whose identity changed — re-enrolled, re-installed, or
// given a per-mesh identity by ADR-015 — announcing under a key the roster has
// not seen before while the old entry sits there for its remaining hours of
// ForgetAfter. Both entries are the same machine, one of them is dead, and
// refusing to answer means the name stops working for up to six hours after a
// device is re-added. That is what "k11.mesh resolves to nothing while k11 is
// a direct peer with a fresh handshake" was.
//
// The freshest announce is the live device, and where two genuinely different
// machines share a name it is the one still running. The qualified
// `<peer>.<mesh>` form and the address itself work regardless.
func (m *Mesh) Resolve(host string) (netip.Addr, bool) {
	want := sanitiseName(host)
	if want == "" {
		return netip.Addr{}, false
	}

	var found netip.Addr
	var freshest time.Time
	for _, p := range m.roster.Peers() {
		if sanitiseName(p.Name) != want {
			continue
		}
		if found.IsValid() && !p.LastSeen.After(freshest) {
			continue
		}
		found, freshest = p.Overlay, p.LastSeen
	}
	return found, found.IsValid()
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
// SanitiseName reduces a name to the label that will actually resolve.
//
// Exported because anything that *stores* a name should store the form that
// works: a device called "Living Room NAS" resolves as living-room-nas, and a
// config holding the original is a device whose name works in the roster and
// not in a browser.
func SanitiseName(s string) string { return sanitiseName(s) }

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

// QualifiedDNSName is the name that resolves to *this* mesh's address for a
// device, on a node that has more than one.
//
// The short form is answered by the primary mesh alone, so a name built from
// the device alone points at an address on whichever mesh happens to be first
// — a different machine's network, where the thing being named is not
// listening. That has now been the same bug in three places: `shrooms bound`
// printed it, the desktop panel built service URLs from it, and the phone
// showed a bound ssh port as laptop.mesh:22 when the port was on another mesh
// entirely.
//
// Each component is sanitised on its own and then joined. Sanitising
// "laptop.test" as one string turns the separator into a hyphen and yields
// laptop-test.mesh, which resolves to nothing at all.
//
// An empty label gives the short form, which is what a single-mesh node wants
// and what every older payload meant.
func QualifiedDNSName(name, label, suffix string) string {
	host := sanitiseName(name)
	if host == "" {
		return ""
	}
	if suffix == "" {
		suffix = "mesh"
	}
	suffix = strings.Trim(suffix, ".")
	if l := sanitiseName(label); l != "" {
		return host + "." + l + "." + suffix
	}
	return host + "." + suffix
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

// SetV4 attaches the synthetic IPv4 table, so names can be answered with A
// records as well as AAAA (ADR-021).
func (m *Mesh) SetV4(t *v4.Table) { m.v4 = t }

// LookupV4 resolves a mesh name to its synthetic IPv4 address.
//
// Separate from Lookup rather than folded into it, because the two answer
// different questions: Lookup says where a name is on the overlay, and this
// says what this device calls that place when it has to speak IPv4.
func (m *Mesh) LookupV4(overlay netip.Addr) (netip.Addr, bool) {
	if m.v4 == nil {
		return netip.Addr{}, false
	}
	return m.v4.Alias(overlay)
}
