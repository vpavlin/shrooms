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
	"sync/atomic"
	"syscall"
	"time"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/logtail"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/rendezvous"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
	"github.com/vpavlin/shrooms/internal/waku"
	"github.com/vpavlin/shrooms/internal/wg"
)

// DefaultSocket is the daemon's control socket. The CLI is a thin client over
// it, which also gives monitoring a hook for free.
const DefaultSocket = "/run/shrooms/shrooms.sock"

// LegacySocket is where the daemon listened before the rename. The CLI falls
// back to it so a running pre-rename daemon stays reachable from a new binary.
const LegacySocket = "/run/logos-vpn/logos-vpn.sock"

// errRestartRequested ends the daemon because somebody asked it to, over the
// control socket. Distinct from a fault so the exit line says which it was —
// "restarted on request" and "restarted because the rendezvous plane was deaf"
// look identical in a journal otherwise, and they are the two things anybody
// reading that journal is trying to tell apart.
var errRestartRequested = errors.New("restart requested over the control socket")

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
	// Everything logged to stderr is also kept in a short in-memory tail, so a
	// UI on the other end of the control socket can show it. Basecamp cannot
	// read the journal — it cannot read a file at all — and "what is it doing"
	// is the first question anybody asks a mesh that has not come up.
	tail := logtail.NewRing(200)
	log := slog.New(logtail.New(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), tail))

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
	// Configured addresses first, then ones peers published (ADR-031).
	//
	// Configured never displaced by learned: an operator who wrote an address
	// down meant it, and learned ones come from members — who cannot forge an
	// announce, but could offer a node that answers selectively. Order is the
	// cheap defence.
	//
	// The preset still supplies its own when this is empty, so a node that has
	// never met anybody behaves exactly as before.
	entry := append([]string(nil), cfg.EntryNodes...)
	if learned := st.BootPeers(time.Now()); len(learned) > 0 {
		entry = append(entry, learned...)
		log.Info("bootstrapping from addresses peers published",
			"configured", len(cfg.EntryNodes), "learned", len(learned))
	}
	if len(entry) > 0 {
		nodeCfg["entryNodes"] = entry
	}
	// tcpPort, confirmed against the library's own AvailableConfigs rather than
	// guessed. The binary also contains "p2pTcpPort" and a withP2pTcpPort
	// builder, which the FFI configuration layer rejects outright —
	// "Unrecognized configuration option(s)" — so the strings in the shared
	// object are not the list of keys this accepts.
	if cfg.DeliveryPort != 0 {
		nodeCfg["tcpPort"] = cfg.DeliveryPort
	}
	// A stable libp2p identity, so a bootstrap address this node publishes
	// keeps working after it restarts (ADR-031). Best effort: without it the
	// library invents one per start, which is what every node did before.
	if k, err := st.NodeKey(); err == nil {
		nodeCfg["nodekey"] = k
	} else {
		log.Warn("could not keep a stable rendezvous identity; peers that "+
			"bootstrap from this node will need a fresh address after a restart",
			"err", err)
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

	// One block per mesh, assigned across the set so two meshes cannot route
	// the same synthetic range (ADR-021).
	ids := make([]string, 0, len(meshes))
	for _, mc := range meshes {
		id, err := mc.NetworkID()
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	blocks := v4.Blocks(ids)

	for i, mc := range meshes {
		iface, port := ifaceAndPort(cfg, i)
		id, err := mc.NetworkID()
		if err != nil {
			return err
		}
		// Legacy by identity, not by position: the mesh written as network_key
		// is the one this device already belonged to, and keeps its keys —
		// re-deriving them would change its address and make it a stranger to
		// every peer. A mesh labelled "aaa" sorts first and is not it.
		primary := isLegacyMesh(cfg, mc)
		in, err := startInstance(ctx, log, cfg, st, node, mc, iface, port,
			blocks[id], primary, *verbose)
		if in != nil {
			in.primary = primary
		}
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

	// How anything inside this process asks for a restart: the rendezvous
	// watchdog below, and the control socket. One channel, one exit path — the
	// alternative is two ways to end a daemon that behave differently on the
	// day one of them is wrong. Buffered for every mesh plus the socket, so a
	// send never blocks a caller that is holding a lock.
	errs := make(chan error, len(instances)+1)
	rt := &runtimeBits{
		tail: tail, restart: errs, dns: &atomic.Pointer[dnsStatus]{},
		invites: rendezvous.InviteTransport(node), st: st, cfgPath: *cfgPath,
	}
	rl := &reloader{cfgPath: *cfgPath, log: log, instances: instances, baseline: cfg}
	srv, err := serveControl(ctx, log, *sock, instances, cfg, rl, rt)
	if err != nil {
		return err
	}
	defer srv.Close()

	// Names, on the primary mesh's address. Port 53 needs CAP_NET_BIND_SERVICE;
	// a failure here is logged and not fatal, because losing name resolution is
	// a much smaller thing than losing the tunnel.
	dns := dnsStatus{Suffix: cfg.HostsSuffix, Address: self.String()}
	if pc, err := dnssrv.Listen(self); err != nil {
		log.Warn("name resolution unavailable", "err", err,
			"hint", "port 53 needs CAP_NET_BIND_SERVICE")
		dns.Err = err.Error()
		rt.dns.Store(&dns)
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
		dns.Serving = true
		rt.dns.Store(&dns)

		// Serving DNS and being asked are different things; the daemon used to
		// do only the first and report success. Scoped to the suffix, so the
		// system's own resolvers keep everything else.
		if err := dnssrv.Register(ctx, cfg.Interface, self, cfg.HostsSuffix); err != nil {
			dns.Err = err.Error()
			rt.dns.Store(&dns)
			log.Warn("could not register the resolver with the host; "+
				"mesh names will not resolve system-wide",
				"err", err,
				"hint", fmt.Sprintf("resolvectl dns %s %s && resolvectl domain %s '~%s'",
					cfg.Interface, self, cfg.Interface, cfg.HostsSuffix))
		} else {
			log.Info("resolver registered with the host",
				"interface", cfg.Interface, "domain", "~"+cfg.HostsSuffix)
			dns.Registered = true
			rt.dns.Store(&dns)
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
	for _, in := range instances {
		go func(in *instance) { errs <- in.mesh.Run(ctx) }(in)
	}

	go watchRendezvous(ctx, log, instances, *stateDir, errs)

	// Ask the router for a way in, per mesh, unless told not to. Best effort
	// by construction: a router that refuses leaves the node exactly where it
	// was (ADR-024).
	if cfg.PortMapping {
		for _, in := range instances {
			go keepMapped(ctx, log, in)
		}
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
		// A requested restart is not a fault. It still exits non-zero, because
		// under `Restart=always` that is what brings the daemon back and the
		// unit is the only thing that can restart it — but the log says which
		// of the two happened.
		if errors.Is(err, errRestartRequested) {
			log.Info("stopping so the service manager starts a fresh daemon")
		}
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

	// Relays counts the devices on this mesh that offer to forward, this one
	// included. Reported because zero is a configuration, not a fault, and it
	// is invisible until a peer on mobile data cannot reach anything.
	Relays int `json:"relays"`

	// BlindRelays counts relays configured on this device that are not members
	// of the mesh (docs/blind-relays.md). Counted separately because they can
	// never appear in the roster: they hold no network key and never announce.
	BlindRelays int `json:"blind_relays,omitempty"`

	// RelayUsing is the relay currently carrying traffic, empty when none is.
	RelayUsing string `json:"relay_using,omitempty"`
	// RelayUsingBlind says that relay is one somebody else runs.
	RelayUsingBlind bool `json:"relay_using_blind,omitempty"`

	// What the config says about this mesh, so a UI can show the current
	// setting rather than only offering the buttons that change it.
	//
	// This was the whole of why the settings section read as broken: every
	// toggle was a pair of actions in fixed colours, with nothing anywhere
	// saying which one was in force. "on" looked highlighted because it was a
	// button, not because the mesh was on.
	//
	// Read from the config on disk rather than from the running instance,
	// because that is what these controls write and what a restart will apply
	// — and showing the running value after a click would report the click as
	// having done nothing.
	Relay            bool `json:"relay,omitempty"`
	AnnounceServices bool `json:"announce_services,omitempty"`
	AnnounceBound    bool `json:"announce_bound,omitempty"`

	// BoundHere is what is listening on *this* device's address on this mesh,
	// whether or not it is announced — the same list `shrooms bound` prints.
	//
	// Reported because a switch that discloses something should be next to the
	// thing it would disclose. Deciding whether to announce your bound ports
	// without being shown which ones they are is a decision taken blind, and
	// the answer changes every time somebody starts a server and forgets.
	BoundHere []string `json:"bound_here,omitempty"`

	// NotRunning means the config has this mesh and this process does not.
	// Switching one on writes the config and takes effect at the next restart,
	// so between those two moments it belongs to neither list — which made the
	// whole settings section vanish the first time somebody switched one back
	// on. Absent on an older daemon, which only ever listed running meshes, so
	// absent must keep meaning "running".
	NotRunning bool `json:"not_running,omitempty"`

	// Left means the opposite: this process is running a mesh the config no
	// longer has. Leaving does not tear down a tunnel, so it keeps running
	// until a restart, and saying nothing would make a completed `leave` look
	// like it failed.
	Left bool `json:"left,omitempty"`

	// Disabled means this device is a member of the mesh and is not running
	// it. Reported because the alternative is what happened: switching a mesh
	// off removed it from the only list any front-end had, so the reversible
	// half of leaving became the irreversible-looking one. There is no
	// instance behind these entries, so everything below is absent.
	Disabled bool `json:"disabled,omitempty"`

	// Expires is when this device's credential on this mesh runs out. Reported
	// because a credential expiring is the one failure in this system that is
	// scheduled: it happens on a known day, it takes the device off the mesh,
	// and nothing else on the status page hints at it. Zero when the mesh has
	// no admin keys, where membership is the network key and does not lapse.
	Expires int64 `json:"expires,omitempty"`

	// Unenrolled means this mesh admits by credential and this device holds
	// none, so every peer refuses its announces before they reach a roster.
	//
	// Reported because it is invisible from here by construction: the node is
	// running, its tunnel state is fine, and the failure appears only on other
	// people's machines as a peer that never shows up. Without this the status
	// page of a node nobody will admit is indistinguishable from a healthy one.
	Unenrolled bool `json:"unenrolled,omitempty"`
}

// statusPayload is what the daemon reports over the control socket.
// relayStatus is a relay's own account of itself.
type relayStatus struct {
	Peers      int    `json:"peers"`
	Registered uint64 `json:"registered"`
	Forwarded  uint64 `json:"forwarded"`
	Dropped    uint64 `json:"dropped"`
	Refused    uint64 `json:"refused"`
}

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

	// Mode is what the config says this node contributes to the rendezvous
	// plane, and ModeRunning is what this process actually started as. They
	// differ exactly between a change and the restart that applies it, and a
	// UI that shows only one of them either hides the click or hides the
	// truth.
	Mode        string `json:"mode,omitempty"`
	ModeRunning string `json:"mode_running,omitempty"`

	// PortMapping is whether the router is asked for a way in (ADR-024).
	// A pointer so "off" and "an older daemon that never said" stay distinct:
	// it defaults to on, so a missing field rendered as off would show every
	// old daemon as having a setting it does not have.
	PortMapping *bool `json:"port_mapping,omitempty"`

	// Version is this daemon's build, so a UI can say what it is talking to.
	// The Android app has always shown its own; the desktop showed the
	// module's, which is a different thing and the wrong one — "is the daemon
	// new enough for this?" is the question actually being asked.
	Version string `json:"version,omitempty"`

	// DNS is whether mesh names resolve on this device, and where. Reported
	// because it fails on its own and quietly: port 53 needs a capability, the
	// system resolver needs to be told about the suffix, and either can be
	// missing while everything else is perfect. Then `ssh laptop.mesh` does
	// not work and nothing on the page hints at why.
	DNS dnsStatus `json:"dns"`

	// Rendezvous is the health of the Waku side. Reported separately from
	// peers because the two planes fail independently: the fleet can be
	// unreachable while every tunnel keeps working, and without this that
	// situation is indistinguishable from "nobody else is online".
	Rendezvous rendezvousStatus `json:"rendezvous"`

	// Reflexive is what peers report observing our source address as. Several
	// distinct values means endpoint-dependent (symmetric) NAT, which is the
	// case where punching fails and a relay is required.
	Reflexive []string `json:"reflexive,omitempty"`

	// Relay is what this node has done as a relay, present only when it is one.
	Relay *relayStatus `json:"relay_stats,omitempty"`

	// Announced is where this node tells peers it can be reached. An empty
	// list means nobody can dial it, which is invisible from any other output.
	//
	// A pointer so that "this daemon does not report it" and "this node
	// announces nothing" stay different answers. They are opposite diagnoses —
	// one is an old daemon, the other is a node no peer can reach — and a
	// reader that conflates them sends somebody looking for a fault that is not
	// there. An older daemon omits the field entirely.
	Announced *[]string `json:"announced,omitempty"`

	// Services are the local ports this device publishes on the mesh. Only
	// this device's own: services are not announced, so no node knows what any
	// other one publishes.
	Services []serviceStatus `json:"services,omitempty"`

	// NameRouter is the shared-port router that lets a service be reached
	// without a port at all. Reported separately because it can fail on its
	// own — port 80 needs a capability the services' own ports do not.
	NameRouter []routerStatus `json:"name_router,omitempty"`
}

// dnsStatus is the state of name resolution on this device.
type dnsStatus struct {
	// Serving means this daemon answers queries on the mesh address.
	Serving bool `json:"serving"`
	// Registered means the host's resolver was told to send the suffix here,
	// which is what makes `ssh laptop.mesh` work rather than only `dig @…`.
	Registered bool   `json:"registered"`
	Suffix     string `json:"suffix,omitempty"`
	Address    string `json:"address,omitempty"`
	Err        string `json:"err,omitempty"`
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

	// Services are the names this peer says it publishes (ADR-023), empty
	// unless that peer has been told to announce them. A claim about what it
	// offers, not a report that anything is listening.
	Services []string `json:"services,omitempty"`

	// Bound is what this peer says is listening on its own mesh address, as
	// "name:port" (ADR-026). Reached as <device>.mesh:<port> — no forwarder
	// and no name of its own, which is why it is not in Services.
	Bound []string `json:"bound,omitempty"`

	// Expires is when this peer's credential runs out, learned from what it
	// announces. Zero means this node has not seen one — which is not the same
	// as expired, and a viewer that renders the two alike sends somebody
	// chasing a renewal nobody needs.
	Expires int64 `json:"expires,omitempty"`

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

	// Created unreachable, then opened to the group.
	//
	// Bind-then-chmod leaves a window in which the socket exists at whatever
	// the umask allows. Under the usual 022 that is 0755 — no write bit for
	// others, and connecting to a unix socket needs write, so the window was
	// closed by luck rather than by design. A permissive umask, which a service
	// manager or a container entrypoint is entitled to set, opens it.
	//
	// 0177 masks everything but owner read/write, so the socket is never more
	// permissive than 0600 before the Chmod below widens it deliberately.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
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
			// Name the likely cause. Running as root is not enough: a systemd
			// unit with a CapabilityBoundingSet that omits CAP_CHOWN makes
			// this fail with EPERM for uid 0, and the message alone sends
			// people to look at file modes instead.
			hint := ""
			if errors.Is(err, os.ErrPermission) {
				hint = "the unit's CapabilityBoundingSet and AmbientCapabilities must include CAP_CHOWN"
			}
			log.Warn("could not set the group on the control socket; status will need root",
				"err", err, "hint", hint)
		} else {
			// The directory too, or the group setting does nothing.
			//
			// systemd creates RuntimeDirectory as root:root 0750, so a socket
			// inside it is unreachable to the group however it is chowned —
			// the group cannot traverse the directory to get to it. That made
			// socket_group appear to work (the log line said so, the socket
			// had the right group) while every `shrooms status` still failed
			// with permission denied, and the error named the directory rather
			// than the setting.
			dir := filepath.Dir(path)
			if err := os.Chown(dir, -1, gid); err != nil {
				log.Warn("could not set the group on the socket directory",
					"dir", dir, "err", err)
			} else if err := os.Chmod(dir, 0o750); err != nil {
				log.Warn("could not set the mode on the socket directory",
					"dir", dir, "err", err)
			}
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
// rendezvousGrace is how long a fresh daemon is left alone before the watchdog
// may act. Long enough to reach the fleet, subscribe, and hear something on an
// unhurried connection.
const rendezvousGrace = 6 * time.Minute

// rendezvousStall is how long every mesh must be unreachable on the rendezvous
// plane before the daemon gives up on the connection it has.
//
// Comfortably more than mesh.RendezvousStale, which is when one mesh calls its
// own plane stale: this is not "a message is late", it is "this connection is
// not coming back".
const rendezvousStall = 10 * time.Minute

// deafConfirm is how long the deaf condition must persist before acting on it.
//
// Short, because mesh.DeafAfter has already waited twelve minutes for an
// announce that never came. This only insists the condition survive a second
// look, so a peer that shuts down in the same minute its tunnel last rekeyed
// does not cost a restart.
const deafConfirm = 2 * time.Minute

// regraftEvery bounds how often the cheap repair is attempted.
//
// Two of them inside deafConfirm, so a restart only ever happens after
// rejoining the gossip mesh has been tried and has not brought the announces
// back.
const regraftEvery = 45 * time.Second

// silentCooldown is how long to leave a silent plane alone after acting on it.
// Long, because a small mesh whose every peer is genuinely off looks exactly
// like this and must not be restarted in a loop.
const silentCooldown = time.Hour

// watchRendezvous restarts the daemon when the rendezvous plane stays down.
//
// When it stops, nothing looks broken. Established tunnels keep carrying
// traffic, the roster stops changing, and every peer quietly ages out until the
// status page is a list of devices marked offline that are all perfectly fine.
// What actually breaks is everything that depends on discovery: a peer that
// moves is never found again, and — the one that is genuinely confusing — a
// relay stops working, because relaying needs both ends to have chosen and
// registered with the same relay, and a node whose roster says everyone is
// offline chooses none. A phone on mobile data then reaches the relay itself
// and nothing behind it.
//
// Exiting is the fix rather than reconnecting in place because the library
// keeps process-global state and has never survived being restarted inside a
// running process. The unit file says Restart=always, so this is a five-second
// gap and a clean node; run by hand, it stops visibly, which is also better
// than pretending.
func watchRendezvous(ctx context.Context, log *slog.Logger, instances []*instance, stateDir string, errs chan<- error) {
	started := time.Now()
	// Restart history, which has to be read from disk because the last one
	// exited this process. See restartlog.go.
	restarts := loadRestartLog(stateDir)
	healthy := started
	// When we first noticed a peer we can reach but cannot hear. Zero when
	// there is no such peer.
	var deafSince time.Time
	// The same, for a plane carrying nothing of ours, and when we last acted
	// on it.
	var silentSince, lastSilentAct time.Time
	// When the cheap repair was last tried, so it is not attempted on every
	// tick while the expensive one waits for its threshold.
	var lastRegraft time.Time
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Two ways to be cut off, and they look nothing alike.
			//
			// Health counts traffic on the shard, so it catches the connection
			// being gone. It cannot catch the connection being half there: a
			// node can go deaf to some publishers and not others — one
			// gossipsub mesh degrading while another holds — and then messages
			// keep arriving while particular peers fall silent one at a time.
			// Mesh.Deaf catches that, because a peer whose tunnel is still
			// rekeying is a peer whose daemon is running, and a running daemon
			// announces.
			deafTo := ""
			silent := false
			for _, in := range instances {
				h := in.mesh.Health()
				if h.OK(now) {
					healthy = now
				}
				// A plane that is up and busy and carrying nothing of ours.
				// OK cannot see it — it counts other applications' traffic on
				// the shard, deliberately — and Deaf cannot either once the
				// tunnels have gone with the roster.
				if h.Silent(now) {
					silent = true
				}
				// Half the peers we can reach, and never on the strength of a
				// single one. One peer going quiet while its tunnel lives is
				// most likely that peer's own rendezvous connection failing,
				// which is its business; half of the few we can reach at once
				// is the shape of this node being the deaf one.
				deaf, reachable := in.mesh.Deaf(now)
				if len(deaf) >= 2 && len(deaf)*2 >= reachable && deafTo == "" {
					deafTo = strings.Join(deaf, ", ")
				}
			}
			switch {
			case deafTo == "":
				deafSince = time.Time{}
			case deafSince.IsZero():
				deafSince = now
			}
			switch {
			case !silent:
				silentSince = time.Time{}
			case silentSince.IsZero():
				silentSince = now
			}

			// A plane that has been healthy since this process started, for
			// longer than the fault would have taken to reappear, means
			// whatever was wrong is over. Forget the restart history, or a
			// node that had a bad hour last week starts its next real outage
			// already penalised.
			//
			// Keyed on our own uptime rather than on the clock: a restart
			// loop cannot reach this, because a looping node never stays up
			// this long.
			if now.Sub(healthy) < rendezvousStall && now.Sub(started) >= restartForgetAfter {
				restarts.clear()
			}

			if now.Sub(started) < rendezvousGrace {
				continue
			}
			// The cheap rung first. A node that has gone deaf is still
			// connected and still receiving other applications' traffic, so
			// the connection is not what is broken — the gossip mesh
			// membership for our topics is — and rejoining it costs two
			// library calls and no downtime at all. Restarting the process
			// works and is a sledgehammer; it should be what happens when this
			// has already been tried and did not take.
			if (deafTo != "" || silent) && now.Sub(lastRegraft) >= regraftEvery {
				lastRegraft = now
				for _, in := range instances {
					if err := in.mesh.Regraft(now); err != nil {
						log.Warn("could not rejoin the gossip mesh",
							"mesh", in.label, "err", err)
					}
				}
				// Give it a chance before deciding it failed: the timers below
				// keep running, so a regraft that works simply means they
				// never reach their thresholds.
				continue
			}

			var problem string
			switch {
			case now.Sub(healthy) >= rendezvousStall:
				problem = fmt.Sprintf("no rendezvous connection for %s (%s)",
					now.Sub(healthy).Round(time.Second),
					instances[0].mesh.Health().Problem(now))
			case !silentSince.IsZero() && now.Sub(silentSince) >= deafConfirm &&
				now.Sub(lastSilentAct) >= silentCooldown:
				// An hour between attempts, because this is the one signal that
				// can be produced by an entirely healthy node: every peer of a
				// small mesh really can be switched off for a quarter of an
				// hour. Restarting such a node costs five seconds and finds
				// nothing, which is a fine price hourly and a silly one every
				// twelve minutes.
				lastSilentAct = now
				problem = "the rendezvous plane is carrying other applications' traffic and none of ours"
			case !deafSince.IsZero() && now.Sub(deafSince) >= deafConfirm:
				// Traffic is arriving and theirs is not. Restarting is a blunt
				// answer, and it is the one that has worked every time: the
				// subscription is intact as far as this process can tell, so
				// there is nothing here to retry.
				problem = fmt.Sprintf("deaf to %s, whose tunnels are live", deafTo)
			default:
				continue
			}

			// Exiting is only a fix if something starts us again. Under
			// systemd or as a container's main process it is a five-second
			// gap; from a terminal it is a mesh that stays down until somebody
			// notices, which is worse than the fault being repaired. So say
			// what is wrong, keep running, and let the person decide.
			if !restartable() {
				log.Error("the rendezvous plane needs a restart and nothing would restart me",
					"problem", problem,
					"fix", "restart this daemon; under systemd or in a container it would have restarted itself")
				healthy = now
				deafSince = time.Time{}
				continue
			}
			// Backed off, because the last restart may not have worked and
			// repeating it costs every tunnel on every mesh. See restartlog.go.
			if ok, left := restarts.ready(now, rendezvousStall); !ok {
				log.Warn("the rendezvous plane needs a restart, but the last one did not help",
					"problem", problem,
					"history", restarts.String(),
					"retry_in", left.Round(time.Second),
					"note", "established tunnels keep working while this waits")
				continue
			}
			restarts.note(now)
			log.Error("restarting to rebuild the rendezvous connection",
				"problem", problem, "history", restarts.String())
			errs <- errors.New(problem)
			return
		}
	}
}

// restartable reports whether something outside this process will start it
// again if it exits.
//
// systemd sets INVOCATION_ID for every service it runs, and NOTIFY_SOCKET when
// the unit asks to be notified; a container's main process is pid 1, and
// whatever started the container decides what happens when it ends. A daemon
// run by hand in a terminal has neither, and exiting there just ends the mesh.
func restartable() bool {
	return os.Getenv("INVOCATION_ID") != "" ||
		os.Getenv("NOTIFY_SOCKET") != "" ||
		os.Getpid() == 1
}

// runtimeBits is what the control socket needs that is not a mesh: the log
// tail it serves, the channel it ends the process through, and where name
// resolution got to. One struct rather than three more parameters, because
// serveControl already takes everything a daemon has.
type runtimeBits struct {
	tail    *logtail.Ring
	restart chan<- error
	dns     *atomic.Pointer[dnsStatus]

	// What an invite is redeemed through, and the state it is redeemed into.
	// The daemon's own rendezvous connection: starting a second node for two
	// messages is what `shrooms invite` used to do and what the endpoints
	// exist to stop.
	invites invite.Transport
	st      *state.State
	cfgPath string
}

func serveControl(ctx context.Context, log *slog.Logger, path string, instances []*instance,
	cfg state.Config, rl *reloader, rt *runtimeBits) (*http.Server, error) {
	var tail *logtail.Ring
	var restart chan<- error
	if rt != nil {
		tail, restart = rt.tail, rt.restart
	}
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
			Version: version,
		}
		if rt != nil {
			if d := rt.dns.Load(); d != nil {
				out.DNS = *d
			}
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
			if in.relay {
				ms.Relays++
			}
			for _, p := range in.mesh.Roster().Current(now) {
				if p.Relay {
					ms.Relays++
				}
			}
			_, ms.BlindRelays = in.mesh.ConfiguredRelays()
			if at, blind, ok := in.mesh.RelayInUse(); ok {
				ms.RelayUsing, ms.RelayUsingBlind = at.String(), blind
			}
			if e := in.mesh.SelfExpiry(); !e.IsZero() {
				ms.Expires = e.Unix()
			}
			ms.Unenrolled = in.mesh.Unenrolled()
			if a, ok := in.mesh.LookupV4(in.self); ok {
				ms.OverlayV4 = a.String()
			}
			out.Meshes = append(out.Meshes, ms)
		}

		// Everything the config says, which is a different question from what
		// this process is doing and the one a settings form has to answer:
		// these controls write the config, so the config is what a click
		// changed and what a restart will apply.
		//
		// Merged by label rather than appended, because the two lists overlap
		// in both directions and every unmerged combination is a visible bug.
		// A mesh switched off is in the config and still running, and appending
		// gave it two rows; a mesh switched on is in the config with nothing
		// running, and appending only the disabled ones gave it none — which
		// emptied the section that could have switched it off again.
		//
		// Read from disk on every snapshot rather than from the copy loaded at
		// startup: the startup copy reports a mesh as running after it was
		// switched off, and a mode as unchanged after it was changed.
		out.ModeRunning = cfg.Mode
		if rl != nil {
			if onDisk, err := state.LoadConfigUnvalidated(rl.cfgPath); err == nil {
				out.Mode = onDisk.Mode
				pm := onDisk.PortMapping
				out.PortMapping = &pm

				at := map[string]int{}
				for i, ms := range out.Meshes {
					at[ms.Label] = i
				}
				for _, mc := range onDisk.Meshes() {
					i, running := at[mc.Label]
					if !running {
						// In the config, not in this process: either just
						// switched on, or switched off and never started.
						entry := meshStatus{Label: mc.Label, NotRunning: true}
						if k, err := mc.Key(); err == nil {
							entry.Prefix = k.Prefix().String()
						}
						out.Meshes = append(out.Meshes, entry)
						i = len(out.Meshes) - 1
					}
					out.Meshes[i].Disabled = mc.Disabled
					out.Meshes[i].Relay = mc.Relay
					out.Meshes[i].AnnounceServices = mc.AnnounceServices
					out.Meshes[i].AnnounceBound = mc.AnnounceBound
					delete(at, mc.Label)
				}
				// What each running mesh has bound to its own address, which
				// only an instance can answer — the config knows what is
				// announced, not what is listening.
				for _, in := range instances {
					for i := range out.Meshes {
						if out.Meshes[i].Label == in.label {
							out.Meshes[i].BoundHere = in.mesh.BoundHere()
						}
					}
				}

				// Whatever is left is running and no longer in the config,
				// which is a mesh that has been left and not yet restarted out
				// of. It is still carrying traffic, so it is still listed.
				for _, i := range at {
					out.Meshes[i].Left = true
				}
			}
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
			svc, bnd := m.Services(now), m.Bound(now)
			for _, p := range m.Roster().Current(now) {
				ps := peerStatus{
					Services:  svc[p.ID()],
					Bound:     bnd[p.ID()],
					Mesh:      meshLabel,
					Name:      p.Name,
					Overlay:   p.Overlay.String(),
					OverlayV4: v4Of(m, p.Overlay),
					Endpoints: p.Endpoints,
					Seq:       p.Seq,
					LastSeen:  p.LastSeen.Format(time.RFC3339),
					Online:    p.Online(now),
					Relay:     p.Relay,
					// Qualified when this node has more than one mesh, because
					// the short form is answered by the first one — so an
					// unqualified name for a peer on any other mesh points at
					// an address on a network it is not on.
					DNSName: mesh.QualifiedDNSName(p.Name, meshLabel, cfg.HostsSuffix),
				}
				if best, ok := m.BestPath(p.ID(), now); ok {
					ps.RTTMs = best.RTT.Milliseconds()
				}
				if e := m.PeerExpiry(p.ID()); !e.IsZero() {
					ps.Expires = e.Unix()
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
		if rs, ok := m.RelayStats(); ok {
			out.Relay = &relayStatus{
				Peers: rs.Peers, Registered: rs.Registered,
				Forwarded: rs.Forwarded, Dropped: rs.Dropped, Refused: rs.Refused,
			}
		}
		announced := m.Announced()
		if announced == nil {
			announced = []string{}
		}
		out.Announced = &announced
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

	status := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshot())
	}
	mux.HandleFunc("/status", status)

	// A second, read-only mux for the loopback port (ADR-025).
	//
	// The port used to be handed the same mux as the unix socket, which put
	// every mutating endpoint on it: /config/*, /join, /leave, /reload,
	// /restart. The socket decides who may call those by file permissions and
	// SO_PEERCRED, and neither exists over TCP — no ConnContext is set, so
	// peer-cred is unavailable and only the root-gated handlers failed closed.
	// The group-tier ones were not gated at all.
	//
	// That is reachable by any local user on a shared machine, and by a
	// browser: it is plain HTTP with no Origin check, and a text/plain POST
	// does not trigger a preflight, so a page in a tab could rewrite the
	// config, leave a mesh or restart the daemon. It is off by default, which
	// is the only reason this was not worse.
	//
	// So the port gets what the config says it is for and nothing else — the
	// status JSON. Serving a separate mux rather than checking the verb per
	// handler, because a split is a property somebody can see, while a check
	// is one that has to be remembered by whoever adds the next endpoint.

	// The log tail and the restart button, both of which exist because a UI
	// cannot reach the journal or a terminal (ADR-025).
	runtimeHandlers(mux, log, rl.cfgPath, tail, restart)

	// Changing this device's own settings, and applying them (ADR-025). In the
	// socket group's tier, because none of it decides who belongs to a mesh —
	// that needs the admin key, which the daemon has never held.
	if rl != nil {
		// Re-reading the config is in the group's tier: it applies what a
		// desktop app has just written, and it can do nothing the config does
		// not already say.
		mux.HandleFunc("/reload", (func(w http.ResponseWriter, r *http.Request) {
			msg, err := rl.Reload(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			log.Info(msg)
			fmt.Fprintln(w, msg)
		}))

		// Settings a desktop app may change, and joining or leaving a mesh
		// (ADR-025).
		controlHandlers(mux, log, rl.cfgPath, rl)
		mux.HandleFunc("/leave", leaveHandler(log, rl.cfgPath))
		if rt != nil && rt.st != nil {
			mux.HandleFunc("/join", joinHandler(log, rt.invites, rl.cfgPath, rt.st))
		}
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

	return listenControl(ctx, log, path, mux, cfg)
}
