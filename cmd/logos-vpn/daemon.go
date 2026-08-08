package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/device"

	dnssrv "github.com/vpavlin/logos-vpn/internal/dns"
	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/mesh"
	"github.com/vpavlin/logos-vpn/internal/state"
	"github.com/vpavlin/logos-vpn/internal/waku"
	"github.com/vpavlin/logos-vpn/internal/wg"
)

// DefaultSocket is the daemon's control socket. The CLI is a thin client over
// it, which also gives monitoring a hook for free.
const DefaultSocket = "/run/logos-vpn/logos-vpn.sock"

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	verbose := fs.Bool("v", false, "verbose logging")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	st, err := state.LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}
	nk, err := cfg.Key()
	if err != nil {
		return err
	}

	self := identity.OverlayAddr(nk, st.Identity.DevicePub)
	log.Info("starting", "name", cfg.Name, "overlay", self, "prefix", nk.Prefix())

	// --- data plane ---
	tunDev, err := wg.CreateTUN(cfg.Interface, self, nk.Prefix(), wg.DefaultMTU)
	if err != nil {
		return fmt.Errorf("tun: %w (need CAP_NET_ADMIN)", err)
	}

	wgLevel := device.LogLevelError
	if *verbose {
		wgLevel = device.LogLevelVerbose
	}
	dev, err := wg.NewDevice(tunDev, st.Identity.WGPriv, cfg.ListenPort, device.NewLogger(wgLevel, "[wg] "))
	if err != nil {
		return fmt.Errorf("wireguard: %w", err)
	}
	defer dev.Close()
	log.Info("data plane up", "interface", cfg.Interface, "port", cfg.ListenPort)

	// --- rendezvous plane ---
	// clusterId alongside preset, not instead of it: the preset is what loads
	// the fleet's entry nodes, and the explicit cluster is what stops those
	// nodes hanging up on us. See state.DefaultClusterID.
	nodeCfg := waku.Config{"mode": cfg.Mode}
	// Only when explicitly set. Passing clusterId at all activates a legacy
	// cluster-to-network mapping in the library that overrides the preset, so
	// "helpfully" always sending it sends some nodes to the wrong fleet.
	if cfg.ClusterID != 0 {
		nodeCfg["clusterId"] = cfg.ClusterID
	}
	if cfg.Preset != "" {
		nodeCfg["preset"] = cfg.Preset
	}
	if len(cfg.EntryNodes) > 0 {
		nodeCfg["entryNodes"] = cfg.EntryNodes
	}
	node, err := waku.New(nodeCfg)
	if err != nil {
		return fmt.Errorf("rendezvous plane: %w", err)
	}
	defer node.Close()

	if err := node.Start(); err != nil {
		return fmt.Errorf("start rendezvous plane: %w", err)
	}
	log.Info("rendezvous plane up", "preset", cfg.Preset, "cluster", cfg.ClusterID, "mode", cfg.Mode)

	m, err := mesh.New(log, cfg, st, node, dev)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := serveControl(ctx, log, *sock, m, cfg, self)
	if err != nil {
		return err
	}
	defer srv.Close()

	// Names, on the overlay address only. Port 53 needs CAP_NET_BIND_SERVICE;
	// a failure here is logged and not fatal, because losing name resolution
	// is a much smaller thing than losing the tunnel.
	if pc, err := dnssrv.Listen(self); err != nil {
		log.Warn("name resolution unavailable", "err", err,
			"hint", "port 53 needs CAP_NET_BIND_SERVICE")
	} else {
		resolver := &dnssrv.Server{
			Suffix: cfg.HostsSuffix,
			Lookup: m.Lookup,
			Log:    func(msg string, args ...any) { log.Debug(msg, args...) },
		}
		go func() {
			if err := resolver.Serve(ctx, pc); err != nil {
				log.Error("name resolution stopped", "err", err)
			}
		}()
		log.Info("name resolution up", "address", self, "suffix", cfg.HostsSuffix)

		// Serving DNS and being asked are different things; the daemon used to
		// do only the first and report success. Scoped to the suffix, so the
		// system's own resolvers keep everything else.
		if err := dnssrv.Register(ctx, cfg.Interface, self, cfg.HostsSuffix); err != nil {
			log.Warn("could not register the resolver with the host; "+
				"mesh names will not resolve system-wide",
				"err", err,
				"hint", fmt.Sprintf("resolvectl dns %s %s && resolvectl domain %s '~%s'",
					cfg.Interface, self, cfg.Interface, cfg.HostsSuffix))
		} else {
			log.Info("resolver registered with the host",
				"interface", cfg.Interface, "domain", "~"+cfg.HostsSuffix)
			defer dnssrv.Unregister(cfg.Interface)
		}
	}

	log.Info("running", "socket", *sock)
	if err := m.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shutting down")
	return nil
}

// statusPayload is what the daemon reports over the control socket.
type statusPayload struct {
	Name    string       `json:"name"`
	Overlay string       `json:"overlay"`
	Prefix  string       `json:"prefix"`
	Peers   []peerStatus `json:"peers"`

	// Rendezvous is the health of the Waku side. Reported separately from
	// peers because the two planes fail independently: the fleet can be
	// unreachable while every tunnel keeps working, and without this that
	// situation is indistinguishable from "nobody else is online".
	Rendezvous rendezvousStatus `json:"rendezvous"`

	// Reflexive is what peers report observing our source address as. Several
	// distinct values means endpoint-dependent (symmetric) NAT, which is the
	// case where punching fails and a relay is required.
	Reflexive []string `json:"reflexive,omitempty"`
}

type pathStatus struct {
	Addr     string `json:"addr"`
	RTTMs    int64  `json:"rtt_ms"`
	LastPong string `json:"last_pong"`
	Selected bool   `json:"selected"`
}

type rendezvousStatus struct {
	Status      string `json:"status"` // Connected, PartiallyConnected, Disconnected, unknown
	OK          bool   `json:"ok"`
	Problem     string `json:"problem,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Topics      int    `json:"topics"`
	LastMessage string `json:"last_message,omitempty"`
	LastMsgAgeS int64  `json:"last_message_age_s,omitempty"`
}

type peerStatus struct {
	Name      string   `json:"name"`
	Overlay   string   `json:"overlay"`
	Endpoints []string `json:"endpoints"`
	Seq       uint64   `json:"seq"`
	LastSeen  string   `json:"last_seen"`
	Online    bool     `json:"online"`

	// Relay reports that this peer offers to forward for others. Worth
	// surfacing: "which of my peers can relay" is otherwise invisible, and it
	// is the first thing to check when a pair will not connect.
	Relay bool `json:"relay,omitempty"`

	// Data-plane view. Gossip says the peer exists; this says whether we can
	// actually reach it.
	// Paths are the candidates that answered a probe. Empty means either no
	// probing has completed or none of the peer's candidates are reachable.
	Paths []pathStatus `json:"paths,omitempty"`

	// Handshaked means a handshake has EVER completed; Live means the tunnel
	// works now. Both are reported because they answer different questions and
	// conflating them makes a dead peer look connected indefinitely.
	// How long each stage took, measured from daemon start. Reported because
	// "it connected eventually" is not a measurement, and the three stages fail
	// for different reasons: discovery is the rendezvous plane, path is NAT
	// traversal, tunnel is WireGuard.
	DiscoveredAfterS float64 `json:"discovered_after_s,omitempty"`
	PathAfterS       float64 `json:"path_after_s,omitempty"`
	TunnelAfterS     float64 `json:"tunnel_after_s,omitempty"`

	// Throughput, derived from WireGuard's cumulative counters. Bytes per
	// second, smoothed; history is a short sparkline, oldest first.
	RxBps     float64   `json:"rx_bps"`
	TxBps     float64   `json:"tx_bps"`
	RxHistory []float64 `json:"rx_history,omitempty"`
	TxHistory []float64 `json:"tx_history,omitempty"`

	Handshaked    bool   `json:"handshaked"`
	Live          bool   `json:"live"`
	HandshakeAgeS int64  `json:"handshake_age_s,omitempty"`
	LastHandshake string `json:"last_handshake,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	RxBytes       uint64 `json:"rx_bytes"`
	TxBytes       uint64 `json:"tx_bytes"`
}

// serveUI serves the same handler over loopback HTTP.
//
// Refuses anything but a loopback address. The payload names every device and
// address on the mesh, and "0.0.0.0:8787" in a config file is an easy way to
// publish that to the network without meaning to.
func serveUI(ctx context.Context, log *slog.Logger, addr string, h http.Handler) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ui_listen must be host:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("ui_listen must be a loopback address, got %q", host)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
			log.Error("monitoring endpoint stopped", "err", err)
		}
	}()
	log.Info("monitoring endpoint up", "url", "http://"+addr+"/status")
	return nil
}

// serveControl exposes status over a unix socket.
func serveControl(ctx context.Context, log *slog.Logger, path string, m *mesh.Mesh, cfg state.Config, self netip.Addr) (*http.Server, error) {
	// systemd's RuntimeDirectory creates this, but the daemon must also work
	// when run by hand.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	_ = os.Remove(path) // a stale socket from an unclean exit would block bind

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		log.Warn("chmod control socket", "err", err)
	}

	nk, _ := cfg.Key()

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		out := statusPayload{
			Name:    cfg.Name,
			Overlay: self.String(),
			Prefix:  nk.Prefix().String(),
		}
		h := m.Health()
		out.Rendezvous = rendezvousStatus{
			Status:  h.Status,
			OK:      h.OK(now),
			Problem: h.Problem(now),
			Detail:  h.Detail(now),
			Topics:  h.Topics,
		}
		if !h.LastMessage.IsZero() {
			out.Rendezvous.LastMessage = h.LastMessage.Format(time.RFC3339)
			out.Rendezvous.LastMsgAgeS = int64(now.Sub(h.LastMessage).Seconds())
		}

		stats, _ := m.PeerStats()
		for _, p := range m.Roster().Peers() {
			ps := peerStatus{
				Name:      p.Name,
				Overlay:   p.Overlay.String(),
				Endpoints: p.Endpoints,
				Seq:       p.Seq,
				LastSeen:  p.LastSeen.Format(time.RFC3339),
				Online:    p.Online(now),
				Relay:     p.Relay,
			}
			if r := m.Rate(p.ID()); r.RxBps > 0 || r.TxBps > 0 || len(r.RxHistory) > 0 {
				ps.RxBps, ps.TxBps = r.RxBps, r.TxBps
				ps.RxHistory, ps.TxHistory = r.RxHistory, r.TxHistory
			}
			if t := m.Timing(p.ID()); t.DiscoveredAfter > 0 || t.TunnelAfter > 0 {
				ps.DiscoveredAfterS = t.DiscoveredAfter.Seconds()
				ps.PathAfterS = t.PathAfter.Seconds()
				ps.TunnelAfterS = t.TunnelAfter.Seconds()
			}
			best, hasBest := m.BestPath(p.ID(), now)
			for _, path := range m.Paths(p.ID()) {
				ps.Paths = append(ps.Paths, pathStatus{
					Addr:     path.Addr.String(),
					RTTMs:    path.RTT.Milliseconds(),
					LastPong: path.LastPong.Format(time.RFC3339),
					Selected: hasBest && path.Addr == best.Addr,
				})
			}
			if st, ok := stats[p.WGPub.String()]; ok {
				ps.Endpoint = st.Endpoint
				ps.RxBytes = st.RxBytes
				ps.TxBytes = st.TxBytes
				if st.Handshaked() {
					ps.Handshaked = true
					ps.LastHandshake = st.LastHandshake.Format(time.RFC3339)
					ps.HandshakeAgeS = int64(now.Sub(st.LastHandshake).Seconds())
					ps.Live = st.Live(now)
				}
			}
			out.Peers = append(out.Peers, ps)
		}
		for _, ap := range m.Reflexive() {
			out.Reflexive = append(out.Reflexive, ap.String())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	if cfg.UIListen != "" {
		if err := serveUI(ctx, log, cfg.UIListen, mux); err != nil {
			log.Warn("monitoring endpoint unavailable", "listen", cfg.UIListen, "err", err)
		}
	}

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("control socket", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	return srv, nil
}
