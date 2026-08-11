package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/waku"
	"github.com/vpavlin/shrooms/internal/wg"
)

// DefaultSocket is the daemon's control socket. The CLI is a thin client over
// it, which also gives monitoring a hook for free.
const DefaultSocket = "/run/shrooms/shrooms.sock"

// LegacySocket is where the daemon listened before the rename. The CLI falls
// back to it so a running pre-rename daemon stays reachable from a new binary.
const LegacySocket = "/run/logos-vpn/logos-vpn.sock"

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

	ctx0, cancel0 := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel0()

	// No key yet — no mesh yet. Wait to be told which one this is rather than
	// exiting, which under systemd is a restart loop nobody reads.
	if unset, err := configHasNoKey(*cfgPath); err != nil {
		return err
	} else if unset {
		return runWaiting(ctx0, log, *cfgPath, *stateDir, *sock)
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	st, err := state.LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}

	// --- rendezvous plane, shared by every mesh ---
	//
	// The expensive part, and the reason this is one daemon rather than one per
	// mesh: a Core node costs ~20 MB/h whether it carries one mesh or five.
	//
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

	ctx := ctx0

	// --- one data plane per mesh (ADR-015) ---
	//
	// A WireGuard device holds one static key and allows one preshared key per
	// peer, and both are per mesh here — so meshes cannot share a device, and
	// two devices cannot share a port. Interfaces and ports are numbered from
	// the configured ones, so a config naming one mesh is exactly what it was.
	meshes := cfg.Active()
	if len(meshes) == 0 {
		return errors.New("no meshes are enabled")
	}
	var instances []*instance
	defer func() {
		for _, in := range instances {
			in.Close()
		}
	}()

	for i, mc := range meshes {
		iface, port := ifaceAndPort(cfg, i)
		// Legacy by identity, not by position: the mesh written as network_key
		// is the one this device already belonged to, and keeps its keys —
		// re-deriving them would change its address and make it a stranger to
		// every peer. A mesh labelled "aaa" sorts first and is not it.
		in, err := startInstance(ctx, log, cfg, st, node, mc, iface, port,
			isLegacyMesh(cfg, mc), *verbose)
		if in != nil {
			instances = append(instances, in)
		}
		if err != nil {
			return err
		}
	}

	primary := instances[0]
	self := primary.self
	log.Info("data plane up", "meshes", len(instances),
		"self", self, "ipv4", primary.aliases.Self())

	rl := &reloader{cfgPath: *cfgPath, log: log, instances: instances, baseline: cfg}
	srv, err := serveControl(ctx, log, *sock, instances, cfg, rl)
	if err != nil {
		return err
	}
	defer srv.Close()

	// Names, on the primary mesh's address. Port 53 needs CAP_NET_BIND_SERVICE;
	// a failure here is logged and not fatal, because losing name resolution is
	// a much smaller thing than losing the tunnel.
	if pc, err := dnssrv.Listen(self); err != nil {
		log.Warn("name resolution unavailable", "err", err,
			"hint", "port 53 needs CAP_NET_BIND_SERVICE")
	} else {
		resolver := &dnssrv.Server{
			Suffix: cfg.HostsSuffix,
			Lookup: resolveAcross(named(instances)),
			Alias:  aliasAcross(named(instances)),
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

	// One reader, every mesh.
	//
	// The node's event channel delivers each event to exactly one reader, so
	// meshes reading it directly take turns and each drops what the other
	// should have had — which presents as a second mesh that discovers peers
	// slowly, or in one direction only. Left alone for a single mesh, where
	// there is nothing to share and the existing path is well travelled.
	if len(instances) > 1 {
		for _, in := range instances {
			in.mesh.SetFed()
		}
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-node.Events():
					if !ok {
						return
					}
					for _, in := range instances {
						in.mesh.Deliver(ev)
					}
				}
			}
		}()
		log.Info("rendezvous events fanned out", "meshes", len(instances))
	}

	// Every mesh runs; the first one to stop stops the daemon, because a node
	// that is half up is worse than one that restarts.
	errs := make(chan error, len(instances))
	for _, in := range instances {
		go func(in *instance) { errs <- in.mesh.Run(ctx) }(in)
	}

	// Reload on SIGHUP, and over the socket, so `systemctl reload shrooms` and
	// `shrooms reload` both work. Only the safe parts change; the rest is
	// reported rather than silently ignored.
	go func() {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				msg, err := rl.Reload(ctx)
				if err != nil {
					log.Warn("reload failed", "err", err)
					continue
				}
				log.Info(msg)
			}
		}
	}()

	log.Info("running", "socket", *sock)
	if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shutting down")
	return nil
}

// meshStatus is one mesh, for a node that has more than one.
type meshStatus struct {
	Label     string `json:"label"`
	Overlay   string `json:"overlay"`
	OverlayV4 string `json:"overlay_v4,omitempty"`
	Prefix    string `json:"prefix"`
	Iface     string `json:"interface"`
	Port      uint16 `json:"port"`
	Peers     int    `json:"peers"`

	// Expires is when this device's credential on this mesh runs out. Reported
	// because a credential expiring is the one failure in this system that is
	// scheduled: it happens on a known day, it takes the device off the mesh,
	// and nothing else on the status page hints at it. Zero when the mesh has
	// no admin keys, where membership is the network key and does not lapse.
	Expires int64 `json:"expires,omitempty"`
}

// statusPayload is what the daemon reports over the control socket.
type statusPayload struct {
	// Waiting means this daemon has no mesh yet and is holding the socket open
	// until someone tells it which one it belongs to.
	Waiting bool `json:"waiting,omitempty"`

	// OverlayV4 is this device's synthetic IPv4 address (ADR-021). Reported
	// because two addresses now name the same machine, and an address nothing
	// explains gets reported as a bug.
	OverlayV4 string `json:"overlay_v4,omitempty"`

	// Meshes is one entry per mesh this node has joined (ADR-015). A
	// single-mesh node reports one, and everything above still describes it.
	Meshes []meshStatus `json:"meshes,omitempty"`

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

	// Services are the local ports this device publishes on the mesh. Only
	// this device's own: services are not announced, so no node knows what any
	// other one publishes.
	Services []serviceStatus `json:"services,omitempty"`

	// NameRouter is the shared-port router that lets a service be reached
	// without a port at all. Reported separately because it can fail on its
	// own — port 80 needs a capability the services' own ports do not.
	NameRouter []routerStatus `json:"name_router,omitempty"`
}

// routerStatus is one shared port of the name router.
type routerStatus struct {
	Port      uint16 `json:"port"`
	Listening bool   `json:"listening"`
	Direct    bool   `json:"direct,omitempty"`
	Err       string `json:"err,omitempty"`
}

// serviceStatus is one published local service.
type serviceStatus struct {
	Name   string `json:"name"`
	Port   uint16 `json:"port"`
	Target string `json:"target"`

	// DNSName is this device's name, so a viewer can render the full
	// <service>.<device>.<suffix> without reimplementing the sanitising.
	DNSName string `json:"dns_name,omitempty"`

	Listening bool   `json:"listening"`
	Direct    bool   `json:"direct,omitempty"`
	Conns     uint64 `json:"conns"`
	Err       string `json:"err,omitempty"`
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
	// Mesh is which mesh this peer is on, empty on a single-mesh node.
	Mesh string `json:"mesh,omitempty"`

	Name string `json:"name"`
	// OverlayV4 is the synthetic IPv4 address this device uses for the peer
	// (ADR-021). Local to this node: another device may call it something else.
	OverlayV4 string `json:"overlay_v4,omitempty"`
	// DNSName, Relayed and RTTMs exist so a viewer never has to re-derive what
	// the daemon already knows — parsing "relay:" out of an endpoint string, or
	// reimplementing name sanitising, is how a front-end drifts from what
	// actually resolves.
	DNSName string `json:"dns_name,omitempty"`
	Relayed bool   `json:"relayed"`
	RTTMs   int64  `json:"rtt_ms,omitempty"`

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

// inviteHolder is the part of the mesh the enrolment endpoints need. An
// interface so the handlers can be tested without a rendezvous node, which is
// otherwise the only way to reach them.
type inviteHolder interface {
	HoldInvite(ctx context.Context, s invite.Secret) (*invite.Request, error)
	ReplyInvite(s invite.Secret, req *invite.Request, credential []byte) error
}

// inviteHandlers registers the enrolment endpoints.
//
// pick chooses which mesh an invite belongs to: an empty label means the first,
// which is what a single-mesh node and an older CLI both send.
func inviteHandlers(mux *http.ServeMux, pick func(string) inviteHolder) {
	// Enrolment, held open by the daemon because it is the thing that is
	// already connected — `shrooms invite` used to start a Logos Delivery node
	// of its own for the sake of two messages.
	//
	// Two calls rather than one, because a credential can only be signed once
	// the joining device's keys are known, and the admin key is deliberately
	// not here. /invite/hold blocks until a request arrives and returns it; the
	// CLI signs; /invite/reply publishes what it signed. The token is what
	// names the exchange, so this cannot be used to publish on any other topic.
	mux.HandleFunc("/invite/hold", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string `json:"token"`
			TTLS  int    `json:"ttl_s"`
			Mesh  string `json:"mesh"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		secret, err := invite.Parse(req.Token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := pick(req.Mesh)
		if m == nil {
			http.Error(w, "no mesh called "+req.Mesh+" on this node", http.StatusBadRequest)
			return
		}
		ttl := time.Duration(req.TTLS) * time.Second
		if ttl <= 0 || ttl > 2*time.Hour {
			ttl = invite.DefaultTTL
		}
		ctx, cancel := context.WithTimeout(r.Context(), ttl)
		defer cancel()

		got, err := m.HoldInvite(ctx, secret)
		if err != nil {
			// Expiry is the ordinary outcome of an invite nobody used, so it
			// is a status rather than an error the CLI has to interpret.
			if errors.Is(err, context.DeadlineExceeded) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device_pub": hex.EncodeToString(got.DevicePub),
			"wg_pub":     hex.EncodeToString(got.WGPub),
			"name":       got.Name,
			"eph_pub":    hex.EncodeToString(got.EphPub),
		})
	}))

	mux.HandleFunc("/invite/reply", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token      string `json:"token"`
			EphPub     string `json:"eph_pub"`
			Name       string `json:"name"`
			Credential string `json:"credential"`
			Mesh       string `json:"mesh"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		secret, err := invite.Parse(in.Token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		eph, err := hex.DecodeString(in.EphPub)
		if err != nil {
			http.Error(w, "eph_pub: "+err.Error(), http.StatusBadRequest)
			return
		}
		var credential []byte
		if in.Credential != "" {
			if credential, err = base64.StdEncoding.DecodeString(in.Credential); err != nil {
				http.Error(w, "credential: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		m := pick(in.Mesh)
		if m == nil {
			http.Error(w, "no mesh called "+in.Mesh+" on this node", http.StatusBadRequest)
			return
		}
		if err := m.ReplyInvite(secret, &invite.Request{EphPub: eph, Name: in.Name}, credential); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// listenControl binds the control socket and serves a handler on it.
//
// Shared with the waiting daemon (see waiting.go), which has no mesh to report
// but the same socket to own — including its permissions, which are the only
// thing standing between the socket group and the mutating endpoints.
func listenControl(ctx context.Context, log *slog.Logger, path string, h http.Handler, cfg state.Config) (*http.Server, error) {
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
	// 0660 is only useful with a group somebody is in. The daemon needs
	// CAP_NET_ADMIN and so runs as root; without this the socket is root:root
	// and every `shrooms status` needs sudo — including, every time, right
	// after a restart has quietly reset whatever chgrp you did by hand.
	if cfg.SocketGroup != "" {
		if gid, err := lookupGroup(cfg.SocketGroup); err != nil {
			log.Warn("socket group not applied; status will need root",
				"group", cfg.SocketGroup, "err", err)
		} else if err := os.Chown(path, -1, gid); err != nil {
			log.Warn("could not set the group on the control socket", "err", err)
		} else {
			log.Info("control socket readable by group", "group", cfg.SocketGroup)
		}
	}

	srv := &http.Server{Handler: h, ConnContext: withPeerCred}
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

// v4Of renders a peer's synthetic address, or "" when there is none.
func v4Of(m *mesh.Mesh, overlay netip.Addr) string {
	if a, ok := m.LookupV4(overlay); ok {
		return a.String()
	}
	return ""
}

// serveControl exposes status over a unix socket.
func serveControl(ctx context.Context, log *slog.Logger, path string, instances []*instance, cfg state.Config, rl *reloader) (*http.Server, error) {
	// The first mesh is what the top-level fields describe, so a reader that
	// knows nothing about several meshes — the Android app, Basecamp, an older
	// CLI — sees exactly what it always saw. The rest are added alongside.
	primary := instances[0]
	m, self := primary.mesh, primary.self

	nk, _ := cfg.Key()

	mux := http.NewServeMux()
	// One snapshot builder, read by the CLI over the unix socket and by the
	// status file. Two copies is how two front-ends quietly drift.
	snapshot := func() statusPayload {
		now := time.Now()
		out := statusPayload{
			Name:    cfg.Name,
			Overlay: self.String(),
			Prefix:  nk.Prefix().String(),
		}
		if v4self, ok := m.LookupV4(self); ok {
			out.OverlayV4 = v4self.String()
		}
		for _, in := range instances {
			ms := meshStatus{
				Label:   in.label,
				Overlay: in.self.String(),
				Prefix:  in.prefix.String(),
				Iface:   in.iface,
				Port:    in.port,
				Peers:   len(in.mesh.Roster().Current(now)),
			}
			if e := in.mesh.SelfExpiry(); !e.IsZero() {
				ms.Expires = e.Unix()
			}
			if a, ok := in.mesh.LookupV4(in.self); ok {
				ms.OverlayV4 = a.String()
			}
			out.Meshes = append(out.Meshes, ms)
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

		// Every mesh's peers, not just the first one's. A node with two meshes
		// counted the second's peers and then listed none of them, which reads
		// as "it says one peer and shows me nothing".
		for _, in := range instances {
			m, stats := in.mesh, map[string]wg.PeerStat{}
			if s, err := in.mesh.PeerStats(); err == nil {
				stats = s
			}
			meshLabel := ""
			if len(instances) > 1 {
				meshLabel = in.label
			}
			for _, p := range m.Roster().Current(now) {
				ps := peerStatus{
					Mesh:      meshLabel,
					Name:      p.Name,
					Overlay:   p.Overlay.String(),
					OverlayV4: v4Of(m, p.Overlay),
					Endpoints: p.Endpoints,
					Seq:       p.Seq,
					LastSeen:  p.LastSeen.Format(time.RFC3339),
					Online:    p.Online(now),
					Relay:     p.Relay,
					DNSName:   mesh.DNSName(p.Name, cfg.HostsSuffix),
				}
				if best, ok := m.BestPath(p.ID(), now); ok {
					ps.RTTMs = best.RTT.Milliseconds()
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
					// A relayed endpoint serialises with a relay: prefix; say so as
					// a field so no viewer has to parse the string.
					ps.Relayed = strings.HasPrefix(st.Endpoint, "relay:")
					if st.Handshaked() {
						ps.Handshaked = true
						ps.LastHandshake = st.LastHandshake.Format(time.RFC3339)
						ps.HandshakeAgeS = int64(now.Sub(st.LastHandshake).Seconds())
						ps.Live = st.Live(now)
					}
				}
				out.Peers = append(out.Peers, ps)
			}
		}
		for _, ap := range m.Reflexive() {
			out.Reflexive = append(out.Reflexive, ap.String())
		}
		if services := primary.services; services != nil {
			for _, sv := range services.Status() {
				out.Services = append(out.Services, serviceStatus{
					Name:      sv.Name,
					Port:      sv.Port,
					Target:    sv.Target,
					DNSName:   mesh.DNSName(cfg.Name, cfg.HostsSuffix),
					Listening: sv.Listening,
					Direct:    sv.Direct,
					Conns:     sv.Conns,
					Err:       sv.Err,
				})
			}
			sort.Slice(out.Services, func(i, j int) bool {
				return out.Services[i].Name < out.Services[j].Name
			})
			for _, r := range primary.services.Router() {
				out.NameRouter = append(out.NameRouter, routerStatus{
					Port: r.Port, Listening: r.Listening, Direct: r.Direct, Err: r.Err,
				})
			}
		}
		return out
	}

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshot())
	})

	// Config changes that can be applied while running (services, today).
	// requireRoot like every mutating endpoint: it re-reads a file only root
	// can write, but it also rebinds ports, and the socket group is for
	// reading status.
	if rl != nil {
		mux.HandleFunc("/reload", requireRoot(func(w http.ResponseWriter, r *http.Request) {
			msg, err := rl.Reload(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			log.Info(msg)
			fmt.Fprintln(w, msg)
		}))
	}

	// Which mesh an admin request is about. Empty means the primary one, which
	// is what a single-mesh node has and what older tooling sends.
	pickMesh := func(label string) *mesh.Mesh {
		for _, in := range instances {
			if label == "" || in.label == label {
				return in.mesh
			}
		}
		return nil
	}

	inviteHandlers(mux, func(label string) inviteHolder {
		for _, in := range instances {
			if label == "" || in.label == label {
				return in.mesh
			}
		}
		return nil
	})

	// The way a revocation reaches the bus. The admin key signs offline — that
	// is the whole point of it — so something with a rendezvous connection has
	// to publish what it signed, and the daemon is the only such thing here.
	//
	// The socket's permissions decide who may ask; the signature inside decides
	// whether it is honoured. A caller who can reach this socket can already
	// read every peer's endpoints, and cannot forge a revocation with it.
	mux.HandleFunc("/revoke", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST a revocation", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			http.Error(w, "revocation is not base64: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.Revoke(blob); err != nil {
			log.Warn("refused a revocation", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// The mirror of /revoke: how a renewed credential reaches the mesh.
	//
	// Same reasoning about permissions. This one carries a credential rather
	// than a withdrawal, and the mesh verifies the admin signature on arrival
	// exactly as every other node will — a caller who can reach this socket
	// cannot mint membership with it.
	mux.HandleFunc("/grant", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST a credential", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			http.Error(w, "credential is not base64: "+err.Error(), http.StatusBadRequest)
			return
		}
		target := pickMesh(r.URL.Query().Get("mesh"))
		if target == nil {
			http.Error(w, "no such mesh", http.StatusNotFound)
			return
		}
		if err := target.Grant(blob); err != nil {
			log.Warn("refused a credential", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// Who is on the mesh, in the form the admin tooling needs to reissue for
	// them: both public keys, the name, and when what they hold runs out.
	//
	// Not part of /status, which is a display: this is keys, and a renewal
	// sweep is the only thing that wants them.
	mux.HandleFunc("/members", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		type member struct {
			DevicePub string `json:"device_pub"`
			WGPub     string `json:"wg_pub"`
			Name      string `json:"name"`
			NotAfter  int64  `json:"not_after,omitempty"`
		}
		target := pickMesh(r.URL.Query().Get("mesh"))
		if target == nil {
			http.Error(w, "no such mesh", http.StatusNotFound)
			return
		}
		out := []member{}
		for _, mem := range target.Members() {
			e := member{
				DevicePub: hex.EncodeToString(mem.DevicePub),
				WGPub:     hex.EncodeToString(mem.WGPub),
				Name:      mem.Name,
			}
			if !mem.NotAfter.IsZero() {
				e.NotAfter = mem.NotAfter.Unix()
			}
			out = append(out, e)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))

	// A file, not a port: QML can read a file and cannot open a unix socket,
	// and access is then decided by file permissions rather than by a listener
	// a VPN daemon has no other reason to run.
	if cfg.StatusFile != "" {
		if err := writeStatusFile(ctx, log, cfg.StatusFile, cfg.StatusFileGroup, snapshot); err != nil {
			log.Warn("could not write the status file", "path", cfg.StatusFile, "err", err)
		} else {
			log.Info("status file up", "path", cfg.StatusFile)
		}
	}

	if cfg.UIListen != "" {
		if err := serveUI(ctx, log, cfg.UIListen, mux); err != nil {
			log.Warn("monitoring endpoint unavailable", "listen", cfg.UIListen, "err", err)
		}
	}

	return listenControl(ctx, log, path, mux, cfg)
}
