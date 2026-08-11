package main

import (
	"net/netip"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

func fixed(addrs map[string]string) func(string) (netip.Addr, bool) {
	return func(host string) (netip.Addr, bool) {
		s, ok := addrs[host]
		if !ok {
			return netip.Addr{}, false
		}
		return netip.MustParseAddr(s), true
	}
}

// A node with one mesh must see exactly what it saw before: names resolve
// unqualified, and nothing else changes.
func TestOneMeshResolvesTheShortName(t *testing.T) {
	lookup := resolveAcross([]namedMesh{
		{label: "default", lookup: fixed(map[string]string{"vps": "fd00::1"})},
	})
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
	})

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
	})
	if addr, ok := lookup("vps.shared"); ok {
		t.Errorf("vps.shared resolved to %v, but shared has no vps", addr)
	}
	// And a mesh nobody has joined resolves nothing.
	if _, ok := lookup("vps.elsewhere"); ok {
		t.Error("resolved a name on a mesh this node is not in")
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
