// Package mesh wires Waku discovery to the WireGuard data plane.
//
// Waku is a rendezvous and repair channel, not a live control plane: once
// tunnels exist they sustain themselves, because WireGuard relearns a peer's
// endpoint from any correctly-authenticated packet. The announce loop exists to
// bootstrap, to repair after a move, and to carry membership changes.
package mesh

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/hosts"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/topic"
	"github.com/vpavlin/shrooms/internal/waku"
	"github.com/vpavlin/shrooms/internal/wg"
)

// AnnounceInterval is the heartbeat period. Fixed-rate, fixed-size announces
// mean "came online" and "changed IP" look identical to steady state on the bus.
const AnnounceInterval = 45 * time.Second

// AnnounceDebounce is the minimum gap between triggered announces, as opposed
// to the periodic ones.
//
// A node that has just started announces immediately, so others find it fast.
// Discovering others is the slow half: it must wait out each of their periodic
// announces. Measured on real infrastructure, that asymmetry was 5.4s versus
// 19.3s for the same pair — and the 19.3s side is the one that cannot fix it
// alone, because it has nothing to wait for and no way to ask.
//
// So a node that hears an announce from a peer it has no tunnel to answers with
// one of its own. The restarted node then learns about it in a round trip
// rather than an announce interval.
//
// This cannot run away. The reply is conditional on there being no working
// tunnel, so it stops the moment one exists; a per-peer cooldown of
// AnnounceInterval bounds the pathological case of a peer that announces
// forever without ever connecting.
const AnnounceDebounce = 5 * time.Second

// FreshWindow is how long after starting a node marks its announces Fresh,
// asking peers to introduce themselves. Comfortably longer than the time to
// hear the replies, and far shorter than an announce interval.
const FreshWindow = 90 * time.Second

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
	// relayPin is a hand-configured relay_addr. Normally empty: relays are
	// discovered from the roster instead. Kept as an escape hatch for a mesh
	// whose relay has not announced yet, and for the container tests.
	relayPin netip.AddrPort
	// relayNow is the relay currently in use, for logging changes.
	relayNow netip.AddrPort

	// resync is poked when the data plane needs reconfiguring. The control
	// handler runs on the packet receive path and must not block, so it signals
	// here instead of doing the work inline — IpcSet takes device locks and
	// refreshHosts touches the filesystem, neither of which belongs on the
	// datapath.
	resync chan struct{}

	// health tracks the rendezvous plane, which fails independently of the
	// data plane and otherwise gives no visible signal at all.
	health *health

	// unknownSeen is the last reported count of unroutable inbound packets.
	unknownSeen uint64

	// timing records how long each peer took to reach each stage.
	timing *timings

	// rates turns WireGuard's cumulative byte counters into throughput.
	rates *rates

	// reannounce is poked when a newly-discovered peer should be told we
	// exist. Like resync, it is signalled from the receive path and acted on
	// in the main loop, because publishing is a network call.
	reannounce   chan struct{}
	lastAnnounce time.Time

	// authority is the set of admin keys this mesh trusts, or nil when
	// membership is still the network key alone. Both worlds run at once
	// during migration (ADR-018): nil behaves exactly as before.
	authority *cred.Authority

	// lastRendezvousOK is the previous health verdict, so a recovery can be
	// noticed. See repairRendezvous.
	lastRendezvousOK bool

	// relayRegistered is when we last renewed our mapping with the relay.
	// See RelayRefresh: this used to be re-sent on every probe tick.
	relayRegistered time.Time

	// repliedTo bounds how often a single peer can draw a triggered announce.
	replyMu   sync.Mutex
	repliedTo map[string]time.Time

	mu         sync.Mutex
	subscribed map[string]bool // content topics we hold a subscription for
}

// New assembles a mesh node. The Waku node and WireGuard device are owned by
// the caller and must outlive the Mesh.
func New(log *slog.Logger, cfg state.Config, st *state.State, node *waku.Node, dev *wg.Device) (*Mesh, error) {
	auth, err := cfg.Authority()
	if err != nil {
		return nil, err
	}
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
		authority:  auth,
		guard:      control.NewReplayGuard(),
		self:       identity.OverlayAddr(nk, st.Identity.DevicePub),
		discoKey:   disco.DeriveKey(nk),
		relayKey:   relay.DeriveKey(nk),
		resync:     make(chan struct{}, 1),
		reannounce: make(chan struct{}, 1),
		repliedTo:  make(map[string]time.Time),
		health:     newHealth(),
		timing:     newTimings(time.Now()),
		rates:      newRates(),
		subscribed: make(map[string]bool),
	}
	if cfg.Relay {
		m.relaySrv = relay.NewServer(m.relayKey)
		log.Info("acting as a relay for this mesh")
	}
	if cfg.RelayAddr != "" {
		if ap, err := netip.ParseAddrPort(cfg.RelayAddr); err == nil {
			m.relayPin = ap
			log.Info("relay pinned by config", "addr", ap)
		} else {
			log.Warn("ignoring unparseable relay_addr", "value", cfg.RelayAddr, "err", err)
		}
	}
	m.prober = disco.NewProber(m.discoKey, st.Identity.DevicePub, m.sendDisco)

	// Control packets share the WireGuard socket; this is what makes NAT
	// traversal possible at all.
	dev.Bind.SetControlHandler(m.handleControl)

	// ParseEndpoint needs these to rebuild relay endpoints when WireGuard hands
	// back an endpoint string over the UAPI.
	dev.Bind.SetRelayIdentity(m.relayKey, st.Identity.WGPub)

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

// requestResync asks the main loop to reconfigure the data plane.
//
// Non-blocking and coalescing: a full channel already means a resync is pending,
// and one resync reflects the current roster however many times it was
// requested.
func (m *Mesh) requestResync() {
	select {
	case m.resync <- struct{}{}:
	default:
	}
}

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
		case <-m.resync:
			if err := m.syncPeers(); err != nil {
				m.log.Error("failed to reconfigure data plane", "err", err)
			}
		case <-m.reannounce:
			// Skip if we announced recently: the new peer will already have
			// that one, and this exists to shorten discovery, not to add
			// traffic to a shared bus.
			now := time.Now()
			if now.Sub(m.lastAnnounce) < AnnounceDebounce {
				continue
			}
			if err := m.announce(now); err != nil {
				m.log.Warn("announce for new peer failed", "err", err)
			}
		case now := <-probeTicker.C:
			m.probeAll(now)
			m.registerWithRelay()
			m.reportUnknown()
			m.checkTunnels(now)
			m.sampleRates(now)
			m.pruneForgotten(now)
		case now := <-ticker.C:
			// A rendezvous node that dropped and came back has lost its
			// subscriptions; ours is the only side that knows they are gone.
			m.repairRendezvous(now)

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

// repairRendezvous re-establishes subscriptions after the rendezvous node has
// been away.
//
// Subscriptions live in the delivery library, and m.subscribed is only our
// belief about them. When that node disconnects and reconnects — a phone
// changing network, a fleet peer dropping us — the real subscriptions are gone
// while our map still says they exist, so resubscribe skips every topic and
// nothing is ever received again. The mesh then decays silently: no announces
// arrive, peers age out, and the only recovery is restarting the process. That
// is exactly what it looked like in the field, and it is why this is a repair
// rather than a report.
//
// Two triggers, because either alone misses cases:
//
//   - a transition from unhealthy to healthy, which is the ordinary reconnect;
//   - being nominally healthy while nothing has arrived for RendezvousStale,
//     which is the case where the library never told us it had dropped.
//
// Announcing afterwards is not optional: our peers' rosters have been ageing
// while we were deaf, and Fresh asks them to announce back so both directions
// recover in one round rather than waiting out an interval.
func (m *Mesh) repairRendezvous(now time.Time) {
	h := m.Health()
	why, need := needsRendezvousRepair(h, m.lastRendezvousOK, now)
	m.lastRendezvousOK = h.OK(now)
	if !need {
		return
	}

	m.mu.Lock()
	had := len(m.subscribed)
	m.subscribed = map[string]bool{}
	m.mu.Unlock()

	m.log.Info("re-establishing rendezvous subscriptions",
		"why", why, "topics", had, "status", h.Status)

	if err := m.resubscribe(now); err != nil {
		m.log.Warn("resubscribe after repair failed", "err", err)
		// Left cleared deliberately: a failed resubscribe must be retried on
		// the next tick, and remembering topics we do not hold would prevent
		// exactly that.
		return
	}
	if err := m.announceFresh(now); err != nil {
		m.log.Warn("announce after repair failed", "err", err)
	}
}

// needsRendezvousRepair decides whether subscriptions must be re-established,
// and why.
//
// Separated from the repair itself so the decision can be tested: the action
// reaches into the delivery library, and the part that gets this wrong is the
// trigger, not the Subscribe call.
func needsRendezvousRepair(h Health, lastOK bool, now time.Time) (why string, need bool) {
	ok := h.OK(now)
	switch {
	case h.Status == "Disconnected":
		// Nothing to subscribe into. The recovery edge fires when it returns.
		return "", false

	case ok && !lastOK:
		// The ordinary reconnect.
		return "reconnected", true

	case !ok && !h.LastMessage.IsZero():
		// The library reports a connection and yet nothing has arrived for
		// RendezvousStale. This is the case that stranded the mesh, and the
		// reason it cannot be left to the recovery edge: Health.OK is false
		// precisely BECAUSE nothing is arriving, so waiting for it to become
		// true waits on a message that our missing subscription guarantees will
		// never come. Repairing here is what breaks that circle.
		return "silent for too long", true
	}
	return "", false
}

// checkMembership admits a peer only if it can prove the admin let it in.
//
// Both schemes run at once, which is what makes this deployable. With no
// authority configured the network key alone decides membership, exactly as
// every mesh works today — this returns nil and nothing changes. With an
// authority, a peer must present a credential that authority signed, naming
// the device key that signed the announce.
//
// That last check is the one that matters. A credential is public and travels
// on a public bus, so anyone can copy one; binding it to the announce's signing
// key is what stops a copied credential being replayed by someone else.
func (m *Mesh) checkMembership(a *control.Announce, now time.Time) error {
	if m.authority == nil {
		return nil
	}
	if len(a.Credential) == 0 {
		return errors.New("no credential")
	}
	c, err := cred.UnmarshalCredential(a.Credential)
	if err != nil {
		return fmt.Errorf("unreadable credential: %w", err)
	}
	if err := cred.VerifyBy(m.authority, c, now); err != nil {
		return err
	}
	if !bytes.Equal(c.DevicePub, a.DevicePub) {
		return errors.New("credential names a different device than the one that signed")
	}
	if !bytes.Equal(c.WGPub, a.WGPub) {
		return errors.New("credential names a different tunnel key")
	}
	return nil
}

// reportUnknown logs packets WireGuard rejected as an unknown message type.
//
// wireguard-go logs these itself but without a source, which is what made them
// impossible to attribute when they appeared on both ends of a failing tunnel.
// Logged only when the count changes, so a healthy node stays quiet.
func (m *Mesh) reportUnknown() {
	n, last := m.dev.Bind.Unknown()
	if n == m.unknownSeen {
		return
	}
	m.log.Warn("packets rejected as unknown type",
		"total", n, "new", n-m.unknownSeen, "last_from", last)
	m.unknownSeen = n
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
	m.health.setTopics(len(m.subscribed))
	return nil
}

// shouldReplyTo reports whether a peer's announce warrants one of ours.
//
// Called on the receive path, so it does no work beyond map lookups.
//
// The per-peer cooldown is what bounds the cost: a peer that is announcing but
// unreachable — a genuine outage rather than a restart — would otherwise draw a
// reply every time it spoke, doubling announce traffic on a shared bus for as
// long as it stayed broken.
func (m *Mesh) shouldReplyTo(a *control.Announce, p PeerInfo, now time.Time) bool {
	if !a.Fresh {
		// Not asking. Answer anyway if they have no tunnel to us, which catches
		// a peer stuck for some reason other than having just started.
		//
		// Deliberately NOT the only condition: after a peer restarts, our
		// session with it stays valid for REJECT_AFTER_TIME, so we would
		// consider the tunnel live and stay silent for minutes — through
		// exactly the window the restarted node is waiting in. Only it knows it
		// restarted, which is what Fresh is for.
		stats, err := m.dev.PeerStats()
		if err != nil {
			return false
		}
		if st, ok := stats[p.WGPub.String()]; ok && st.Live(now) {
			return false
		}
	}

	m.replyMu.Lock()
	defer m.replyMu.Unlock()
	if last, ok := m.repliedTo[p.ID()]; ok && now.Sub(last) < AnnounceInterval {
		return false
	}
	m.repliedTo[p.ID()] = now
	return true
}

// requestAnnounce asks the main loop to announce, without blocking the caller.
func (m *Mesh) requestAnnounce() {
	select {
	case m.reannounce <- struct{}{}:
	default:
	}
}

// announce publishes our endpoint candidates for the current epoch.
func (m *Mesh) announce(now time.Time) error {
	// Fresh means "I have just started, please announce back". Within the
	// window after start that is simply true.
	return m.announceWith(now, now.Sub(m.timing.started) < FreshWindow)
}

// announceFresh announces and asks peers to answer, whatever our uptime.
//
// For recovery rather than start-up: after being deaf to the rendezvous plane
// our roster is stale in both directions, and waiting out every peer's announce
// interval to rebuild it is the slow path this flag exists to avoid.
func (m *Mesh) announceFresh(now time.Time) error {
	return m.announceWith(now, true)
}

func (m *Mesh) announceWith(now time.Time, fresh bool) error {
	seq, err := m.st.NextSeq()
	if err != nil {
		return fmt.Errorf("advance sequence: %w", err)
	}

	a := &control.Announce{
		Kind:       control.KindAnnounce,
		DevicePub:  m.st.Identity.DevicePub,
		WGPub:      m.st.Identity.WGPub[:],
		Name:       m.cfg.Name,
		Endpoints:  m.candidates(),
		Seq:        seq,
		Timestamp:  now.Unix(),
		Relay:      m.cfg.Relay,
		Fresh:      fresh,
		Credential: m.st.Credential,
	}

	// Trim until it fits. The announce is padded to a fixed size and Seal
	// refuses anything larger, so a node with enough addresses simply stopped
	// announcing — it logged "announce failed" once per interval and vanished
	// from every roster, which looks like a network fault rather than a node
	// that had too many interfaces. Five was enough: two reflexive addresses
	// and three local ones is an ordinary laptop with docker installed.
	//
	// Dropping from the end is right because candidates() is ordered by
	// usefulness: reflexive first, then configured, then local addresses.
	raw, err := control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, a)
	for err != nil && len(a.Endpoints) > 0 && errors.Is(err, control.ErrTooLarge) {
		a.Endpoints = a.Endpoints[:len(a.Endpoints)-1]
		m.log.Debug("announce too large; dropping the least useful endpoint",
			"remaining", len(a.Endpoints))
		raw, err = control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, a)
	}
	if err != nil {
		return fmt.Errorf("seal announce: %w", err)
	}

	// Ephemeral: endpoint announces are worthless once stale, and keeping them
	// out of Store avoids handing an observer an archive.
	if _, err := m.node.Send(topic.Current(m.nk, now), raw, true); err != nil {
		return fmt.Errorf("publish announce: %w", err)
	}
	m.lastAnnounce = now
	m.log.Debug("announced", "seq", seq, "endpoints", a.Endpoints)
	return nil
}

// pruneForgotten drops every trace of peers that have been gone for
// ForgetAfter.
//
// Each of these maps is keyed by peer id and only ever grew: the roster, the
// replay counters, the probe paths, the connection timings, the throughput
// history and the announce-reply debounce. A peer that never returns stayed in
// all six until the process restarted.
//
// Ordered deliberately: the roster is the authority on who exists, so it
// decides who is gone and everything else follows. Doing it the other way
// leaves state for a peer the roster still lists.
func (m *Mesh) pruneForgotten(now time.Time) {
	gone := m.roster.Prune(now)
	if len(gone) == 0 {
		return
	}
	for _, id := range gone {
		m.guard.Forget(mustHexBytes(id))
		m.prober.Forget(id)
		m.timing.forget(id)
		m.rates.forget(id)
		delete(m.repliedTo, id)
	}
	m.log.Info("forgot peers not seen recently", "count", len(gone), "after", ForgetAfter)

	// The data plane still holds them as WireGuard peers until the next sync.
	m.requestResync()
}

// mustHexBytes converts a peer id back to the device key it encodes. Peer ids
// are produced by hex-encoding that key, so a failure here is impossible
// unless that changes — in which case an empty slice is the safe answer, since
// Forget on an unknown key does nothing.
func mustHexBytes(id string) []byte {
	b, err := hex.DecodeString(id)
	if err != nil {
		return nil
	}
	return b
}

// localAddrs lists routable local addresses, skipping loopback, link-local and
// our own overlay range.
// HasGlobalAddr reports whether any local interface carries a globally
// routable address.
//
// If one does — the normal case on a VPS with a directly attached public IP —
// the node announces it automatically and needs no `advertise` configuration.
// If none does, the node is behind NAT and peers cannot reach it until either
// reflexive discovery kicks in or an address is configured.
func HasGlobalAddr() bool {
	for _, ip := range localAddrs() {
		if ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return true
		}
	}
	return false
}

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
	now := time.Now()
	// Fold every event into health, not just the ones we can decrypt: traffic
	// from other applications on the shard is undecryptable to us but still
	// proves the subscription is live.
	m.health.observe(ev, now)

	msg, _, ok := waku.ParseMessage(ev.JSON)
	if !ok {
		return
	}

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

	m.health.announceOpened(now)

	// Membership, when this mesh has an authority. Before the replay guard so a
	// peer we will not admit cannot consume a sequence number.
	if err := m.checkMembership(a, now); err != nil {
		m.log.Warn("refusing a peer without valid membership",
			"peer", hex.EncodeToString(a.DevicePub)[:16], "err", err)
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
	if m.timing.mark(peer.ID(), func(x *Milestones) *time.Time { return &x.Discovered }, now) {
		m.log.Info("peer discovered", "peer", peer.Name,
			"after", m.Timing(peer.ID()).DiscoveredAfter.Round(time.Millisecond))
	}

	// Introduce ourselves when asked, or when this peer plainly cannot reach us.
	if m.shouldReplyTo(a, peer, now) {
		m.requestAnnounce()
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

	m.requestResync()
}

// bootstrapEndpoint picks the announced candidate most likely to be reachable
// from here, for a peer we have never probed successfully.
//
// Globally-routable addresses first. A peer announces every address it has, and
// taking the list head meant a LAN address could be chosen over a public one
// purely because of interface ordering on the far side.
func bootstrapEndpoint(candidates []string) string {
	var fallback string
	for _, c := range candidates {
		ap, err := netip.ParseAddrPort(c)
		if err != nil {
			continue
		}
		if ap.Addr().IsGlobalUnicast() && !ap.Addr().IsPrivate() &&
			!ap.Addr().IsLinkLocalUnicast() && !ap.Addr().IsLoopback() {
			return c
		}
		if fallback == "" {
			fallback = c
		}
	}
	// A private address is still worth trying when it is all we have: two nodes
	// on the same LAN reach each other that way and nothing else.
	return fallback
}

func hasBest(m *Mesh, id string, now time.Time) bool {
	_, ok := m.prober.Best(id, now)
	return ok
}

// syncPeers pushes the roster into WireGuard.
func (m *Mesh) syncPeers() error {
	peers := m.roster.Peers()
	out := make([]wg.Peer, 0, len(peers))
	drifted := false

	now := time.Now()
	rl := m.selectRelay(now)
	// The data-plane view, so we can tell a peer we have never reached from one
	// whose endpoint WireGuard already learned and which must not be clobbered.
	stats, _ := m.dev.PeerStats()

	for _, p := range peers {
		// A peer with no announce for OfflineAfter and no live tunnel is gone;
		// keeping it configured means transmitting at a dead address forever.
		//
		// Both conditions, not just the first: rendezvous and the data plane
		// fail independently, so a peer that has gone quiet on Waku while its
		// tunnel still works must be kept. Tearing tunnels down because the
		// fleet went away is exactly what DESIGN §2 forbids.
		st, haveStats := stats[p.WGPub.String()]
		if !p.Online(now) && !(haveStats && st.Live(now)) {
			continue
		}

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
		case rl.ok && rl.id != p.ID():
			// No direct path. Route through the relay rather than leaving the
			// peer unreachable — failing over is just an endpoint swap, with no
			// tunnel teardown or rehandshake.
			peer.RelayVia = wg.NewRelayEndpoint(m.relayKey, rl.addr, m.st.Identity.WGPub, p.WGPub)
		case haveStats && st.Live(now):
			// Nothing probed and no relay, but the tunnel is working. Leave the
			// endpoint alone.
			//
			// This is the bug that took a mesh down for fourteen minutes: the
			// fallback below wrote an announced candidate over an endpoint that
			// was carrying traffic. Peers announce every address they have,
			// including LAN ones, so the "candidate" was 192.168.0.151 — routable
			// only from the announcer's own house. ADR-009 says never set an
			// endpoint that has not answered a probe; this case is where that
			// rule was being broken.
			peer.KeepEndpoint = true
		case bootstrapEndpoint(p.Endpoints) != "":
			// Never reached this peer and have no relay: try what was announced.
			// This is a bootstrap guess, so prefer an address that could
			// plausibly work from here over one that certainly cannot.
			peer.Endpoint = bootstrapEndpoint(p.Endpoints)
		}
		// Has the device drifted away from what we last asked for? WireGuard
		// roams a peer's endpoint to wherever its packets arrive from, so a
		// peer that replies over the relay moves the endpoint under us. The
		// desired configuration is then unchanged while the device disagrees,
		// and SetPeers would skip the write and keep the drift.
		//
		// Seen in the field: `paths` reported the direct LAN address selected
		// and in use at 5ms while the tunnel ran through a VPS on another
		// continent, because a relayed packet had rewritten the endpoint and
		// nothing ever wrote it back.
		if peer.Endpoint != "" && haveStats && st.Endpoint != "" && st.Endpoint != peer.Endpoint {
			drifted = true
		}

		out = append(out, peer)
	}
	if drifted {
		m.log.Info("data plane drifted from the selected paths; rewriting")
		m.dev.Invalidate()
	}
	if err := m.dev.SetPeers(out); err != nil {
		return err
	}
	m.refreshHosts()
	return nil
}

// refreshHosts keeps /etc/hosts in step with the roster, when asked to.
//
// Called after every successful peer sync rather than on a timer: the roster is
// what it reflects, so it should change exactly when the roster does. Apply is a
// no-op when the rendered block is unchanged, so an unchanged sync does not
// touch the file.
func (m *Mesh) refreshHosts() {
	if !m.cfg.ManageHosts {
		return
	}

	entries := []hosts.Entry{{Name: m.cfg.Name, Addr: m.self.String(), Self: true}}
	for _, p := range m.roster.Peers() {
		entries = append(entries, hosts.Entry{Name: p.Name, Addr: p.Overlay.String()})
	}

	suffix := m.cfg.HostsSuffix
	if suffix == "" {
		suffix = "mesh"
	}

	changed, err := hosts.Apply(hosts.DefaultFile, hosts.Render(entries, suffix))
	if err != nil {
		// Not fatal: the mesh works without names. Warn once per occurrence
		// rather than failing the sync.
		m.log.Warn("could not update /etc/hosts", "err", err)
		return
	}
	if changed {
		m.log.Info("updated /etc/hosts", "entries", len(entries))
	}
}
