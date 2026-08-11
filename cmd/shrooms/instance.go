package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"golang.zx2c4.com/wireguard/device"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/service"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
	"github.com/vpavlin/shrooms/internal/waku"
	"github.com/vpavlin/shrooms/internal/wg"
)

// One mesh, running (ADR-015).
//
// What is per mesh: a WireGuard device, a TUN, a UDP port, an identity, a
// roster, an alias table and any services published on it. What is shared: the
// process, the control socket, the resolver — and the Logos Delivery node,
// which is the expensive part and the reason this is one daemon rather than
// several.
type instance struct {
	label   string
	mesh    *mesh.Mesh
	dev     *wg.Device
	aliases *v4.Table
	self    netip.Addr
	prefix  netip.Prefix
	iface   string
	port    uint16

	services *service.Publisher
	// specs is what services was published from, so a reload can tell whether
	// anything actually changed.
	specs []string
}

// Close tears one mesh down. Safe on a partially built instance, because
// startInstance returns what it managed to build when it fails.
func (in *instance) Close() {
	if in == nil {
		return
	}
	if in.services != nil {
		in.services.Close()
	}
	if in.dev != nil {
		in.dev.Close()
	}
}

// startInstance builds the data plane for one mesh and joins it to the shared
// rendezvous node.
func startInstance(ctx context.Context, log *slog.Logger, cfg state.Config, st *state.State,
	node *waku.Node, m state.Mesh, iface string, port uint16, legacy, verbose bool) (*instance, error) {

	nk, err := m.Key()
	if err != nil {
		return nil, fmt.Errorf("mesh %q: %w", m.Label, err)
	}
	networkID, err := m.NetworkID()
	if err != nil {
		return nil, err
	}
	// The identity for this mesh, kept verbatim for the one this device already
	// belonged to and derived for any other.
	ms, err := st.MeshState(networkID, legacy)
	if err != nil {
		return nil, fmt.Errorf("mesh %q: %w", m.Label, err)
	}

	self := identity.OverlayAddr(nk, ms.Identity.DevicePub)
	log.Info("mesh starting", "mesh", m.Label, "overlay", self,
		"prefix", nk.Prefix(), "interface", iface, "port", port)

	in := &instance{
		label: m.Label, self: self, prefix: nk.Prefix(), iface: iface, port: port,
	}

	// Synthetic IPv4 (ADR-021), per mesh: with per-mesh identities the aliases
	// cannot collide by construction, but the table must still be per mesh or
	// one mesh's peer would answer another's name.
	in.aliases = v4.NewTable(networkID, v4.Entry{Overlay: self, DevicePub: ms.Identity.DevicePub}, nil)

	// The mesh's own slice of the range, routed at its own interface. One
	// route for the whole range would send another mesh's traffic here.
	tunDev, err := wg.CreateTUN(iface, self, nk.Prefix(), wg.DefaultMTU,
		in.aliases.Self(), in.aliases.Block())
	if err != nil {
		return nil, fmt.Errorf("mesh %q: tun: %w (need CAP_NET_ADMIN)", m.Label, err)
	}
	translated := v4.NewDevice(tunDev, in.aliases, wg.DefaultMTU-40-20)

	wgLevel := device.LogLevelError
	if verbose {
		wgLevel = device.LogLevelVerbose
	}
	in.dev, err = wg.NewDevice(translated, ms.Identity.WGPriv, port,
		device.NewLogger(wgLevel, "[wg "+m.Label+"] "))
	if err != nil {
		return in, fmt.Errorf("mesh %q: wireguard: %w", m.Label, err)
	}

	// The mesh's own view of the config: its key, its relay setting, its admin
	// keys. Everything else — name, preset, mode — is the device's and shared.
	meshCfg := cfg
	meshCfg.NetworkKey = m.NetworkKey
	meshCfg.AdminKeys = m.AdminKeys
	meshCfg.Relay = m.Relay
	meshCfg.Services = m.Services

	in.mesh, err = mesh.New(log.With("mesh", m.Label), meshCfg, stateFor(st, ms), node, in.dev)
	if err != nil {
		return in, fmt.Errorf("mesh %q: %w", m.Label, err)
	}
	in.mesh.SetV4(in.aliases)

	in.specs = append([]string(nil), m.Services...)
	if specs, err := meshCfg.ServiceSpecs(); err != nil {
		log.Warn("services not published", "mesh", m.Label, "err", err)
	} else if len(specs) > 0 {
		in.services = service.Publish(ctx, self, mesh.DNSName(cfg.Name, cfg.HostsSuffix), specs,
			func(msg string, args ...any) { log.Info(msg, append(args, "mesh", m.Label)...) })
	}
	return in, nil
}

// stateFor presents one mesh's state as the single-mesh State the mesh package
// still expects.
//
// A shim rather than a rewrite: the mesh package reads Identity, Seq and
// Credential, and threading a per-mesh type through it would be a large change
// to the part of the system that is working. The fields alias the same objects,
// so a sequence number the mesh advances is advanced in the per-mesh state.
func stateFor(st *state.State, ms *state.MeshState) *state.State {
	if ms.Identity == st.Identity {
		return st // the mesh that owns the single-mesh fields
	}
	return st.View(ms)
}

// namedMesh is what name resolution needs from a mesh: a label and two
// lookups. An interface rather than *instance so the rule below can be tested
// without a tunnel, which is otherwise the only way to reach it.
type namedMesh struct {
	label  string
	lookup func(string) (netip.Addr, bool)
	alias  func(netip.Addr) (netip.Addr, bool)
}

func named(instances []*instance) []namedMesh {
	out := make([]namedMesh, 0, len(instances))
	for _, in := range instances {
		out = append(out, namedMesh{label: in.label, lookup: in.mesh.Lookup, alias: in.mesh.LookupV4})
	}
	return out
}

// resolveAcross answers a name across every mesh (ADR-015).
//
// The qualified form wins: vps.home.mesh is unambiguous by construction. The
// short form is answered only when exactly one mesh has that name, so a
// single-mesh node — which is every node today — sees no change, and ambiguity
// removes the short name rather than silently picking a mesh for you.
func resolveAcross(meshes []namedMesh) dnssrv.Lookup {
	return func(host string) (netip.Addr, bool) {
		// Qualified: the label to the right of the device is a mesh label.
		// Tried first, and only against the mesh it names — otherwise
		// "vps.home" would fall through and match a device called "vps" on
		// another mesh, which is the one answer that is certainly wrong.
		if dev, rest, ok := cutLabel(host); ok {
			for _, m := range meshes {
				if m.label == rest {
					return m.lookup(dev)
				}
			}
		}
		var found netip.Addr
		hits := 0
		for _, m := range meshes {
			if addr, ok := m.lookup(host); ok {
				found, hits = addr, hits+1
			}
		}
		return found, hits == 1
	}
}

// aliasAcross maps an overlay address to its synthetic IPv4, whichever mesh it
// belongs to. Addresses are unique across meshes — the prefix derives from the
// network key — so there is nothing to disambiguate.
func aliasAcross(meshes []namedMesh) func(netip.Addr) (netip.Addr, bool) {
	return func(overlay netip.Addr) (netip.Addr, bool) {
		for _, m := range meshes {
			if a, ok := m.alias(overlay); ok {
				return a, true
			}
		}
		return netip.Addr{}, false
	}
}

// ifaceAndPort numbers the interface and port for the nth mesh. The first keeps
// exactly what the config says, so a node with one mesh is unchanged.
func ifaceAndPort(cfg state.Config, i int) (string, uint16) {
	if i == 0 {
		return cfg.Interface, cfg.ListenPort
	}
	return fmt.Sprintf("%s%d", cfg.Interface, i), cfg.ListenPort + uint16(i)
}

// cutLabel splits "vps.home" into "vps" and "home".
func cutLabel(host string) (first, rest string, ok bool) {
	for i := 0; i < len(host); i++ {
		if host[i] == '.' {
			return host[:i], host[i+1:], true
		}
	}
	return "", "", false
}
