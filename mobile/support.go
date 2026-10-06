package mobile

import (
	"net"
	"sync"

	"context"
	"fmt"
	"github.com/vpavlin/shrooms/internal/invite"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/waku"
)

// paths keeps config and state together under the app's private directory.
// Android gives one writable place; there is nothing to gain from splitting
// them as the Linux packaging does.
func paths(configDir string) (cfgPath, stateDir string) {
	return filepath.Join(configDir, "config.toml"), filepath.Join(configDir, "state")
}

func load(configDir string) (state.Config, *state.State, error) {
	cfgPath, stateDir := paths(configDir)
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return state.Config{}, nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return state.Config{}, nil, fmt.Errorf("config: %w", err)
	}
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return state.Config{}, nil, fmt.Errorf("state: %w", err)
	}
	return cfg, st, nil
}

// phoneDefaults is DefaultConfig with the one value a phone must not inherit
// from a server.
//
// Core carries gossip for the whole network — roughly 20 MB/h, which on a phone
// is somebody's mobile data — and holding a gossip mesh needs stable
// connectivity, which is the one thing a phone does not have. A phone that woke
// up as Core did not merely cost data: it failed to form a mesh at all behind a
// carrier NAT and its discovery stalled, which presents as "the app does not
// work" a long way from the cause.
//
// Deliberately not changed in state.DefaultConfig, because a server should keep
// defaulting to Core. A network where everything is Edge has nobody left to
// carry it.
func phoneDefaults() state.Config {
	cfg := state.DefaultConfig()
	cfg.Mode = state.ModeEdge
	return cfg
}

// setup writes a config, creating a network key when none is given.
//
// The device identity is created by LoadOrCreateState and never replaced here:
// losing it means a new overlay address and looking like a different device to
// every peer, which on a phone would silently happen on every reconfigure.
func setup(configDir, name, meshName, key string) (state.Config, *state.State, error) {
	cfgPath, stateDir := paths(configDir)

	cfg := phoneDefaults()
	if name != "" {
		cfg.Name = name
	}
	if key == "" {
		nk, err := identity.NewNetworkKey()
		if err != nil {
			return state.Config{}, nil, fmt.Errorf("generate network key: %w", err)
		}
		key = nk.String()
	} else if _, err := identity.ParseNetworkKey(key); err != nil {
		return state.Config{}, nil, fmt.Errorf("network key: %w", err)
	}
	// Named, with the base identity, like every first mesh.
	cfg = cfg.WithFirstMesh(meshName, state.Mesh{NetworkKey: key})
	if err := cfg.Validate(); err != nil {
		return state.Config{}, nil, err
	}
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return state.Config{}, nil, fmt.Errorf("write config: %w", err)
	}
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return state.Config{}, nil, fmt.Errorf("state: %w", err)
	}
	return cfg, st, nil
}

// nodeConfig mirrors the daemon's, including the rule that clusterId is only
// passed when explicitly set — sending it activates a legacy
// cluster-to-network mapping that overrides the preset.
func nodeConfig(cfg state.Config, learned ...string) waku.Config {
	c := waku.Config{"mode": cfg.Mode}
	if cfg.ClusterID != 0 {
		c["clusterId"] = cfg.ClusterID
	}
	if cfg.Preset != "" {
		c["preset"] = cfg.Preset
	}
	// Configured first, then what peers published (ADR-031), in the same
	// order as the daemon (cmd/shrooms/daemon.go). The library merges these
	// with the preset's own fleet nodes rather than replacing them — a daemon
	// with one learned address logs "Dialing multiple peers numOfPeers=7",
	// one learned plus the preset's six — so adding ours cannot cut a phone
	// off from the public fleet; it only gives it somewhere else to start.
	entry := append(append([]string(nil), cfg.EntryNodes...), learned...)
	if len(entry) > 0 {
		c["entryNodes"] = entry
	}
	return c
}

// withFreePorts gives a phone's delivery node ports nobody else is holding.
//
// The Android library is v0.38.1 (vendored, revision e91aaa), and v0.38.1
// binds FIXED defaults: TCP 60000 and discovery on UDP 9000
// (tools/confutils/cli_args.nim at that tag). Every app embedding it does the
// same, so whichever starts second fails — "failed to start waku discovery:
// Address already in use". On 2026-10-01 a phone could not connect until the
// Loam app, which embeds the same library, was force-stopped. The desktop
// library (our July pin) picks random ports, which is why this never showed
// there and could not be reproduced on a laptop.
//
// A phone publishes no delivery address (it is an Edge node; ADR-031's boot
// addresses come only from Core relays), so its ports need not be stable, only
// free. So: ask the OS for one of each and hand them over. A moment passes
// between closing the probe sockets and the library binding them, in which
// another process could take one — far smaller than a default every embedder
// shares, and a collision still fails the start loudly.
func withFreePorts(c waku.Config) waku.Config {
	if _, set := c["tcpPort"]; !set {
		if p := freePort("tcp"); p != 0 {
			c["tcpPort"] = p
		}
	}
	if _, set := c["discv5UdpPort"]; !set {
		if p := freePort("udp"); p != 0 {
			c["discv5UdpPort"] = p
		}
	}
	return c
}

// freePort returns a port the OS just confirmed was free, or 0 to leave the
// choice to the library.
func freePort(network string) int {
	switch network {
	case "tcp":
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	case "udp":
		pc, err := net.ListenPacket("udp", ":0")
		if err != nil {
			return 0
		}
		defer pc.Close()
		return pc.LocalAddr().(*net.UDPAddr).Port
	}
	return 0
}

// sessionNodeConfig is what Start builds its node from: the device's fleet
// settings, the addresses it learned, and ports of its own. The counterpart of
// sharedNodeConfig, so both constructions are built — and tested — the same way.
func sessionNodeConfig(cfg state.Config, st *state.State) waku.Config {
	return withFreePorts(nodeConfig(cfg, learnedBootPeers(st)...))
}

// learnedBootPeers is what this device has been told it can bootstrap from:
// the delivery addresses mesh Core nodes publish in their announces, kept on
// disk because a running node cannot take new ones (internal/state/bootpeers.go).
//
// The phone never read them. The daemon has since ADR-031, which is why on
// 2026-10-01, with five of six public fleet nodes down after the v0.39 rollout,
// desktops got back in through the VPS's own delivery node while a phone that
// knew the same address kept dialling only the dead public list.
func learnedBootPeers(st *state.State) []string {
	if st == nil {
		return nil
	}
	return st.BootPeers(time.Now())
}

// dupFd copies a descriptor so Go's os.File can own its copy. Without this,
// closing the tun device would close Android's descriptor too.
func dupFd(fd int) (int, error) {
	n, err := syscall.Dup(fd)
	if err != nil {
		return -1, err
	}
	syscall.CloseOnExec(n)
	return n, nil
}

// --- status ---------------------------------------------------------------

type statusPeer struct {
	// Mesh is which mesh this peer is on, empty on a single-mesh device.
	Mesh    string `json:"mesh,omitempty"`
	Name    string `json:"name"`
	DNSName string `json:"dns_name,omitempty"`
	Overlay string `json:"overlay"`
	Online  bool   `json:"online"`
	Relay   bool   `json:"relay,omitempty"`
	// Services are the names this peer says it publishes (ADR-023). A claim
	// about what it offers, not a report that anything is listening.
	Services []string `json:"services,omitempty"`

	// Bound is what this peer says is listening on its own mesh address, as
	// "name:port" (ADR-026). Reached as <device>.mesh:<port> — no forwarder
	// and no name of its own, which is why it is not in Services.
	//
	// The daemon has reported this since ADR-026 landed and these bindings did
	// not, so the phone could not show a bound port however loudly a peer
	// announced it. Two front-ends over one core is worth exactly as much as
	// the narrower of the two.
	Bound            []string `json:"bound,omitempty"`
	Live             bool     `json:"live"`
	HandshakeAgeS    int64    `json:"handshake_age_s,omitempty"`
	Endpoint         string   `json:"endpoint,omitempty"`
	Relayed          bool     `json:"relayed"`
	RxBytes          uint64   `json:"rx_bytes"`
	TxBytes          uint64   `json:"tx_bytes"`
	RxBps            float64  `json:"rx_bps"`
	TxBps            float64  `json:"tx_bps"`
	RTTMs            int64    `json:"rtt_ms,omitempty"`
	DiscoveredAfterS float64  `json:"discovered_after_s,omitempty"`
	TunnelAfterS     float64  `json:"tunnel_after_s,omitempty"`
}

// statusMesh is one mesh, for a device that has more than one.
type statusMesh struct {
	Label   string `json:"label"`
	Overlay string `json:"overlay"`
	Prefix  string `json:"prefix"`
	Peers   int    `json:"peers"`
}

type statusPayload struct {
	// Meshes is present only when there is more than one.
	Meshes  []statusMesh `json:"meshes,omitempty"`
	Name    string       `json:"name"`
	DNSName string       `json:"dns_name,omitempty"`
	Overlay string       `json:"overlay"`
	Prefix  string       `json:"prefix"`
	Peers   []statusPeer `json:"peers"`

	// Announced is where this device tells peers to reach it.
	//
	// Carried to the phone because that is where it is hardest to find out and
	// most often empty: Android restricts interface enumeration for untrusted
	// apps, so a phone frequently cannot list its own addresses and has nothing
	// to announce until some peer reaches it and reports back. A device in that
	// state looks perfectly healthy from every other screen while being
	// undialable by everybody.
	Announced []string `json:"announced"`

	// Due is every member of every mesh on this phone — the phone included —
	// whose credential runs out within mesh.DueWithin, or has. The same list
	// the desktop's status shows (mesh/due.go). The app shows it on the mesh
	// screen and notifies when the phone's own entry appears.
	Due []mesh.Due `json:"due,omitempty"`

	Rendezvous struct {
		Status  string `json:"status"`
		OK      bool   `json:"ok"`
		Problem string `json:"problem,omitempty"`
		Detail  string `json:"detail,omitempty"`
		// LibraryDead is the waku package's verdict that the delivery library
		// itself is refusing requests (waku.Liveness): the one fault the app's
		// watchdog could not see, because the library goes on reporting
		// Connected while it happens. The app acts on it through hardRestart,
		// with that path's own cooldowns.
		LibraryDead     bool   `json:"library_dead,omitempty"`
		LibraryEvidence string `json:"library_evidence,omitempty"`
		// Deaf is mesh.Health.Silent on any of the phone's meshes: traffic
		// arriving, none of it ours, for SilentAfter — including a node that
		// came up that way (it opened no announce at all). Its OK says
		// healthy throughout, because other applications' traffic proves the
		// subscription live; only a new process has cured it. The app acts
		// on it through hardRestart, with that path's own floor.
		Deaf bool `json:"deaf,omitempty"`
	} `json:"rendezvous"`

	// DNS counts what each layer of name resolution actually saw.
	//
	// Three failures look identical from outside the phone — the query never
	// reaches us, it reaches us and is refused, or we answer and Android
	// discards the reply — and only counters tell them apart. Intercepted is
	// the packet layer (did a query for our resolver arrive on the tun at all);
	// Queries/Answers is the resolver above it.
	DNS struct {
		Intercepted     uint64 `json:"intercepted"`
		InterceptFailed uint64 `json:"intercept_failed"`
		// Missed is queries aimed at the resolver in a form it does not answer,
		// which in practice means DNS over TCP.
		Missed uint64 `json:"missed"`
		// NXDomain and NoData are the two outcomes that used to be invisible: a
		// name we do not know, and a name we know with no record of the type
		// asked for. The overlay is IPv6-only, so NoDataA is a client that
		// asked only for IPv4 — which fails while the resolver works perfectly.
		NXDomain      uint64 `json:"nxdomain"`
		NoDataA       uint64 `json:"nodata_a"`
		NoDataOther   uint64 `json:"nodata_other"`
		Queries       uint64 `json:"queries"`
		Answers       uint64 `json:"answers"`
		Refused       uint64 `json:"refused"`
		Forwarded     uint64 `json:"forwarded"`
		ForwardFailed uint64 `json:"forward_failed"`
	} `json:"dns"`
}

// snapshotAll reports every mesh on the device. The first one fills the
// top-level fields, so an app build that knows nothing about several meshes
// shows exactly what it always did.
func snapshotAll(instances []*meshInstance, suffix string) statusPayload {
	// Labelled even with one mesh: names are qualified everywhere, and an
	// empty label here now yields no name rather than a short one that the
	// resolver no longer answers.
	out := snapshot(instances[0].mesh, suffix, instances[0].label)
	now := time.Now()
	// Before the single-mesh return, so a phone on one mesh is told too.
	out.Due = dueAcross(instances, now)
	for _, in := range instances[1:] {
		if in.mesh.Health().Silent(now) {
			out.Rendezvous.Deaf = true
		}
	}
	if len(instances) == 1 {
		return out
	}
	out.Peers = nil
	for _, in := range instances {
		part := snapshot(in.mesh, suffix, in.label)
		for i := range part.Peers {
			// Which mesh a peer is on, since two meshes may hold devices with
			// the same name and the address is the only thing that differs.
			part.Peers[i].Mesh = in.label
		}
		out.Peers = append(out.Peers, part.Peers...)
		out.Meshes = append(out.Meshes, statusMesh{
			Label:   in.label,
			Overlay: in.self.String(),
			Prefix:  in.prefix.String(),
			Peers:   len(in.mesh.Roster().Current(now)),
		})
	}
	return out
}

// dueAcross is mesh.DueAmong over every mesh on this phone, soonest first.
func dueAcross(instances []*meshInstance, now time.Time) []mesh.Due {
	var due []mesh.Due
	for _, in := range instances {
		due = append(due, mesh.DueAmong(in.label, in.mesh.Members(), now)...)
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].NotAfter.Before(due[j].NotAfter) })
	return due
}

// label names the mesh for the names built here. Always set: a name without its
// mesh is one the resolver does not answer.
func snapshot(m *mesh.Mesh, suffix, label string) statusPayload {
	now := time.Now()
	var out statusPayload

	h := m.Health()
	out.Rendezvous.Status = h.Status
	out.Rendezvous.OK = h.OK(now)
	out.Rendezvous.Problem = h.Problem(now)
	out.Rendezvous.Detail = h.Detail(now)
	out.Rendezvous.LibraryDead, out.Rendezvous.LibraryEvidence = waku.LibraryVerdict(now)
	out.Rendezvous.Deaf = h.Silent(now)

	// Never nil: the app distinguishes "none" from "this build does not report
	// it", and a nil slice marshals to null, which is the same ambiguity the
	// desktop status had to fix with a pointer.
	out.Announced = m.Announced()
	if out.Announced == nil {
		out.Announced = []string{}
	}

	stats, _ := m.PeerStats()
	svc, bnd := m.Services(now), m.Bound(now)
	for _, p := range m.Roster().Current(now) {
		sp := statusPeer{
			Services: svc[p.ID()],
			Bound:    bnd[p.ID()],
			Name:     p.Name,
			// Qualified by the mesh label when this device has more than one,
			// or a peer on the second mesh is named at an address on the
			// first. The phone showed a bound ssh port as laptop.mesh:22 when
			// the port was on another mesh entirely.
			DNSName: mesh.QualifiedDNSName(p.Name, label, suffix),
			Overlay: p.Overlay.String(),
			Online:  p.Online(now),
			Relay:   p.Relay,
		}
		if st, ok := stats[p.WGPub.String()]; ok {
			sp.Live = st.Live(now)
			sp.Endpoint = st.Endpoint
			sp.RxBytes, sp.TxBytes = st.RxBytes, st.TxBytes
			if st.Handshaked() {
				sp.HandshakeAgeS = int64(now.Sub(st.LastHandshake).Seconds())
			}
			// A relayed endpoint is serialised with a relay: prefix; the app
			// shows it differently, so it must not have to parse the string.
			sp.Relayed = len(st.Endpoint) > 6 && st.Endpoint[:6] == "relay:"
		}
		if r := m.Rate(p.ID()); r.RxBps > 0 || r.TxBps > 0 {
			sp.RxBps, sp.TxBps = r.RxBps, r.TxBps
		}
		if best, ok := m.BestPath(p.ID(), now); ok {
			sp.RTTMs = best.RTT.Milliseconds()
		}
		t := m.Timing(p.ID())
		sp.DiscoveredAfterS = t.DiscoveredAfter.Seconds()
		sp.TunnelAfterS = t.TunnelAfter.Seconds()
		out.Peers = append(out.Peers, sp)
	}
	return out
}

// --- logging --------------------------------------------------------------

// bridge forwards slog records to the app. Android has no useful stderr for a
// library, and a log the user can see is the difference between "it does not
// work" and a bug report.
type bridge struct {
	l Logger
	// configDir so every line the app is shown is also appended to disk. A
	// crash takes the app's in-memory tail with it; the file is what is left.
	configDir string
	attrs     []slog.Attr
}

func newBridge(l Logger, configDir string) slog.Handler {
	return &bridge{l: l, configDir: configDir}
}

func (b *bridge) Enabled(_ context.Context, lvl slog.Level) bool { return lvl >= slog.LevelInfo }

func (b *bridge) Handle(_ context.Context, r slog.Record) error {
	if b.l == nil {
		return nil
	}
	msg := r.Message
	for _, a := range b.attrs {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})
	b.l.Log(r.Level.String(), msg)
	// And to disk, where a kill cannot take it. The app's own tail is bounded
	// and in memory, so it is gone exactly when it is wanted.
	appendLog(b.configDir, r.Level.String(), msg)
	return nil
}

func (b *bridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &bridge{l: b.l, configDir: b.configDir, attrs: append(append([]slog.Attr(nil), b.attrs...), attrs...)}
}

func (b *bridge) WithGroup(string) slog.Handler { return b }

// DNSSuffix is the domain the app should hand to VpnService.Builder as a search
// domain, so `ping laptop` works and not only `laptop.mesh`.
func DNSSuffix(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return dnssrv.DefaultSuffix
	}
	if cfg.HostsSuffix == "" {
		return dnssrv.DefaultSuffix
	}
	return cfg.HostsSuffix
}

// DNSAddress is the address the app must hand to VpnService.Builder.
//
// Not this device's overlay address: an address the interface holds is
// delivered locally by the kernel and never reaches the tun, so nothing can
// answer it. See dns.ServiceAddr.
func DNSAddress(configDir string) string {
	// The primary mesh's prefix, not the top-level key, which a named first
	// mesh does not have — see primaryOf.
	nk, _, ok := primaryOf(configDir)
	if !ok {
		return ""
	}
	return dnssrv.ServiceAddr(nk.Prefix()).String()
}

// eventTap lets an enrolment listen in on the running node.
//
// The phone has one rendezvous node and the library has one global callback, so
// a second node started for a join is not merely wasteful — it does not work.
// And the node's event channel has a single consumer, so the exchange cannot
// simply read it alongside the meshes. Both problems have the same answer: one
// reader, feeding everything that wants events.
type eventTap struct {
	mu sync.Mutex
	ch chan invite.Message
}

// attach opens the tap. The returned function closes it, and must be called.
func (t *eventTap) attach() (<-chan invite.Message, func()) {
	ch := make(chan invite.Message, 512)
	t.mu.Lock()
	t.ch = ch
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		t.ch = nil
		t.mu.Unlock()
	}
}

// feed offers an event to whatever is listening. Nothing usually is.
func (t *eventTap) feed(ev waku.Event) {
	t.mu.Lock()
	ch := t.ch
	t.mu.Unlock()
	if ch == nil {
		return
	}
	msg, _, ok := waku.ParseMessage(ev.JSON)
	if !ok {
		return
	}
	select {
	case ch <- invite.Message{Topic: msg.ContentTopic, Payload: msg.Payload}:
	default: // the exchange is behind; it retries
	}
}

// tapTransport runs an enrolment over the node a session is already using:
// subscribing and sending on it directly, and receiving through the tap.
type tapTransport struct {
	node *waku.Node
	msgs <-chan invite.Message
}

func (t tapTransport) Subscribe(topic string) error   { return t.node.Subscribe(topic) }
func (t tapTransport) Unsubscribe(topic string) error { return t.node.Unsubscribe(topic) }
func (t tapTransport) Send(topic string, payload []byte, ephemeral bool) (string, error) {
	return t.node.Send(topic, payload, ephemeral)
}
func (t tapTransport) Messages() <-chan invite.Message { return t.msgs }

// resolveAll answers a name across every mesh on this device (ADR-015).
//
// The qualified form wins — vps.home.mesh is answered only by the mesh it names
// — and for the short form the first mesh that has the name answers, in config
// order. See resolveAcross in the daemon: refusing an ambiguous short name
// took the short name away from precisely the devices on both of your meshes,
// which are the ones you reach most.
func resolveAll(instances []*meshInstance, configDir string) dnssrv.Lookup {
	// Qualified only, the same rule as the daemon (mesh.ResolveQualified): the
	// name's last label is the mesh, and a name with no mesh answers nothing.
	// The fallthrough that tried every mesh for an unqualified name is what
	// let a phone answer peer.mesh while the rest of the mesh used
	// peer.office.mesh (docs/one-kind-of-mesh.md, 2026-09-29).
	return func(host string) (netip.Addr, bool) {
		return mesh.ResolveQualified(host, func(label string) (func(string) (netip.Addr, bool), bool) {
			for _, in := range instances {
				if in.label == label {
					return in.mesh.Lookup, true
				}
			}
			// Not a label this session was built with. It may still be a mesh
			// this device is on: a label is local and can change while the
			// tunnel runs — accepting an invite to a mesh you are already on
			// renames it — and until now that meant the new name resolved only
			// after a reconnect, while the app already displayed it.
			//
			// Only on a miss, so the ordinary path never reads a file.
			if in, ok := byCurrentLabel(instances, configDir, label); ok {
				return in.mesh.Lookup, true
			}
			return nil, false
		})
	}
}

// byCurrentLabel finds the instance a label names *now*, according to the
// config rather than according to what this session was started with.
func byCurrentLabel(instances []*meshInstance, configDir, label string) (*meshInstance, bool) {
	cfgPath, _ := paths(configDir)
	cfg, err := state.LoadConfigUnvalidated(cfgPath)
	if err != nil {
		return nil, false
	}
	for _, m := range cfg.Meshes() {
		if m.Label != label {
			continue
		}
		nk, err := m.Key()
		if err != nil {
			return nil, false
		}
		id := state.NetworkID(nk)
		for _, in := range instances {
			if in.networkID == id {
				return in, true
			}
		}
	}
	return nil, false
}

// aliasAll maps an overlay address to its synthetic IPv4, whichever mesh owns
// it. Overlay addresses are unique across meshes, so there is nothing to
// disambiguate.
func aliasAll(instances []*meshInstance) func(netip.Addr) (netip.Addr, bool) {
	return func(overlay netip.Addr) (netip.Addr, bool) {
		for _, in := range instances {
			if a, ok := in.mesh.LookupV4(overlay); ok {
				return a, true
			}
		}
		return netip.Addr{}, false
	}
}
