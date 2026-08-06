// Package mesh wires Waku discovery to the WireGuard data plane.
//
// Waku is a rendezvous and repair channel, not a live control plane: once
// tunnels exist they sustain themselves, because WireGuard relearns a peer's
// endpoint from any correctly-authenticated packet. The announce loop exists to
// bootstrap, to repair after a move, and to carry membership changes.
package mesh

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/vpavlin/logos-vpn/internal/control"
	"github.com/vpavlin/logos-vpn/internal/disco"
	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/relay"
	"github.com/vpavlin/logos-vpn/internal/state"
	"github.com/vpavlin/logos-vpn/internal/topic"
	"github.com/vpavlin/logos-vpn/internal/waku"
	"github.com/vpavlin/logos-vpn/internal/wg"
)

// AnnounceInterval is the heartbeat period. Fixed-rate, fixed-size announces
// mean "came online" and "changed IP" look identical to steady state on the bus.
const AnnounceInterval = 45 * time.Second

// Keepalive is the WireGuard persistent keepalive, in seconds.
//
// DESIGN §10: 74% of CGNATs expire idle UDP state within 60s and the
// non-cellular CGN median is 35s, so this must stay in 15-25 and never above 25.
const Keepalive = 25

// Mesh is the running node.
type Mesh struct {
	log    *slog.Logger
	cfg    state.Config
	st     *state.State
	nk     identity.NetworkKey
	node   *waku.Node
	dev    *wg.Device
	roster *Roster
	guard  *control.ReplayGuard

	self netip.Addr

	discoKey disco.Key
	prober   *disco.Prober

	relayKey relay.Key
	// relaySrv is non-nil only when this node acts as a relay for others.
	relaySrv *relay.Server
	// relayAddr is the relay we use when no direct path exists.
	relayAddr netip.AddrPort

	mu         sync.Mutex
	subscribed map[string]bool // content topics we hold a subscription for
}

// New assembles a mesh node. The Waku node and WireGuard device are owned by
// the caller and must outlive the Mesh.
func New(log *slog.Logger, cfg state.Config, st *state.State, node *waku.Node, dev *wg.Device) (*Mesh, error) {
	nk, err := cfg.Key()
	if err != nil {
		return nil, err
	}
	m := &Mesh{
		log:        log,
		cfg:        cfg,
		st:         st,
		nk:         nk,
		node:       node,
		dev:        dev,
		roster:     NewRoster(nk, st.Identity.DevicePub),
		guard:      control.NewReplayGuard(),
		self:       identity.OverlayAddr(nk, st.Identity.DevicePub),
		discoKey:   disco.DeriveKey(nk),
		relayKey:   relay.DeriveKey(nk),
		subscribed: make(map[string]bool),
	}
	if cfg.Relay {
		m.relaySrv = relay.NewServer(m.relayKey)
		log.Info("acting as a relay for this mesh")
	}
	if cfg.RelayAddr != "" {
		if ap, err := netip.ParseAddrPort(cfg.RelayAddr); err == nil {
			m.relayAddr = ap
			log.Info("relay configured", "addr", ap)
		} else {
			log.Warn("ignoring unparseable relay_addr", "value", cfg.RelayAddr, "err", err)
		}
	}
	m.prober = disco.NewProber(m.discoKey, st.Identity.DevicePub, m.sendDisco)

	// Control packets share the WireGuard socket; this is what makes NAT
	// traversal possible at all.
	dev.Bind.SetControlHandler(m.handleControl)

	return m, nil
}

// Paths returns every probed path for a peer, for diagnostics.
func (m *Mesh) Paths(peerID string) []disco.Path { return m.prober.Paths(peerID) }

// BestPath returns the selected path for a peer, if any.
func (m *Mesh) BestPath(peerID string, now time.Time) (disco.Path, bool) {
	return m.prober.Best(peerID, now)
}

// Reflexive returns the self-addresses peers have reported observing.
func (m *Mesh) Reflexive() []netip.AddrPort { return m.prober.Reflexive(time.Now()) }

// PeerStats exposes the data-plane view of peers.
func (m *Mesh) PeerStats() (map[string]wg.PeerStat, error) { return m.dev.PeerStats() }

// Roster exposes the peer roster.
func (m *Mesh) Roster() *Roster { return m.roster }

// Self returns this node's overlay address.
func (m *Mesh) Self() netip.Addr { return m.self }

// Run drives the mesh until ctx is cancelled.
func (m *Mesh) Run(ctx context.Context) error {
	go m.consume(ctx)

	if err := m.resubscribe(time.Now()); err != nil {
		return fmt.Errorf("initial subscribe: %w", err)
	}
	if err := m.announce(time.Now()); err != nil {
		m.log.Warn("initial announce failed", "err", err)
	}

	ticker := time.NewTicker(AnnounceInterval)
	defer ticker.Stop()

	// Probing runs far more often than announcing: finding a path is urgent,
	// advertising is not.
	probeTicker := time.NewTicker(disco.ProbeInterval)
	defer probeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-probeTicker.C:
			m.probeAll(now)
			m.registerWithRelay()
		case now := <-ticker.C:
			// Resubscribe first: near an epoch boundary the next topic must be
			// live before we publish to it.
			if err := m.resubscribe(now); err != nil {
				m.log.Warn("resubscribe failed", "err", err)
			}
			if err := m.announce(now); err != nil {
				m.log.Warn("announce failed", "err", err)
			}
		}
	}
}

// resubscribe ensures we hold subscriptions across the epoch window.
//
// All window topics share one shard (verified by spike S3), so this changes no
// gossipsub subscription and causes no mesh churn.
func (m *Mesh) resubscribe(now time.Time) error {
	want := topic.Window(m.nk, now)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ct := range want {
		if m.subscribed[ct] {
			continue
		}
		if err := m.node.Subscribe(ct); err != nil {
			return fmt.Errorf("subscribe %s: %w", ct, err)
		}
		m.subscribed[ct] = true
		m.log.Debug("subscribed", "topic", ct)
	}

	// Drop topics that have fallen out of the window.
	keep := map[string]bool{}
	for _, ct := range want {
		keep[ct] = true
	}
	for ct := range m.subscribed {
		if keep[ct] {
			continue
		}
		if err := m.node.Unsubscribe(ct); err != nil {
			m.log.Warn("unsubscribe failed", "topic", ct, "err", err)
		}
		delete(m.subscribed, ct)
	}
	return nil
}

// announce publishes our endpoint candidates for the current epoch.
func (m *Mesh) announce(now time.Time) error {
	seq, err := m.st.NextSeq()
	if err != nil {
		return fmt.Errorf("advance sequence: %w", err)
	}

	a := &control.Announce{
		Kind:      control.KindAnnounce,
		DevicePub: m.st.Identity.DevicePub,
		WGPub:     m.st.Identity.WGPub[:],
		Name:      m.cfg.Name,
		Endpoints: m.candidates(),
		Seq:       seq,
		Timestamp: now.Unix(),
	}

	raw, err := control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, a)
	if err != nil {
		return fmt.Errorf("seal announce: %w", err)
	}

	// Ephemeral: endpoint announces are worthless once stale, and keeping them
	// out of Store avoids handing an observer an archive.
	if _, err := m.node.Send(topic.Current(m.nk, now), raw, true); err != nil {
		return fmt.Errorf("publish announce: %w", err)
	}
	m.log.Debug("announced", "seq", seq, "endpoints", a.Endpoints)
	return nil
}

// localAddrs lists routable local addresses, skipping loopback, link-local and
// our own overlay range.
func localAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			pfx, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(pfx.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
				continue
			}
			// Skip our own overlay addresses — announcing them is circular.
			if ip.Is6() && ip.As16()[0] == 0xfd {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}

// consume processes inbound Waku events.
func (m *Mesh) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-m.node.Events():
			if !ok {
				return
			}
			m.handle(ev)
		}
	}
}

func (m *Mesh) handle(ev waku.Event) {
	msg, _, ok := waku.ParseMessage(ev.JSON)
	if !ok {
		return
	}
	now := time.Now()

	// Try every epoch in the window: a peer whose clock differs will have
	// sealed under a neighbouring key.
	e := topic.Epoch(now)
	a, err := control.OpenAnnounceWindow(m.nk, []int64{e - 1, e, e + 1}, msg.Payload, now)
	if err != nil {
		// Expected: the shard carries other applications' traffic, and we
		// cannot decrypt any of it.
		m.log.Debug("ignoring undecryptable message", "topic", msg.ContentTopic)
		return
	}

	if !m.guard.Accept(a) {
		m.log.Warn("rejected replayed or stale announce",
			"peer", hex.EncodeToString(a.DevicePub)[:16], "seq", a.Seq)
		return
	}

	peer, changed := m.roster.Apply(a, now)
	if peer.DevicePub == nil {
		return // our own announce
	}
	if !changed {
		return
	}

	m.log.Info("peer updated", "name", peer.Name, "overlay", peer.Overlay,
		"endpoints", peer.Endpoints, "seq", peer.Seq)

	// Probe straight away. This is also the punch: the outbound probes open our
	// NAT mapping so the peer's own probes can get back in, and both sides do
	// this on receiving each other's announce.
	m.prober.Probe(peer.ID(), parseCandidates(peer.Endpoints), now)

	if err := m.syncPeers(); err != nil {
		m.log.Error("failed to reconfigure data plane", "err", err)
	}
}

func hasBest(m *Mesh, id string, now time.Time) bool {
	_, ok := m.prober.Best(id, now)
	return ok
}

// syncPeers pushes the roster into WireGuard.
func (m *Mesh) syncPeers() error {
	peers := m.roster.Peers()
	out := make([]wg.Peer, 0, len(peers))

	now := time.Now()
	for _, p := range peers {
		// Prefer a path that has actually answered a probe. Fall back to the
		// first announced candidate so a directly-reachable peer still works
		// before probing completes — that is the M1 behaviour.
		peer := wg.Peer{
			WGPub:     p.WGPub,
			AllowedIP: p.Overlay,
			PSK:       identity.PairPSK(m.nk, m.st.Identity.WGPub, p.WGPub),
			Keepalive: Keepalive,
		}

		switch {
		case hasBest(m, p.ID(), now):
			// A probed path: packets have demonstrably reached the peer here.
			best, _ := m.prober.Best(p.ID(), now)
			peer.Endpoint = best.Addr.String()
		case m.relayAddr.IsValid() && m.relaySrv == nil:
			// No direct path. Route through the relay rather than leaving the
			// peer unreachable — failing over is just an endpoint swap, with no
			// tunnel teardown or rehandshake.
			peer.RelayVia = wg.NewRelayEndpoint(m.relayKey, m.relayAddr, m.st.Identity.WGPub, p.WGPub)
		case len(p.Endpoints) > 0:
			// Nothing probed and no relay: try what was announced. This is the
			// M1 behaviour and works whenever one side is directly reachable.
			peer.Endpoint = p.Endpoints[0]
		}
		out = append(out, peer)
	}
	return m.dev.SetPeers(out)
}
