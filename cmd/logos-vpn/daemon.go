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
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/device"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/mesh"
	"github.com/vpavlin/logos-vpn/internal/state"
	"github.com/vpavlin/logos-vpn/internal/waku"
	"github.com/vpavlin/logos-vpn/internal/wg"
)

// DefaultSocket is the daemon's control socket. The CLI is a thin client over
// it, which also gives monitoring a hook for free.
const DefaultSocket = "/run/logos-vpn.sock"

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
	node, err := waku.New(waku.Config{"mode": cfg.Mode, "preset": cfg.Preset})
	if err != nil {
		return fmt.Errorf("waku: %w", err)
	}
	defer node.Close()

	if err := node.Start(); err != nil {
		return fmt.Errorf("waku start: %w", err)
	}
	log.Info("rendezvous plane up", "preset", cfg.Preset, "mode", cfg.Mode)

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
}

type peerStatus struct {
	Name      string   `json:"name"`
	Overlay   string   `json:"overlay"`
	Endpoints []string `json:"endpoints"`
	Seq       uint64   `json:"seq"`
	LastSeen  string   `json:"last_seen"`
	Online    bool     `json:"online"`

	// Data-plane view. Gossip says the peer exists; this says whether we can
	// actually reach it.
	Handshaked    bool   `json:"handshaked"`
	LastHandshake string `json:"last_handshake,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	RxBytes       uint64 `json:"rx_bytes"`
	TxBytes       uint64 `json:"tx_bytes"`
}

// serveControl exposes status over a unix socket.
func serveControl(ctx context.Context, log *slog.Logger, path string, m *mesh.Mesh, cfg state.Config, self netip.Addr) (*http.Server, error) {
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
		stats, _ := m.PeerStats()
		for _, p := range m.Roster().Peers() {
			ps := peerStatus{
				Name:      p.Name,
				Overlay:   p.Overlay.String(),
				Endpoints: p.Endpoints,
				Seq:       p.Seq,
				LastSeen:  p.LastSeen.Format(time.RFC3339),
				Online:    p.Online(now),
			}
			if st, ok := stats[p.WGPub.String()]; ok {
				ps.Endpoint = st.Endpoint
				ps.RxBytes = st.RxBytes
				ps.TxBytes = st.TxBytes
				if st.Handshaked() {
					ps.Handshaked = true
					ps.LastHandshake = st.LastHandshake.Format(time.RFC3339)
				}
			}
			out.Peers = append(out.Peers, ps)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

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
