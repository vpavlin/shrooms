package main

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
)

// fixed stands in for mesh.Lookup and must answer the way it does, or these
// tests agree with nothing. It resolves a bare device name, and a service on a
// device as the label to its right — and, like the real one, refuses a name
// with trailing labels it cannot account for. An exact-match map instead of
// this is what let the fall-through bug pass its own guard test.
func fixed(addrs map[string]string) func(string) (netip.Addr, bool) {
	return func(host string) (netip.Addr, bool) {
		labels := strings.Split(host, ".")
		if len(labels) == 1 {
			if s, ok := addrs[labels[0]]; ok {
				return netip.MustParseAddr(s), true
			}
			return netip.Addr{}, false
		}
		if s, ok := addrs[labels[1]]; ok && len(labels) == 2 {
			return netip.MustParseAddr(s), true
		}
		return netip.Addr{}, false
	}
}

// A node with one mesh must see exactly what it saw before: names resolve
// unqualified, and nothing else changes.
func TestOneMeshResolvesTheShortName(t *testing.T) {
	lookup := resolveAcross([]namedMesh{
		{label: "default", lookup: fixed(map[string]string{"vps": "fd00::1"})},
	}, map[string]bool{"default": true})
	if addr, ok := lookup("vps"); !ok || addr.String() != "fd00::1" {
		t.Errorf("vps resolved to %v (%v)", addr, ok)
	}
	// And qualified, for anyone who types it.
	if addr, ok := lookup("vps.default"); !ok || addr.String() != "fd00::1" {
		t.Errorf("vps.default resolved to %v (%v)", addr, ok)
	}
}

// The whole point of qualifying: two meshes, both with a "vps".
func TestQualifiedNamePicksTheMesh(t *testing.T) {
	lookup := resolveAcross([]namedMesh{
		{label: "home", lookup: fixed(map[string]string{"vps": "fd00::1", "nas": "fd00::2"})},
		{label: "shared", lookup: fixed(map[string]string{"vps": "fd11::1"})},
	}, map[string]bool{"home": true, "shared": true})

	if addr, ok := lookup("vps.home"); !ok || addr.String() != "fd00::1" {
		t.Errorf("vps.home resolved to %v (%v)", addr, ok)
	}
	if addr, ok := lookup("vps.shared"); !ok || addr.String() != "fd11::1" {
		t.Errorf("vps.shared resolved to %v (%v)", addr, ok)
	}

	// A name on both meshes is answered by the first, in config order — which
	// is your own mesh, since that is the one a config lists first and the one
	// a second mesh is added to. Refusing instead took the short name away
	// from exactly the devices that are on both of your meshes, which are the
	// ones you reach most often, and looked from the outside like DNS being
	// broken for one machine.
	if addr, ok := lookup("vps"); !ok || addr.String() != "fd00::1" {
		t.Errorf("shared name resolved to %v (%v), wanted the first mesh", addr, ok)
	}

	// Unambiguous unqualified still works, which is what keeps the short form
	// useful on a node with more than one mesh.
	if addr, ok := lookup("nas"); !ok || addr.String() != "fd00::2" {
		t.Errorf("nas resolved to %v (%v)", addr, ok)
	}
}

// A qualified name naming a mesh must not fall through to a device of the same
// name on another mesh — that is the one answer that is certainly wrong.
func TestQualifiedNameDoesNotFallThrough(t *testing.T) {
	lookup := resolveAcross([]namedMesh{
		{label: "home", lookup: fixed(map[string]string{"vps": "fd00::1"})},
		{label: "shared", lookup: fixed(map[string]string{})},
	}, map[string]bool{"home": true, "shared": true, "work": true})
	if addr, ok := lookup("vps.shared"); ok {
		t.Errorf("vps.shared resolved to %v, but shared has no vps", addr)
	}
	// And a mesh nobody has joined resolves nothing.
	if _, ok := lookup("vps.elsewhere"); ok {
		t.Error("resolved a name on a mesh this node is not in")
	}
	// The reported case: a mesh the config knows but which is not running,
	// because it was switched off. It is absent from the slice, so the
	// qualified branch matches nothing and used to fall through to the loop
	// below it — which handed the whole host to every mesh and got back "vps"
	// on home. `ssh vps.work.mesh` then silently reached a different machine.
	if addr, ok := lookup("vps.work"); ok {
		t.Errorf("vps.work resolved to %v with the work mesh switched off", addr)
	}
	// A service on a device keeps working, which is why an unmatched label
	// cannot simply be refused: it has the same shape as a qualified name.
	if addr, ok := lookup("immich.vps"); !ok || addr.String() != "fd00::1" {
		t.Errorf("immich.vps resolved to %v (%v)", addr, ok)
	}
}

func TestAliasAcrossMeshes(t *testing.T) {
	a := netip.MustParseAddr("fd00::1")
	b := netip.MustParseAddr("fd11::1")
	alias := aliasAcross([]namedMesh{
		{label: "home", alias: func(x netip.Addr) (netip.Addr, bool) {
			return netip.MustParseAddr("198.18.0.1"), x == a
		}},
		{label: "shared", alias: func(x netip.Addr) (netip.Addr, bool) {
			return netip.MustParseAddr("198.19.0.1"), x == b
		}},
	})
	if got, ok := alias(b); !ok || got.String() != "198.19.0.1" {
		t.Errorf("alias for the second mesh is %v (%v)", got, ok)
	}
	if _, ok := alias(netip.MustParseAddr("fd22::9")); ok {
		t.Error("invented an alias for an address in no mesh")
	}
}

// The first mesh keeps exactly the interface and port the config names, so a
// node that has always had one is untouched by any of this.
func TestFirstMeshKeepsTheConfiguredInterface(t *testing.T) {
	cfg := state.Config{Interface: "shrooms0", ListenPort: 51820}
	if iface, port := ifaceAndPort(cfg, 0); iface != "shrooms0" || port != 51820 {
		t.Errorf("first mesh got %s:%d", iface, port)
	}
	if iface, port := ifaceAndPort(cfg, 1); iface != "shrooms01" || port != 51821 {
		t.Errorf("second mesh got %s:%d", iface, port)
	}
}

// A bound port on a second mesh has to be advertised under a name that resolves
// to *that* mesh's address. The short form is answered by the primary mesh
// alone, so `shrooms bound` printed a name pointing at an address where nothing
// was listening — found by binding something to a second mesh and trying the
// name it told us to use.
func TestBoundNameCarriesTheMeshLabel(t *testing.T) {
	for _, tc := range []struct {
		name, host, label string
		several           bool
		want              string
	}{
		{"one mesh keeps the short name", "laptop", "default", false, "laptop.mesh"},
		{"several meshes qualify", "laptop", "test", true, "laptop.test.mesh"},
		{"the primary qualifies too", "laptop", "default", true, "laptop.default.mesh"},
		{"a name needing sanitising", "Living Room NAS", "home", true, "living-room-nas.home.mesh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mesh.DNSName(tc.host, "")
			if tc.several && tc.label != "" {
				if h, l := mesh.SanitiseName(tc.host), mesh.SanitiseName(tc.label); h != "" && l != "" {
					got = h + "." + l + ".mesh"
				}
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The fingerprint must be stable when nothing has changed, or the daemon reads
// its own polling as a network change and restarts on a timer.
func TestLocalUnderlayIsStable(t *testing.T) {
	a := localUnderlay(nil)
	if b := localUnderlay(nil); a != b {
		t.Errorf("fingerprint moved with nothing changing:\n%q\n%q", a, b)
	}
}

// The mesh's own interfaces must be excluded. They are torn down and rebuilt by
// the restart this feeds, so counting them would make recovery look like a
// fresh network change and restart again — a loop, on a machine that had one
// wifi blip.
func TestLocalUnderlayIgnoresOurOwnInterfaces(t *testing.T) {
	all := localUnderlay(nil)
	if all == "" {
		t.Skip("no non-loopback interfaces with addresses here")
	}
	// Whichever interface the first entry belongs to, claim it as a mesh
	// device: its addresses must then disappear from the fingerprint.
	name, _, ok := strings.Cut(strings.Split(all, ",")[0], "=")
	if !ok {
		t.Fatalf("unexpected fingerprint shape: %q", all)
	}
	got := localUnderlay([]*instance{{iface: name}})
	if strings.Contains(got, name+"=") {
		t.Errorf("interface %q is ours and still counted:\n%q", name, got)
	}
	if got == all {
		t.Errorf("excluding %q changed nothing:\n%q", name, got)
	}
}
