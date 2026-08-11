package mobile

import (
	"context"
	"fmt"
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
	Name             string  `json:"name"`
	DNSName          string  `json:"dns_name,omitempty"`
	Overlay          string  `json:"overlay"`
	Online           bool    `json:"online"`
	Relay            bool    `json:"relay,omitempty"`
	Live             bool    `json:"live"`
	HandshakeAgeS    int64   `json:"handshake_age_s,omitempty"`
	Endpoint         string  `json:"endpoint,omitempty"`
	Relayed          bool    `json:"relayed"`
	RxBytes          uint64  `json:"rx_bytes"`
	TxBytes          uint64  `json:"tx_bytes"`
	RxBps            float64 `json:"rx_bps"`
	TxBps            float64 `json:"tx_bps"`
	RTTMs            int64   `json:"rtt_ms,omitempty"`
	DiscoveredAfterS float64 `json:"discovered_after_s,omitempty"`
	TunnelAfterS     float64 `json:"tunnel_after_s,omitempty"`
}

type statusPayload struct {
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

func snapshot(m *mesh.Mesh, suffix string) statusPayload {
	now := time.Now()
	var out statusPayload

	h := m.Health()
	out.Rendezvous.Status = h.Status
	out.Rendezvous.OK = h.OK(now)
	out.Rendezvous.Problem = h.Problem(now)
	out.Rendezvous.Detail = h.Detail(now)

	stats, _ := m.PeerStats()
	for _, p := range m.Roster().Peers() {
		sp := statusPeer{
			Name:    p.Name,
			DNSName: mesh.DNSName(p.Name, suffix),
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

// resolveAll answers a name across every mesh on this device (ADR-015).
//
// The qualified form wins — vps.home.mesh is answered only by the mesh it names
// — and the short form only when exactly one mesh has that name. A phone with
// one mesh, which is every phone today, sees no change.
func resolveAll(instances []*meshInstance) dnssrv.Lookup {
	return func(host string) (netip.Addr, bool) {
		if dev, rest, ok := strings.Cut(host, "."); ok {
			for _, in := range instances {
				if in.label == rest {
					return in.mesh.Lookup(dev)
				}
			}
		}
		var found netip.Addr
		hits := 0
		for _, in := range instances {
			if addr, ok := in.mesh.Lookup(host); ok {
				found, hits = addr, hits+1
			}
		}
		return found, hits == 1
	}
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
