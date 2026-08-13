package mobile

import (
	"sync"

	"context"
	"fmt"
	"github.com/vpavlin/shrooms/internal/invite"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
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

// setup writes a config, creating a network key when none is given.
//
// The device identity is created by LoadOrCreateState and never replaced here:
// losing it means a new overlay address and looking like a different device to
// every peer, which on a phone would silently happen on every reconfigure.
func setup(configDir, name, key string) (state.Config, *state.State, error) {
	cfgPath, stateDir := paths(configDir)

	cfg := state.DefaultConfig()
	if name != "" {
		cfg.Name = name
	}
	if key == "" {
		nk, err := identity.NewNetworkKey()
		if err != nil {
			return state.Config{}, nil, fmt.Errorf("generate network key: %w", err)
		}
		cfg.NetworkKey = nk.String()
	} else {
		if _, err := identity.ParseNetworkKey(key); err != nil {
			return state.Config{}, nil, fmt.Errorf("network key: %w", err)
		}
		cfg.NetworkKey = key
	}
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
func nodeConfig(cfg state.Config) waku.Config {
	c := waku.Config{"mode": cfg.Mode}
	if cfg.ClusterID != 0 {
		c["clusterId"] = cfg.ClusterID
	}
	if cfg.Preset != "" {
		c["preset"] = cfg.Preset
	}
	if len(cfg.EntryNodes) > 0 {
		c["entryNodes"] = cfg.EntryNodes
	}
	return c
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
	Meshes     []statusMesh `json:"meshes,omitempty"`
	Name       string       `json:"name"`
	DNSName    string       `json:"dns_name,omitempty"`
	Overlay    string       `json:"overlay"`
	Prefix     string       `json:"prefix"`
	Peers      []statusPeer `json:"peers"`
	Rendezvous struct {
		Status  string `json:"status"`
		OK      bool   `json:"ok"`
		Problem string `json:"problem,omitempty"`
		Detail  string `json:"detail,omitempty"`
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
	out := snapshot(instances[0].mesh, suffix)
	if len(instances) == 1 {
		return out
	}
	now := time.Now()
	out.Peers = nil
	for _, in := range instances {
		part := snapshot(in.mesh, suffix)
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
	_ = now
	return out
}

func snapshot(m *mesh.Mesh, suffix string) statusPayload {
	now := time.Now()
	var out statusPayload

	h := m.Health()
	out.Rendezvous.Status = h.Status
	out.Rendezvous.OK = h.OK(now)
	out.Rendezvous.Problem = h.Problem(now)
	out.Rendezvous.Detail = h.Detail(now)

	stats, _ := m.PeerStats()
	svc, bnd := m.Services(now), m.Bound(now)
	for _, p := range m.Roster().Current(now) {
		sp := statusPeer{
			Services: svc[p.ID()],
			Bound:    bnd[p.ID()],
			Name:     p.Name,
			DNSName:  mesh.DNSName(p.Name, suffix),
			Overlay:  p.Overlay.String(),
			Online:   p.Online(now),
			Relay:    p.Relay,
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
	l     Logger
	attrs []slog.Attr
}

func newBridge(l Logger) slog.Handler { return &bridge{l: l} }

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
	return nil
}

func (b *bridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &bridge{l: b.l, attrs: append(append([]slog.Attr(nil), b.attrs...), attrs...)}
}

func (b *bridge) WithGroup(string) slog.Handler { return b }

// mustKey is for paths where the config has already been validated.
func mustKey(cfg state.Config) identity.NetworkKey {
	nk, _ := cfg.Key()
	return nk
}

// DNSSuffix is the domain the app should hand to VpnService.Builder as a search
// domain, so `ping laptop` works and not only `laptop.mesh`.
func DNSSuffix(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return "mesh"
	}
	if cfg.HostsSuffix == "" {
		return "mesh"
	}
	return cfg.HostsSuffix
}

// DNSAddress is the address the app must hand to VpnService.Builder.
//
// Not this device's overlay address: an address the interface holds is
// delivered locally by the kernel and never reaches the tun, so nothing can
// answer it. See dns.ServiceAddr.
func DNSAddress(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	nk, err := cfg.Key()
	if err != nil {
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
	return func(host string) (netip.Addr, bool) {
		if dev, rest, ok := strings.Cut(host, "."); ok {
			for _, in := range instances {
				if in.label == rest {
					return in.mesh.Lookup(dev)
				}
			}
			// Not a label this session was built with. It may still be a mesh
			// this device is on: a label is local and can change while the
			// tunnel runs — accepting an invite to a mesh you are already on
			// renames it — and until now that meant the new name resolved only
			// after a reconnect, while the app already displayed it.
			//
			// Only on a miss, so the ordinary path never reads a file.
			if in, ok := byCurrentLabel(instances, configDir, rest); ok {
				return in.mesh.Lookup(dev)
			}
		}
		for _, in := range instances {
			if addr, ok := in.mesh.Lookup(host); ok {
				return addr, true
			}
		}
		return netip.Addr{}, false
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
