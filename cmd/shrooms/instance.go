package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/lanhost"
	"github.com/vpavlin/shrooms/internal/lanproxy"
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
	// lan answers for the LAN hosts this node offers on this mesh
	// (ADR-068); nil when its config lists none.
	lan *lanproxy.Gateway
	// lanErr is why the config's lan_hosts are not offered, for status.
	lanErr string
	// tunName is the interface as the kernel named it, and aliasRoutes the
	// LAN aliases routed at it now (lanaliases.go).
	tunName     string
	inbound     *wg.SelfOnlyTun // what peers may hand the kernel (F-26)
	aliasMu     sync.Mutex
	aliasRoutes map[netip.Addr]bool
	self        netip.Addr
	prefix      netip.Prefix
	iface       string

	// port is the WireGuard port bound now; read it with listenPort. It moves
	// when ephemeral is set (state.Config.EphemeralPort): a fresh port at start
	// and after every network change, because a phone's tethering NAT can
	// strand a port for good (docs/stale-tether-nat.md). keepMapped reads it
	// from its own goroutine, hence atomic.
	port      atomic.Uint32
	ephemeral bool

	// mapped is the external address the router gave us for this mesh's port,
	// if it gave one (ADR-024).
	mapped netip.AddrPort

	// remap asks keepMapped to forget what it has and ask again now.
	//
	// Sent when the underlay changes. A mapping describes one router, and this
	// node has just left it — see the watchdog in daemon.go, which already
	// notices and, until 2026-09-11, told nobody. Buffered and written without
	// blocking: a missed nudge costs one renewal interval, a blocked watchdog
	// costs the restart it exists to perform.
	remap chan struct{}

	// primary marks the mesh written as network_key — the one this device was
	// built around. It wins an unqualified name (see named).
	primary bool

	// relay is whether this node forwards for peers of this mesh. Per mesh:
	// carrying traffic for one set of people does not imply carrying it for
	// another (ADR-015).
	relay bool

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
	node *waku.Node, m state.Mesh, iface string, port uint16, block netip.Prefix,
	legacy, verbose bool) (*instance, error) {

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
	ephemeral := cfg.EphemeralPort(m)
	log.Info("mesh starting", "mesh", m.Label, "overlay", self,
		"prefix", nk.Prefix(), "interface", iface, "port", port, "ephemeral_port", ephemeral)

	in := &instance{
		label: m.Label, self: self, prefix: nk.Prefix(), iface: iface, ephemeral: ephemeral,
	}
	in.port.Store(uint32(port))

	// Synthetic IPv4 (ADR-021), per mesh: with per-mesh identities the aliases
	// cannot collide by construction, but the table must still be per mesh or
	// one mesh's peer would answer another's name.
	//
	// The block is chosen across every mesh this device has rather than from
	// this one's network id, because two ids land on the same block about once
	// in sixteen and both meshes would then route the same /19.
	in.aliases = v4.NewTableIn(block, v4.Entry{Overlay: self, DevicePub: ms.Identity.DevicePub}, nil)

	// The mesh's own slice of the range, routed at its own interface. One
	// route for the whole range would send another mesh's traffic here.
	tunDev, err := wg.CreateTUN(iface, self, nk.Prefix(), wg.DefaultMTU,
		in.aliases.Self(), in.aliases.Block())
	if err != nil {
		return nil, fmt.Errorf("mesh %q: tun: %w (need CAP_NET_ADMIN)", m.Label, err)
	}
	in.tunName, _ = tunDev.Name()
	translated := v4.NewDevice(tunDev, in.aliases, wg.DefaultMTU-40-20)
	// Only what is addressed to this device reaches the kernel: WireGuard
	// checks a packet's source, not where it is going (F-26).
	inbound := wg.SelfOnly(translated, self)
	in.inbound = inbound

	// LAN hosts this node offers (ADR-068): their packets are diverted into
	// a userspace stack above the kernel TUN, so a host's address is in no
	// kernel namespace. Only when the config lists some: every other node's
	// data plane is untouched.
	//
	// A lan_hosts entry the daemon cannot use is not fatal, any more than a
	// service is (state.Config.Validate): the mesh comes up without LAN hosts
	// and status says why. It was fatal, and on k11 (2026-11-04) one edited
	// line, made to move Home Assistant from a service to a LAN host, stopped
	// the daemon, and with it every mesh, including the one the machine was
	// reached over to fix it.
	var dataDev tun.Device = inbound
	if len(m.LanHosts) > 0 {
		gw, err := func() (*lanproxy.Gateway, error) {
			specs, err := lanhost.ParseSpecs(m.LanHosts)
			if err != nil {
				return nil, err
			}
			return lanproxy.NewGateway(inbound, specs, nk.Prefix(), ms.Identity.DevicePub, self,
				wg.DefaultMTU, log.With("mesh", m.Label, "part", "lan-hosts"))
		}()
		if err != nil {
			in.lanErr = err.Error()
			log.Error("LAN hosts not offered: the mesh runs without them", "mesh", m.Label, "err", err)
		} else {
			in.lan = gw
			dataDev = gw
			log.Info("offering LAN hosts, each once the admin grants it", "mesh", m.Label, "hosts", len(m.LanHosts))
		}
	}

	wgLevel := device.LogLevelError
	if verbose {
		wgLevel = device.LogLevelVerbose
	}
	// An ephemeral port is whatever the OS hands out, never the configured
	// one: a restart on the configured port is exactly what failed to clear a
	// stranded translation on 2026-10-02, so the fresh port has to start here
	// and not only at the next network change.
	bind := port
	if ephemeral {
		bind = 0
	}
	in.dev, err = wg.NewDevice(dataDev, ms.Identity.WGPriv, bind,
		device.NewLogger(wgLevel, "[wg "+m.Label+"] "))
	if err != nil {
		return in, fmt.Errorf("mesh %q: wireguard: %w", m.Label, err)
	}
	if ephemeral {
		if port, err = in.dev.ListenPort(); err != nil {
			return in, fmt.Errorf("mesh %q: which port did wireguard bind: %w", m.Label, err)
		}
		in.port.Store(uint32(port))
		log.Info("bound a fresh port", "mesh", m.Label, "port", port,
			"why", "an Edge node's port is nobody's contract, and a fresh one cannot be stranded by a NAT")
	}

	// The mesh's own view of the config: its key, its relay setting, its admin
	// keys, and its port. Everything else — name, preset, mode — is the
	// device's and shared.
	//
	// The port has to come from `port` rather than the config: that is the one
	// this mesh's WireGuard device just bound, and the config's belongs to the
	// device. Announcing the wrong one sent peers to another mesh's socket.
	meshCfg := cfg.ForMesh(m, port)
	in.relay = m.Relay

	in.mesh, err = mesh.New(log.With("mesh", m.Label), meshCfg, stateFor(st, ms), node, in.dev)
	if err == nil && in.lan != nil {
		in.lan.SetGrants(in.mesh.OwnLanGrant)
		in.mesh.OnLanGrant(in.lan.Recheck)
	}
	if err != nil {
		return in, fmt.Errorf("mesh %q: %w", m.Label, err)
	}
	in.mesh.SetV4(in.aliases)
	if len(m.LanAliases) > 0 {
		in.mesh.OnLanAliases(func(active map[netip.Addr]netip.Addr) { in.routeLanAliases(log, active) })
		in.routeLanAliases(log, in.mesh.LanAliases())
	}

	in.specs = append([]string(nil), m.Services...)
	if specs, err := meshCfg.ServiceSpecs(); err != nil {
		log.Warn("services not published", "mesh", m.Label, "err", err)
	} else if len(specs) > 0 {
		in.services = service.Publish(ctx, self, mesh.QualifiedDNSName(cfg.Name, m.Label, cfg.HostsSuffix), specs,
			func(msg string, args ...any) { log.Info(msg, append(args, "mesh", m.Label)...) })
	}
	return in, nil
}

// listenPort is the WireGuard port this mesh is bound to now.
func (in *instance) listenPort() uint16 { return uint16(in.port.Load()) }

// moveEphemeralPorts rebinds every mesh whose port is ephemeral to a fresh
// one, after the network changed.
//
// A tethering phone kept a stale translation for a laptop's WireGuard ports
// after the tether link renewed (docs/stale-tether-nat.md): every handshake
// reached its peer and every answer was lost, until the ports changed. Moving
// on every change rather than on detecting it costs one announce — and after
// a network change our address changed anyway, so peers' cached endpoints for
// us were already useless.
func moveEphemeralPorts(log *slog.Logger, instances []*instance) {
	for _, in := range instances {
		if in == nil || !in.ephemeral || in.dev == nil {
			continue
		}
		was := in.listenPort()
		now, err := in.dev.SetListenPort(0)
		if err != nil {
			log.Warn("could not move to a fresh port after the network changed",
				"mesh", in.label, "port", was, "err", err)
			continue
		}
		in.port.Store(uint32(now))
		if in.mesh != nil {
			in.mesh.SetListenPort(now)
		}
		log.Info("moved to a fresh port after the network changed",
			"mesh", in.label, "was", was, "now", now)
	}
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

// named is what the resolver needs from each running mesh.
//
// Order no longer decides anything. It did while the short form existed — the
// first mesh with a name answered peer.mesh — and that is exactly why the
// short form went: the same name meant different machines on different
// devices (docs/one-kind-of-mesh.md, 2026-09-29).
func named(instances []*instance) []namedMesh {
	out := make([]namedMesh, 0, len(instances))
	for _, in := range instances {
		out = append(out, namedMesh{label: in.label, lookup: in.mesh.Lookup, alias: in.mesh.LookupV4})
	}
	return out
}

// resolveAcross answers a name across every mesh (ADR-015), qualified only.
//
// peer.<mesh> resolves on the mesh it names, and nothing else resolves. A mesh
// the config knows but which is not running answers nothing, because it is
// not in the slice: the fall-through that once sent `ssh vps.work.mesh` to a
// vps on another mesh when work was switched off has nowhere left to happen,
// since there is no second pass at all.
func resolveAcross(meshes []namedMesh) dnssrv.Lookup {
	return func(host string) (netip.Addr, bool) {
		return mesh.ResolveQualified(host, func(label string) (func(string) (netip.Addr, bool), bool) {
			for _, m := range meshes {
				if m.label == label {
					return m.lookup, true
				}
			}
			return nil, false
		})
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

// cutLabel splits "vps.home" into "vps" and "home".
func cutLabel(host string) (first, rest string, ok bool) {
	for i := 0; i < len(host); i++ {
		if host[i] == '.' {
			return host[:i], host[i+1:], true
		}
	}
	return "", "", false
}

// localUnderlay fingerprints the addresses this node has on the real network.
//
// The mesh's own interfaces are excluded deliberately. They are torn down and
// rebuilt by the very restart this feeds, so counting them would make the
// daemon detect its own recovery as a fresh network change and restart again.
func localUnderlay(instances []*instance) string {
	ours := map[string]bool{}
	for _, in := range instances {
		if in.iface != "" {
			ours[in.iface] = true
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var addrs []string
	for _, ifc := range ifaces {
		if ours[ifc.Name] || ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range list {
			p, err := netip.ParsePrefix(a.String())
			if err != nil || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			addrs = append(addrs, ifc.Name+"="+p.Addr().String())
		}
	}
	sort.Strings(addrs)
	return strings.Join(addrs, ",")
}
