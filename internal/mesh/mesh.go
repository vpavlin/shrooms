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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/hosts"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/topic"
	"github.com/vpavlin/shrooms/internal/v4"
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

	// inv are invite topics held open for an enrolment. See invite.go.
	inv invites

	// v4 maps peers to synthetic IPv4 addresses. Nil when the translator is
	// not in use, which is what the network-key-only path looks like.
	v4 *v4.Table

	// fed says events arrive through Deliver rather than being read from the
	// node. Set when this mesh shares a node with others.
	fed bool

	discoKey disco.Key
	prober   *disco.Prober

	relayKey relay.Key

	// frameKey authenticates relay frames, and is not always relayKey.
	//
	// A relay that is a member of this mesh shares its network key, so the two
	// are the same. A blind relay must never hold it, so frames authenticate
	// under the relay's own token — or under a public key when it is open,
	// where the MAC is a checksum and every real guarantee comes from the
	// registrant's signature and the routability check instead.
	frameKey relay.Key

	// blind says the relay we use is not a member, so handles are tags.
	blind bool

	// relayLive records when each configured relay last answered a routability
	// challenge, which is the only positive signal a client ever gets from one.
	// A registration is unacknowledged; a challenge is not.
	relayMu   sync.Mutex
	relayLive map[netip.AddrPort]time.Time
	// relaySrv is non-nil only when this node acts as a relay for others.
	relaySrv *relay.Server
	// relays are the hand-configured relays, in the order given, each with the
	// key it is spoken to under. See registerWith for how many are occupied at
	// once and selectRelay for which carries traffic.
	relays []relayTarget

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

	// revoked holds withdrawals this node has verified for itself. Kept per
	// node rather than asked of anyone, so a compromised peer cannot un-revoke
	// a device by staying quiet.
	revoked *cred.List

	// networkID names this mesh in the state dir, so its revocations survive a
	// restart (see loadRevocations).
	networkID string

	// revMu guards lastRevAnnounce, which is touched from the receive path
	// (a peer appearing) and read on the same path — the epoch republish runs
	// on the ticker goroutine and does not consult it.
	revMu           sync.Mutex
	lastRevAnnounce time.Time

	// lastBoot is the bootstrap address we last wrote down for ourselves, so
	// an unchanged one is not rewritten every announce.
	lastBoot string

	// noEndpoints is whether we have already said we have nothing to announce,
	// so it is said once per spell rather than every 45 seconds.
	noEndpoints bool

	// heardAnything is set the first time any packet arrives on the data-plane
	// socket from any peer — a disco ping, a pong, a relay frame.
	//
	// Nothing recorded this, and it is the fact that separates "the network is
	// hard" from "nothing can reach this machine". A node whose inbound port is
	// blocked announces good addresses, sees every peer come online over the
	// rendezvous plane, and fails every handshake forever. The evidence that it
	// is a closed door rather than a difficult NAT is that *not one packet* has
	// ever arrived, and that was invisible.
	heardAnything bool

	// saidUnreachable stops the diagnosis repeating on every probe tick.
	saidUnreachable bool

	// started is when Run began, so "nothing has ever reached us" can be
	// distinguished from "we only just started".
	started time.Time

	// lastEpoch is the rendezvous epoch the last announce went out under. A
	// change is the moment to re-publish what we know has been withdrawn.
	lastEpoch int64

	// grants remembers renewals already seen, so relaying one does not become
	// a broadcast storm: every node passes on what is new to it, and without
	// this "new to it" would be true every time it came back round.
	grants map[[32]byte]time.Time

	// roams counts how often each peer's endpoint has been corrected lately,
	// so a losing fight can be conceded rather than repeated forever.
	roams map[string]roamFight

	// services is what each peer says it offers (ADR-023), keyed by peer id.
	services map[string]peerServices

	// lastServices is when this node last published its own list, so a burst
	// of arrivals does not become a burst of messages.
	lastServices time.Time

	// mapped is the external address the router gave us, if it was willing to
	// give one (ADR-024). Zero when there is none.
	mapped netip.AddrPort

	// expiry is when each peer's credential runs out, learned from the
	// announces we have already verified. Kept here rather than on the roster
	// because it is not part of how a peer is reached — it is how long the
	// admin has to renew before it stops being one.
	expiry map[string]int64

	// expiredDropped remembers whose credential we have already acted on, so
	// noticing an expiry asks for one resync rather than one every probe tick
	// for as long as the peer stays in the roster. Guarded by mu, like expiry.
	expiredDropped map[string]bool

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
		log:            log,
		cfg:            cfg,
		st:             st,
		nk:             nk,
		node:           node,
		dev:            dev,
		roster:         NewRoster(nk, st.Identity.DevicePub),
		authority:      auth,
		revoked:        cred.NewList(),
		grants:         map[[32]byte]time.Time{},
		expiry:         map[string]int64{},
		expiredDropped: map[string]bool{},
		guard:          control.NewReplayGuard(),
		self:           identity.OverlayAddr(nk, st.Identity.DevicePub),
		discoKey:       disco.DeriveKey(nk),
		relayKey:       relay.DeriveKey(nk),
		resync:         make(chan struct{}, 1),
		reannounce:     make(chan struct{}, 1),
		repliedTo:      make(map[string]time.Time),
		health:         newHealth(),
		timing:         newTimings(time.Now()),
		rates:          newRates(),
		subscribed:     make(map[string]bool),
	}
	m.networkID = state.NetworkID(nk)
	m.loadRevocations()
	// The receiver's replay marks, which used to be rebuilt empty on every
	// start — reopening the rollback window at exactly the moment an observer
	// would try it. See state.SeqMarks.
	m.guard.Load(m.st.SeqMarks(m.networkID))
	if cfg.Relay {
		m.relaySrv = relay.NewServer(m.relayKey, m.ownsWGKey)
		log.Info("acting as a relay for this mesh")
	}
	// Configured relays, each carrying the key it is spoken to under. Member
	// relays first: a relay of your own is preferable to a stranger's, and
	// listing both is the ordinary state of affairs while moving between them.
	for _, one := range strings.Split(cfg.RelayAddr, ",") {
		if ap, ok := parseRelayAddr(log, one); ok {
			m.relays = append(m.relays, relayTarget{addr: ap, key: m.relayKey})
		}
	}
	// A blind relay authenticates under its operator's token, or a public key
	// when it is open. Never under the mesh's own — that is derived from the
	// network key, and a stranger holding it could read every announce.
	blindKey := relay.OpenKey()
	if cfg.RelayToken != "" {
		blindKey = relay.TokenKey(cfg.RelayToken)
	}
	for _, one := range cfg.RelayBlind {
		if ap, ok := parseRelayAddr(log, one); ok {
			m.relays = append(m.relays, relayTarget{addr: ap, key: blindKey, blind: true})
		}
	}
	if len(m.relays) > 0 {
		log.Info("relays pinned by config", "count", len(m.relays),
			"blind", len(cfg.RelayBlind), "token", cfg.RelayToken != "")
	}
	if cfg.RelayToken != "" && len(cfg.RelayBlind) == 0 {
		log.Warn("relay_token is set with no relay_blind; a token is only used " +
			"with a relay that is not a member of this mesh")
	}

	// Kept for the paths that have already chosen a relay and only need to know
	// which kind it was. True when every configured relay is blind, which is
	// the case once somebody has moved off their own.
	m.blind = len(cfg.RelayBlind) > 0 && cfg.RelayAddr == ""
	m.frameKey = m.relayKey
	if m.blind {
		m.frameKey = blindKey
	}

	m.prober = disco.NewProber(m.discoKey, st.Identity.DevicePriv, m.sendDisco)

	// Control packets share the WireGuard socket; this is what makes NAT
	// traversal possible at all.
	dev.Bind.SetControlHandler(m.handleControl)

	// ParseEndpoint needs these to rebuild relay endpoints when WireGuard hands
	// back an endpoint string over the UAPI.
	dev.Bind.SetRelayIdentity(m.frameKey, m.relayHandle(st.Identity.WGPub))
	// And per relay, because a node may use its own and a stranger's at once,
	// and they are spoken to under different keys with different handles. The
	// global pair above remains the answer for a relay found by discovery,
	// which is always a member.
	for _, t := range m.relays {
		dev.Bind.SetRelayIdentityFor(t.addr, t.key, m.handleFor(t, st.Identity.WGPub))
	}

	return m, nil
}

// Paths returns every probed path for a peer, for diagnostics.
func (m *Mesh) Paths(peerID string) []disco.Path { return m.prober.Paths(peerID) }

// BestPath returns the selected path for a peer, if any.
func (m *Mesh) BestPath(peerID string, now time.Time) (disco.Path, bool) {
	return m.prober.Best(peerID, now)
}

// RelayStats reports what this node has done as a relay, or false when it is
// not one.
//
// Exposed because a relay was a black box: it either carried traffic or it did
// not, and nothing said which devices it had registered or how many it had
// turned away. "Is the relay broken?" then has no better answer than trying it.
func (m *Mesh) RelayStats() (relay.Stat, bool) {
	if m.relaySrv == nil {
		return relay.Stat{}, false
	}
	return m.relaySrv.Stats(), true
}

// Reflexive returns the self-addresses peers have reported observing.
func (m *Mesh) Reflexive() []netip.AddrPort { return m.prober.Reflexive(time.Now()) }

// Announced is what this node currently puts in its announce as the places it
// can be reached.
//
// Exposed because it was the one fact nobody could see. `paths` shows what
// peers announce and where peers observe us, and a node with an empty list of
// its own looks identical to a node with a full one — while being undialable by
// everybody. Diagnosing that took three rounds of asking somebody to read a
// journal, which is three rounds too many for a question the daemon can answer.
func (m *Mesh) Announced() []string { return m.candidates() }

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

// GossipPeers reports how many peers this node shares its shard's gossipsub
// mesh with, or -1 when the library will not say.
//
// The measurement the deafness checks have been working around. Being deaf to
// half the mesh while other applications' traffic keeps arriving is what a
// degraded gossipsub mesh looks like from the outside — few enough peers that
// some publishers' messages never reach us — and this asks the question
// directly rather than inferring it from what did not arrive.
//
// Not used to decide anything on its own: a healthy node can briefly hold few
// peers, and the number means nothing without the announces it explains. It
// goes in the log beside a repair so that "why did this node go deaf" has an
// answer next time.
func (m *Mesh) GossipPeers() int {
	if m.node == nil {
		return -1
	}
	raw, err := m.node.PeersInMesh(topic.MeshPubsubTopic(m.cfg.ClusterID, topic.NumShardsLogosDev))
	if err != nil {
		return -1
	}
	// The library answers with a bare number as a string, or a JSON array of
	// peer ids depending on version; count either rather than trusting one.
	raw = strings.TrimSpace(raw)
	if n, err := strconv.Atoi(strings.Trim(raw, `"`)); err == nil {
		return n
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err == nil {
		return len(ids)
	}
	return -1
}

// Regraft tries to rejoin the shard's gossipsub mesh without restarting
// anything, by dropping every subscription and taking it out again.
//
// The cheap rung of the ladder. A node that has gone deaf is usually still
// connected and still receiving other applications' traffic, so the connection
// is not the problem — the mesh membership for our topics is — and unsubscribe
// followed by subscribe is what asks the library to graft again.
//
// Reports whether it got as far as re-announcing, which is what a peer needs
// to see from us before it will believe we are back.
func (m *Mesh) Regraft(now time.Time) error {
	m.mu.Lock()
	had := len(m.subscribed)
	m.subscribed = map[string]bool{}
	m.mu.Unlock()

	for _, name := range topic.Window(m.nk, now) {
		// Best effort: unsubscribing from a topic we are not on is not an
		// error worth stopping for, and the subscribe below is the half that
		// matters.
		if err := m.node.Unsubscribe(name); err != nil {
			m.log.Debug("unsubscribe during regraft", "topic", name, "err", err)
		}
	}
	m.log.Info("rejoining the shard's gossip mesh",
		"topics", had, "gossip_peers", m.GossipPeers())

	if err := m.resubscribe(now); err != nil {
		return err
	}
	return m.announceFresh(now)
}

// Deaf reports which peers this node can plainly reach but has stopped hearing
// announce from, and how many it can reach at all.
//
// Both numbers, because one deaf peer proves much less than several. A single
// peer whose announces stop while its tunnel lives could be that peer having a
// bad time on its own rendezvous connection — which is its problem to notice,
// not ours. Several at once, out of the few we can reach, is the shape of this
// node being the deaf one. The caller decides where the line is.
//
// The inference is what makes this worth having. WireGuard lives inside the
// daemon, so a handshake completed in the last couple of minutes proves the
// peer's daemon is running — and a running daemon announces every 45 seconds.
// If we have not seen an announce from it for many minutes while its tunnel
// keeps rekeying, the peer is not the one that stopped: we are.
//
// This catches what a plain "nothing has arrived" check cannot. A node can go
// deaf to some publishers and not others — one gossipsub mesh degrading while
// another holds — which looks like those particular peers going offline, one
// at a time, for no reason. Established tunnels keep working throughout, so
// nothing else notices, and what breaks is discovery: a peer that moves is
// never found again, and relaying stops, because a relay is only chosen among
// peers believed to be online.
func (m *Mesh) Deaf(now time.Time) (deaf []string, reachable int) {
	stats, err := m.PeerStats()
	if err != nil {
		return nil, 0
	}
	for _, p := range m.roster.Peers() {
		st, ok := stats[p.WGPub.String()]
		if !ok || now.Sub(st.LastHandshake) > wg.LiveWindow {
			continue // no tunnel, so nothing is proven either way
		}
		reachable++
		if now.Sub(p.LastSeen) > DeafAfter {
			deaf = append(deaf, p.Name)
		}
	}
	return deaf, reachable
}

// DeafAfter is how long an announce may be missing from a peer whose tunnel is
// live before this node calls itself deaf to it.
//
// Generous against RendezvousStale (3 minutes): a single missed announce, or
// several, is ordinary on a lossy link, and this must mean "the stream from
// that peer has stopped", not "a packet was lost".
const DeafAfter = 12 * time.Minute

// Roster exposes the peer roster.
func (m *Mesh) Roster() *Roster { return m.roster }

// Authority is the set of admin keys this mesh trusts, or nil when it has none.
//
// Exposed so a signer outside this package can check it is one of them before
// issuing — a card that is not in admin_keys produces a credential every peer
// will refuse, and finding that out on the joining device days later is a poor
// way to learn it.
func (m *Mesh) Authority() *cred.Authority { return m.authority }

// Self returns this node's overlay address.
func (m *Mesh) Self() netip.Addr { return m.self }

// Run drives the mesh until ctx is cancelled.
func (m *Mesh) Run(ctx context.Context) error {
	m.started = time.Now()
	// Unless somebody else is feeding us. Several meshes share one rendezvous
	// node, and a channel delivers each event to exactly one reader — so two
	// meshes reading the node directly would take it in turns, each dropping
	// what the other should have had. See Deliver.
	if !m.fed {
		go m.consume(ctx)
	}

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

	// Service lists change when somebody edits a config, so this is slow on
	// purpose (ADR-023). The first one goes out with the first announce, so a
	// node that has just started does not look serviceless for five minutes.
	servicesTicker := time.NewTicker(ServicesInterval)
	defer servicesTicker.Stop()
	m.loadServices(time.Now())
	if err := m.publishServices(time.Now()); err != nil {
		m.log.Debug("could not announce services", "err", err)
	}

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
		case now := <-servicesTicker.C:
			if err := m.publishServices(now); err != nil {
				m.log.Debug("could not announce services", "err", err)
			}
		case now := <-probeTicker.C:
			m.probeAll(now)
			m.registerWithRelay()
			m.reportUnknown()
			m.reportUnreachable(now)
			m.checkTunnels(now)
			m.checkExpiries(now)
			m.sampleRates(now)
			m.pruneForgotten(now)
		case now := <-ticker.C:
			// A rendezvous node that dropped and came back has lost its
			// subscriptions; ours is the only side that knows they are gone.
			m.repairRendezvous(now)

			// Each epoch rotation, say again what has been withdrawn. See
			// republishRevocations: this is what makes a revocation durable
			// for a node that was not listening when it was first published.
			if e := topic.Epoch(now); e != m.lastEpoch {
				if m.lastEpoch != 0 {
					m.republishRevocations(now)
				}
				m.lastEpoch = e
			}

			// Resubscribe first: near an epoch boundary the next topic must be
			// live before we publish to it.
			if err := m.resubscribe(now); err != nil {
				m.log.Warn("resubscribe failed", "err", err)
			}
			if err := m.announce(now); err != nil {
				m.log.Warn("announce failed", "err", err)
			}
			// On the announce tick rather than per accepted announce: this is
			// already the cadence at which the sequence number is written, and
			// losing the last 45 seconds of marks to a crash costs one replay
			// window, which is what the situation was before any of this.
			m.saveSeqMarks()
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
// That last check is the one that matters. A credential holds nothing secret,
// and every mesh member can read it: it rides inside an announce, which is
// encrypted to the mesh rather than to a recipient, so any member — or anyone
// who has obtained the network key — can lift one. Binding it to the key that
// signed the announce is what stops a member replaying another member's
// membership. It is not visible to a passing observer on the shard, who sees
// only a fixed-size ciphertext.
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
	if m.revoked.Revoked(c) {
		return cred.ErrRevoked
	}
	m.mu.Lock()
	if m.expiry == nil {
		m.expiry = map[string]int64{}
	}
	m.expiry[hex.EncodeToString(a.DevicePub)] = c.NotAfter
	// A renewal arrived: this peer may be carried again, and if it expires
	// once more that is a new event worth acting on.
	delete(m.expiredDropped, hex.EncodeToString(a.DevicePub))
	m.mu.Unlock()
	return nil
}

// Member is what an admin needs to renew one device: the two public keys the
// credential binds together, the name it carries, and when the one it holds
// runs out.
type Member struct {
	DevicePub []byte
	WGPub     []byte
	Name      string
	NotAfter  time.Time // zero when this mesh has no authority
}

// Members reports every device this node knows to be on the mesh, itself
// included.
//
// Itself included because renewal is a sweep and the node running the admin
// tooling is as much a member as any other — leaving it out is how the machine
// with the admin key on it becomes the one that expires.
func (m *Mesh) Members() []Member {
	m.mu.Lock()
	exp := make(map[string]int64, len(m.expiry))
	for k, v := range m.expiry {
		exp[k] = v
	}
	m.mu.Unlock()

	out := []Member{{
		DevicePub: m.st.Identity.DevicePub,
		WGPub:     m.st.Identity.WGPub[:],
		Name:      m.cfg.Name,
		NotAfter:  m.SelfExpiry(),
	}}
	for _, p := range m.roster.Peers() {
		mem := Member{DevicePub: p.DevicePub, WGPub: p.WGPub[:], Name: p.Name}
		if t, ok := exp[p.ID()]; ok {
			mem.NotAfter = time.Unix(t, 0)
		}
		out = append(out, mem)
	}
	return out
}

// SelfExpiry is when this device's own credential runs out, or the zero time
// when it holds none.
func (m *Mesh) SelfExpiry() time.Time {
	c, err := cred.UnmarshalCredential(m.st.Credential)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(c.NotAfter, 0)
}

// Unenrolled reports that this mesh admits by credential and this device does
// not hold one.
//
// It is the one state in which a node is completely healthy and completely
// invisible: it announces, its peers refuse it before it reaches their roster,
// and it therefore sees them while none of them sees it. Nothing else on the
// status page hints at it, which is how it cost an afternoon of looking at the
// wrong machine — the symptom presents on the peers, not here.
func (m *Mesh) Unenrolled() bool {
	if m.authority == nil {
		return false // membership is the network key; there is nothing to hold
	}
	_, err := cred.UnmarshalCredential(m.st.Credential)
	return err != nil
}

// PeerExpiry is when a peer's credential runs out, or the zero time when this
// node has not seen one for it.
//
// A gap that is not a fault: credentials are learned from the announcements a
// peer makes, so a peer that has been quiet since this node started is simply
// unknown here. A viewer must show "unknown" rather than "expired" for it —
// the two look the same in a struct and mean opposite things to whoever is
// deciding whether to go and reissue.
func (m *Mesh) PeerExpiry(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.expiry[id]; ok {
		return time.Unix(t, 0)
	}
	return time.Time{}
}

// expired reports that a peer's credential has run out, so it should no longer
// be carried on the data plane.
//
// Only on a mesh that admits by credential. Where membership is the network key
// there is nothing to expire, and a peer with no recorded expiry is one whose
// announce we have not verified in this process — a node that has just started
// knows nobody's expiry yet, and dropping every peer until each has announced
// again would turn a restart into an outage.
func (m *Mesh) expired(id string, now time.Time) bool {
	if m.authority == nil {
		return false
	}
	m.mu.Lock()
	at, known := m.expiry[id]
	m.mu.Unlock()
	if !known {
		return false
	}
	return now.After(time.Unix(at, 0))
}

// checkExpiries drops the tunnel to anyone whose credential has just run out.
//
// syncPeers is where the decision lives, but nothing was asking it to run: a
// mesh where everybody is up and nothing moves can go hours without a resync,
// and "expired" is not an event anything else notices. So this looks once a
// tick and asks — once per peer, because the roster keeps a device for
// ForgetAfter and it stays expired the whole time.
func (m *Mesh) checkExpiries(now time.Time) {
	if m.authority == nil {
		return
	}
	for _, p := range m.roster.Peers() {
		id := p.ID()
		if !m.expired(id, now) {
			continue
		}
		if !m.noteExpired(id) {
			continue
		}
		// Loud. This takes a working device off the mesh on a schedule, and
		// the person it happens to is usually elsewhere: the admin renews with
		// a sweep, and a device that was asleep through it comes back to this.
		m.log.Warn("credential expired; the tunnel is being dropped",
			"peer", p.Name, "expired", m.PeerExpiry(id).Format(time.RFC3339),
			"hint", "shrooms admin renew")
		m.requestResync()
	}
}

// noteExpired records that a peer's expiry has been acted on, reporting whether
// this was the first time.
//
// The roster keeps a device for ForgetAfter and it stays expired for all of it,
// so without this the probe tick would ask for a resync every three seconds for
// six hours.
func (m *Mesh) noteExpired(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.expiredDropped[id] {
		return false
	}
	m.expiredDropped[id] = true
	return true
}

// saveSeqMarks writes the replay high-water marks.
//
// Best effort and quiet: this is a hardening measure, and a node that cannot
// write them still enforces everything it holds in memory. Failing an announce
// over it would trade a working mesh for a smaller replay window.
func (m *Mesh) saveSeqMarks() {
	if err := m.st.SetSeqMarks(m.networkID, m.guard.Snapshot()); err != nil {
		m.log.Debug("could not persist replay marks", "err", err)
	}
}

// ownsWGKey answers whether a device may register a tunnel key, which is the
// question a relay cannot answer alone (ADR-029).
//
// The roster is the source, and it is a good one: every entry comes from an
// announce the device signed, which names both of its keys — so the pairing is
// asserted by the only party entitled to assert it. On a mesh with an authority
// it is stronger still, because checkMembership refuses an announce whose
// tunnel key disagrees with the credential that names it.
//
// A device we have never heard announce is refused. That is a real cost — a
// peer registering with a relay that has not yet seen it waits for the next
// refresh — and the alternative is accepting a claim from anybody, which is the
// hijack itself.
func (m *Mesh) ownsWGKey(devicePub []byte, wg identity.WGKey) bool {
	if len(devicePub) == 0 {
		return false
	}
	if bytes.Equal(devicePub, m.st.Identity.DevicePub) {
		return wg == m.st.Identity.WGPub
	}
	for _, p := range m.roster.Peers() {
		if bytes.Equal(p.DevicePub, devicePub) {
			return p.WGPub == wg
		}
	}
	return false
}

// loadRevocations re-reads what this node had already been told is withdrawn.
//
// Every entry is verified again against the authority rather than trusted for
// having been on our own disk: the file is the same shape as something a peer
// hands us, and a list that is believed because of where it was found is a list
// that can be edited by anyone who can write there.
//
// A mesh with no authority has nothing to verify against and nothing to revoke,
// so it skips this entirely.
func (m *Mesh) loadRevocations() {
	if m.authority == nil {
		return
	}
	raws := m.st.Revocations(m.networkID)
	kept := 0
	for _, raw := range raws {
		r, err := cred.UnmarshalRevocation(raw)
		if err != nil {
			continue
		}
		if err := cred.VerifyRevocationBy(m.authority, r); err != nil {
			m.log.Warn("dropping a stored revocation this mesh did not sign", "err", err)
			continue
		}
		if m.revoked.Add(r, raw) {
			kept++
		}
	}
	if kept > 0 {
		m.log.Info("restored revocations", "count", kept)
	}
}

// saveRevocations writes the list back, so a restart does not re-admit a device
// somebody deliberately removed.
func (m *Mesh) saveRevocations() {
	if m.authority == nil {
		return
	}
	if err := m.st.SetRevocations(m.networkID, m.revoked.All()); err != nil {
		// Not fatal, and deliberately loud: the node keeps enforcing what it
		// holds in memory, and the operator needs to know that guarantee now
		// ends at the next restart.
		m.log.Error("could not persist revocations; they will be lost on restart", "err", err)
	}
}

// republishRevocations puts everything this node knows is withdrawn back on the
// bus.
//
// A revocation used to be relayed exactly once, by whoever first heard it. That
// left the guarantee resting on who happened to be online in that instant: a
// node that was asleep, or that joined later, never learned it and admitted the
// device for the rest of its credential's life. Re-publishing on each epoch
// rotation makes the withdrawal a standing statement rather than an event, so a
// node that misses it is wrong for at most an epoch rather than for a month.
//
// The cost is bounded by what an admin has signed — one small message per
// revoked device per hour — and revocations are the one control message where
// saying it again is always safe.
func (m *Mesh) republishRevocations(now time.Time) {
	all := m.revoked.All()
	if len(all) == 0 {
		return
	}
	for _, raw := range all {
		if err := m.publishRevocation(raw, now); err != nil {
			m.log.Debug("could not re-publish a revocation", "err", err)
		}
	}
	m.log.Debug("re-published revocations", "count", len(all))
}

// RevocationRepublishCooldown bounds how often a burst of arrivals can make
// this node repeat itself.
const RevocationRepublishCooldown = 2 * time.Minute

// revocationsOnDiscovery repeats the withdrawal list when a peer appears.
//
// Published to the mesh topic rather than to the peer that arrived, because
// that is the only way a revocation travels — which means N arrivals in one
// restart would otherwise publish the same list N times to the same audience.
// The cooldown makes a wave of reconnections cost one repetition.
//
// Not gated on being an admin, because no daemon holds the admin key: the
// authority is deliberately off the devices (ADR-018), so "the admin node"
// is not a thing that exists at runtime. Every node holding a revocation can
// repeat it, every receiver verifies the signature itself, and that is the
// failover — no node's absence stops a withdrawal propagating.
func (m *Mesh) revocationsOnDiscovery(now time.Time) {
	if m.shouldRepeatRevocations(now) {
		m.republishRevocations(now)
	}
}

// shouldRepeatRevocations answers whether this arrival earns a repetition, and
// records it if so.
//
// Split from the publish because this is the part with a decision in it: the
// switch, the empty list, and the cooldown. The publish itself needs a live
// rendezvous node, and a test that had to stand one up to check a two-minute
// timer would be testing the wrong thing.
func (m *Mesh) shouldRepeatRevocations(now time.Time) bool {
	if m.cfg.QuietRevocations || m.revoked.Len() == 0 {
		return false
	}
	m.revMu.Lock()
	defer m.revMu.Unlock()
	if !m.lastRevAnnounce.IsZero() && now.Sub(m.lastRevAnnounce) < RevocationRepublishCooldown {
		return false
	}
	m.lastRevAnnounce = now
	return true
}

// publishRevocation puts a withdrawal on the bus.
//
// Re-published by every node that learns one, not only by the admin: an admin
// is rarely online, and a revocation that only travels while it is would arrive
// nowhere. Anyone may relay one because nobody has to be trusted to — the
// signature inside is what makes it true.
func (m *Mesh) publishRevocation(raw []byte, now time.Time) error {
	msg := &control.Revoke{
		Kind:      control.KindRevoke,
		DevicePub: m.st.Identity.DevicePub,
		Payload:   raw,
		Timestamp: now.Unix(),
	}
	sealed, err := control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, msg)
	if err != nil {
		return err
	}
	_, err = m.node.Send(topic.Current(m.nk, now), sealed, true)
	return err
}

// handleRevocation takes a withdrawal off the bus.
//
// Verified here rather than trusted: the message arrives from a mesh member,
// and a member may be hostile. Only a signature from this mesh's authority
// makes it a revocation rather than an assertion.
//
// Kept for as long as the revocation itself says (--keep-for), which defaults
// to the standard credential life plus a day rather than to the actual expiry
// of the credential withdrawn — nothing looks that up. After it lapses, expiry
// does the same job.
//
// Not "keeping them forever would grow without bound on the input an attacker
// controls", which this said: applyRevocation verifies the admin signature
// before anything is stored, so only the admin-key holder can add an entry.
// The bound exists to stop the list growing with the mesh's own history, not to
// stop an attacker.
func (m *Mesh) handleRevocation(raw []byte, now time.Time) {
	if _, err := m.applyRevocation(raw, now); err != nil {
		m.log.Warn("ignoring a revocation", "err", err)
	}
}

// Revoke takes a revocation from the admin tooling and puts it on the bus.
//
// This is how one gets there at all: the admin signs offline, and the daemon is
// the only thing on the machine with a rendezvous connection. Verified exactly
// as one arriving from a peer would be — the socket says who may ask, not what
// is true.
func (m *Mesh) Revoke(raw []byte) error {
	fresh, err := m.applyRevocation(raw, time.Now())
	if err != nil {
		return err
	}
	if !fresh {
		// Already known, and already relayed when it first arrived. Not an
		// error: re-publishing a revocation is always safe and often the point.
		return m.publishRevocation(raw, time.Now())
	}
	return nil
}

// applyRevocation verifies, records, and passes on. Reports whether it was new.
func (m *Mesh) applyRevocation(raw []byte, now time.Time) (bool, error) {
	if m.authority == nil {
		return false, errors.New("this mesh has no admin keys, so nothing can be revoked")
	}
	r, err := cred.UnmarshalRevocation(raw)
	if err != nil {
		return false, fmt.Errorf("unreadable revocation: %w", err)
	}
	if err := cred.VerifyRevocationBy(m.authority, r); err != nil {
		return false, fmt.Errorf("this mesh did not sign that revocation: %w", err)
	}
	if !m.revoked.Add(r, raw) {
		return false, nil
	}
	// To disk before anything else: the rest of this function drops the peer
	// and tells the bus, and a crash between those and the next write would
	// leave a node that has forgotten why it dropped anybody.
	m.saveRevocations()
	m.log.Info("device revoked",
		"device", hex.EncodeToString(r.DevicePub)[:16], "serial", r.Serial)
	// Drop it now rather than waiting for its announce to lapse.
	m.roster.Forget(r.DevicePub)
	m.requestResync()
	// Pass it on. New to us means possibly new to a peer, and a node that
	// was offline when the admin published learns it from whoever is up.
	if err := m.publishRevocation(raw, now); err != nil {
		m.log.Debug("could not relay a revocation", "err", err)
	}
	return true, nil
}

// Grant takes a renewed credential from the admin tooling and puts it on the
// mesh, exactly as Revoke does for a withdrawal.
//
// The admin signs one credential per device and hands the lot to whichever
// node it can reach; each one travels to the device it names. Nothing here
// assumes that device is online now — a node that was away picks its own up
// from the next admin sweep, which is why renewal starts well before expiry.
func (m *Mesh) Grant(raw []byte) error {
	if err := m.handleGrant(raw, time.Now()); err != nil {
		return err
	}
	// Publish even when it was ours to keep: a credential for this device is
	// still news to nobody else, but one for a peer must travel, and the admin
	// cannot tell from here which case it handed us.
	return m.publishGrant(raw, time.Now())
}

// publishGrant puts a renewed credential on the bus.
func (m *Mesh) publishGrant(raw []byte, now time.Time) error {
	if m.node == nil {
		// A mesh with no rendezvous connection can still keep a credential
		// handed to it directly; it just cannot pass one on.
		return errors.New("no rendezvous connection")
	}
	msg := &control.Grant{
		Kind:      control.KindGrant,
		DevicePub: m.st.Identity.DevicePub,
		Payload:   raw,
		Timestamp: now.Unix(),
	}
	sealed, err := control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, msg)
	if err != nil {
		return err
	}
	_, err = m.node.Send(topic.Current(m.nk, now), sealed, true)
	return err
}

// handleGrant takes a renewed credential off the bus: keeps it if it is ours,
// relays it if it is new, and refuses it if this mesh did not sign it.
//
// The check that matters is the last one. A credential is public and travels
// through other members' hands, so the only thing standing between a renewal
// and a forgery is the admin signature — which is verified here, on arrival,
// against the same authority that admits peers.
func (m *Mesh) handleGrant(raw []byte, now time.Time) error {
	if m.authority == nil {
		return errors.New("this mesh has no admin keys, so a credential means nothing here")
	}
	c, err := cred.UnmarshalCredential(raw)
	if err != nil {
		return fmt.Errorf("unreadable credential: %w", err)
	}
	if err := cred.VerifyBy(m.authority, c, now); err != nil {
		return fmt.Errorf("this mesh did not sign that credential: %w", err)
	}

	if !bytes.Equal(c.DevicePub, m.st.Identity.DevicePub) {
		// Somebody else's. Relay it once — they may be the reason it was sent
		// through us — and forget it.
		m.relayGrant(raw, now)
		return nil
	}
	if !bytes.Equal(c.WGPub, m.st.Identity.WGPub[:]) {
		return errors.New("credential names this device but a different tunnel key")
	}

	// Ours. Keep the later one: an admin sweep may reissue while an older
	// credential is still in flight, and going backwards would shorten the
	// life of the thing that is keeping this device on the mesh.
	if cur, err := cred.UnmarshalCredential(m.st.Credential); err == nil && cur.NotAfter >= c.NotAfter {
		return nil
	}
	m.st.Credential = raw
	if err := m.st.Save(); err != nil {
		return fmt.Errorf("store the renewed credential: %w", err)
	}
	m.log.Info("credential renewed",
		"until", time.Unix(c.NotAfter, 0).UTC().Format(time.RFC3339), "serial", c.Serial)
	// Announce with it now rather than at the next tick: peers check the
	// credential on every announce, and the one they hold for us is the old.
	m.requestAnnounce()
	return nil
}

// relayGrant passes a renewal on, once.
func (m *Mesh) relayGrant(raw []byte, now time.Time) {
	sum := sha256.Sum256(raw)
	m.mu.Lock()
	_, seen := m.grants[sum]
	if !seen {
		m.grants[sum] = now
		// Drop what can no longer be relevant. Cheap: this map holds one entry
		// per renewal in flight, which is one per device per month.
		for k, t := range m.grants {
			if now.Sub(t) > cred.DefaultLife {
				delete(m.grants, k)
			}
		}
	}
	m.mu.Unlock()
	if seen {
		return
	}
	if err := m.publishGrant(raw, now); err != nil {
		m.log.Debug("could not relay a renewal", "err", err)
	}
}

// reportUnknown logs packets WireGuard rejected as an unknown message type.
//
// wireguard-go logs these itself but without a source, which is what made them
// impossible to attribute when they appeared on both ends of a failing tunnel.
// Logged only when the count changes, so a healthy node stays quiet.
// unreachableFor is how long a node must be visibly online, announcing an
// address, and receiving nothing at all before that combination is called what
// it is.
//
// Long enough to clear an ordinary slow start — discovery alone takes about a
// minute, and a peer that has just woken is not evidence of anything. Short
// enough to be seen in the session where somebody is trying to make it work,
// rather than the one after they gave up.
const unreachableFor = 3 * time.Minute

// unreachable is the decision, separated from gathering the facts so it can be
// exercised without a prober, a roster or a socket. Those are what the
// surrounding code is for; this is the part with a judgement in it.
func (m *Mesh) unreachable(now time.Time, announced []string, anyPeerOnline bool) bool {
	switch {
	case m.saidUnreachable:
		return false // said once; the probe ticker runs every few seconds
	case m.heardAnything:
		return false // somebody got through; whatever is wrong, it is not this
	case now.Sub(m.started) < unreachableFor:
		return false // too early to tell an outage from a slow start
	case len(announced) == 0:
		return false // a different fault, and announceWith already reports it
	case !anyPeerOnline:
		return false // nobody was there to reach us
	}
	return true
}

// reportUnreachable says so when nothing can reach this node.
//
// The three facts together are conclusive, and each is useless alone:
//
//   - we announce at least one address, so peers know where to try
//   - a peer is online, so the rendezvous plane works and somebody is there
//   - not one packet has ever arrived on our socket
//
// A hard NAT still lets replies back. An offline peer would not be announcing.
// A wrong address would not be announced. What is left is a closed door: a host
// firewall, or a network that forbids clients talking to each other.
//
// This cost somebody an entire day, with every symptom pointing elsewhere —
// peers appearing normally, handshakes retrying forever, and a laptop happily
// announcing two perfectly good addresses that nothing was allowed to reach.
func (m *Mesh) reportUnreachable(now time.Time) {
	if m.saidUnreachable || m.heardAnything {
		return // cheap exits before walking the roster
	}
	var online bool
	for _, p := range m.roster.Peers() {
		if p.Online(now) {
			online = true
			break
		}
	}
	if !m.unreachable(now, m.candidates(), online) {
		return
	}
	m.saidUnreachable = true
	m.log.Warn("nothing has ever reached this node",
		"for", now.Sub(m.started).Round(time.Second),
		"we_announce", m.candidates(),
		"means", "peers can see this device and no packet from any of them arrives",
		"likely", "a host firewall, or a network that blocks client-to-client traffic",
		"fix", "allow inbound UDP "+strconv.Itoa(int(m.cfg.ListenPort)))
}

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

	// Computed once: it is also recorded below, and calling it twice would
	// take the peer-id lock twice for one announce.
	boot := m.bootAddr(now)
	// Our own address is worth writing down too. A node never sees its own
	// announce, so without this an invite minted on the bootstrap node itself
	// could not offer the one address it is certain of. Only on change, since
	// announces go out every 45 seconds and this is a file write.
	if boot != "" && boot != m.lastBoot {
		m.lastBoot = boot
		if err := m.st.NoteBootPeer(boot, now); err != nil {
			m.log.Debug("could not record our own bootstrap address", "err", err)
		}
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
		Boot:       boot,
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

	// The bootstrap address goes first, before any endpoint. It is a
	// convenience for somebody's *next* start (ADR-031); an endpoint is how
	// anybody reaches this node at all, now. Losing an endpoint to keep it
	// would be the wrong way round, and this field is new enough that the
	// trimming below has never had to compete with it before.
	if err != nil && a.Boot != "" && errors.Is(err, control.ErrTooLarge) {
		a.Boot = ""
		m.log.Debug("announce too large; dropping the bootstrap address")
		raw, err = control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, a)
	}

	wanted := len(a.Endpoints)
	for err != nil && len(a.Endpoints) > 0 && errors.Is(err, control.ErrTooLarge) {
		a.Endpoints = a.Endpoints[:len(a.Endpoints)-1]
		m.log.Debug("announce too large; dropping the least useful endpoint",
			"remaining", len(a.Endpoints))
		raw, err = control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, a)
	}

	// Announcing no endpoints at all is legitimate — a node behind NAT that has
	// not yet been told where it appears has none to give, and it still needs
	// to say it exists so a reachable peer can answer and teach it. But losing
	// every endpoint to *trimming* is a different thing wearing the same
	// clothes, and it is invisible: peers add a WireGuard peer they can never
	// dial and retry handshakes forever, which reads as a network fault.
	if wanted > 0 && len(a.Endpoints) == 0 {
		m.log.Warn("announcing no endpoints: they did not fit",
			"had", wanted, "name", m.cfg.Name,
			"hint", "a shorter device name leaves more room")
	}

	// Nothing to offer at all. Peers will see this node as online and be unable
	// to dial it, retrying handshakes against no endpoint until something
	// changes — so say why, once per state rather than every 45 seconds.
	if len(a.Endpoints) == 0 && wanted == 0 && !m.noEndpoints {
		m.noEndpoints = true
		if err := LocalAddrsProblem(); err != nil {
			m.log.Warn("announcing no endpoints: cannot list this device's own addresses",
				"err", err,
				"effect", "peers can see this node but cannot dial it",
				"fix", "it needs a peer that can reach it first — a relay, or a node with a public address")
		} else {
			m.log.Warn("announcing no endpoints: this node has no address worth giving",
				"effect", "peers can see this node but cannot dial it",
				"fix", "it learns one from the first peer that reaches it; with no relay and no reachable peer, nothing can")
		}
	} else if len(a.Endpoints) > 0 {
		m.noEndpoints = false
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
// history, the announce-reply debounce, the endpoint-roam counters, the
// per-peer service lists and the credential expiries. A peer that never
// returns stayed in all of them until the process restarted.
//
// The last three were missed when this was written, and services was the
// expensive one: it takes a signed list from any holder of the network key, so
// on a bearer mesh it grew with every stranger who published one.
//
// Ordered deliberately: the roster is the authority on who exists, so it
// decides who is gone and everything else follows. Doing it the other way
// leaves state for a peer the roster still lists.
//
// Runs on the probe ticker, so anything also touched from the receive path is
// reclaimed under the lock that guards it there.
func (m *Mesh) pruneForgotten(now time.Time) {
	// Revocations whose credentials have provably expired, which only version 2
	// revocations can say. Done here rather than on its own timer because it is
	// the same kind of work, and it must persist: a list that shrank in memory
	// and not on disk would grow back at the next restart.
	if n := m.revoked.Prune(now); n > 0 {
		m.log.Info("forgot revocations whose credentials expired anyway", "count", n)
		m.saveRevocations()
	}

	gone := m.roster.Prune(now)
	if len(gone) == 0 {
		return
	}
	m.forget(gone)
	m.log.Info("forgot peers not seen recently", "count", len(gone), "after", ForgetAfter)

	// The data plane still holds them as WireGuard peers until the next sync.
	m.requestResync()
}

// forget drops the per-peer state, each map under whichever lock guards it.
//
// Split out of pruneForgotten so it can be tested directly: the reclamation is
// where the locking lives, and the roster call above is not the part that needs
// proving.
func (m *Mesh) forget(gone []string) {
	for _, id := range gone {
		m.guard.Forget(mustHexBytes(id))
		m.prober.Forget(id)
		m.timing.forget(id)
		m.rates.forget(id)
		// roams is only ever touched from the main loop — syncPeers and this
		// function — so it needs no lock. The two below do.
		delete(m.roams, id)
	}

	// repliedTo is written on the receive path under replyMu, and this runs on
	// the probe ticker. The delete used to be here without the lock: two
	// goroutines writing one map, which Go answers with a fatal crash rather
	// than a corrupted read. Rare — it needs a peer to age out while announces
	// are being processed — and rare concurrent map writes are the ones that
	// take a year to reproduce.
	m.replyMu.Lock()
	for _, id := range gone {
		delete(m.repliedTo, id)
	}
	m.replyMu.Unlock()

	// services and expiry are guarded by m.mu and were never reclaimed at all.
	// services is the one that matters: it accepts a signed list from any
	// holder of the network key, so on a bearer mesh it grows with every
	// stranger who publishes one and nothing ever took an entry out again.
	m.mu.Lock()
	for _, id := range gone {
		delete(m.services, id)
		delete(m.expiry, id)
		delete(m.expiredDropped, id)
	}
	m.mu.Unlock()
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

// localAddrsErr records why enumeration failed, for the one place that reports
// it. Not a log call here: this runs every announce, and a permission error
// does not stop being true.
// Wrapped in a struct because atomic.Value panics on a nil interface, and
// "no problem" is the common case that has to be storable.
type addrsProblem struct{ err error }

var localAddrsErr atomic.Value // addrsProblem

// LocalAddrsProblem returns the last reason this node could not list its own
// addresses, or nil.
//
// Worth surfacing because the failure is silent and total. Android restricts
// netlink for untrusted apps, so net.Interfaces can return "permission denied"
// on a phone — and a node that cannot see its own addresses announces none,
// which peers experience as a device that is online, has no endpoint, and can
// never be dialled. That reads as a broken network rather than a blocked
// syscall.
func LocalAddrsProblem() error {
	if v, ok := localAddrsErr.Load().(addrsProblem); ok {
		return v.err
	}
	return nil
}

func localAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		localAddrsErr.Store(addrsProblem{err})
		return nil
	}
	localAddrsErr.Store(addrsProblem{})
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
			// And the synthetic IPv4 ones (ADR-021), for the same reason. These
			// live on the tunnel interface, so they were being announced as
			// candidate endpoints — an address reachable only through the
			// tunnel it is meant to establish.
			if v4.Prefix.Contains(ip) {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}

// Deliver hands this mesh one event from a shared rendezvous node.
//
// The node's event channel has one consumer, so a daemon running several
// meshes must read it once and pass every event to all of them: each tries to
// open it and ignores what is not addressed to it. That is the trial
// decryption ADR-015 accepts as the cost of no cleartext mesh identifier —
// and, done wrong, the reason a two-mesh node sees roughly half the traffic it
// should.
func (m *Mesh) Deliver(ev waku.Event) { m.handle(ev) }

// SetFed says this mesh is driven by Deliver rather than by reading the node
// itself. Must be set before Run.
func (m *Mesh) SetFed() { m.fed = true }

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

	// An enrolment in progress, on a topic derived from a token rather than
	// from the network key. Checked first and cheaply: normally there is no
	// invite open and this is a map lookup on a topic that never matches.
	if m.handleInvite(msg.ContentTopic, msg.Payload, now) {
		return
	}

	// Try every epoch in the window: a peer whose clock differs will have
	// sealed under a neighbouring key.
	e := topic.Epoch(now)
	a, err := control.OpenAnnounceWindow(m.nk, []int64{e - 1, e, e + 1}, msg.Payload, now)
	if err != nil {
		// A revocation is sealed the same way and published to the same topic,
		// so it lands here too. Tried second because announces are almost all
		// the traffic.
		for _, ep := range []int64{e - 1, e, e + 1} {
			if r, rerr := control.OpenRevoke(m.nk, ep, msg.Payload, now); rerr == nil {
				m.health.announceOpened(now)
				m.handleRevocation(r.Payload, now)
				return
			}
			if g, gerr := control.OpenGrant(m.nk, ep, msg.Payload, now); gerr == nil {
				m.health.announceOpened(now)
				m.handleGrant(g.Payload, now)
				return
			}
			if sv, serr := control.OpenServices(m.nk, ep, msg.Payload, now); serr == nil {
				m.health.announceOpened(now)
				m.handleServices(sv, now)
				return
			}
		}
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

	// Remember where this peer says its rendezvous node can be reached, for
	// our next start (ADR-031). Written on the receive path because that is
	// where the announce is, and cheap: the common case is an address we
	// already hold, which is one timestamp refresh.
	//
	// After checkMembership above, so only a peer this mesh admits can put an
	// address in our bootstrap list. That is the whole of the trust here: a
	// member could still point us at a node that feeds selectively, which is
	// why the configured addresses are never displaced by learned ones.
	if a.Boot != "" {
		if err := m.st.NoteBootPeer(a.Boot, now); err != nil {
			m.log.Debug("could not record a peer's bootstrap address", "err", err)
		}
	}
	if m.timing.mark(peer.ID(), func(x *Milestones) *time.Time { return &x.Discovered }, now) {
		m.log.Info("peer discovered", "peer", peer.Name,
			"after", m.Timing(peer.ID()).DiscoveredAfter.Round(time.Millisecond))
		// Tell it what we offer. It has just arrived and the list goes out
		// every five minutes, so without this it sees nothing for most of
		// that — which is every reconnect (ADR-023).
		m.offerServices(now)
		// And tell it what has been withdrawn, for the same reason and a
		// worse consequence: a node that joined after a revocation was
		// published would otherwise admit that device until its credential
		// expired, which is up to a month.
		m.revocationsOnDiscovery(now)
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

	// And into the IPv4 aliases (ADR-021), here rather than on its own timer:
	// this is already the one place that runs when the roster changes, and an
	// alias that lagged the roster would answer DNS for a peer the data plane
	// has not been told about.
	if m.v4 != nil {
		entries := make([]v4.Entry, 0, len(peers))
		for _, p := range peers {
			entries = append(entries, v4.Entry{Overlay: p.Overlay, DevicePub: p.DevicePub})
		}
		m.v4.Update(entries)
	}
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

		// An expired credential ends access now, not in six hours.
		//
		// Expiry used to stop the roster being *updated* and nothing more: the
		// peer's later announces were refused, but its WireGuard peer stayed
		// configured while its tunnel was live, and a 25-second keepalive keeps
		// a tunnel live indefinitely. So the device was dropped only when it
		// aged out of the roster at ForgetAfter — up to six hours of full mesh
		// access on a credential that had run out. Only explicit revocation
		// tore anything down promptly, and revocation is the mechanism that can
		// be suppressed; expiry is the one SECURITY.md leans on because it
		// cannot be.
		if m.expired(p.ID(), now) {
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
			// The key and both handles come from the relay actually chosen: a
			// member sees real tunnel keys, a stranger sees tags.
			t, ok := m.targetFor(rl.addr)
			if !ok {
				t = relayTarget{addr: rl.addr, key: m.relayKey}
			}
			peer.RelayVia = wg.NewRelayEndpoint(t.key, rl.addr,
				m.handleFor(t, m.st.Identity.WGPub), m.handleFor(t, p.WGPub))
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
			if m.yieldRoam(p.ID(), p.Name, now) {
				// Stop fighting and keep what the device learned. See
				// yieldRoam: pulling it back every few seconds achieved
				// nothing except a rewrite every few seconds.
				peer.Endpoint = ""
				peer.KeepEndpoint = true
			} else {
				drifted = true
			}
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
	m.setMSSLimits(out)
	m.refreshHosts()
	return nil
}

// setMSSLimits tells the tun which peers need smaller TCP segments.
//
// A relayed packet does not fit a path a direct one does
// (docs/relay-mtu.md), and neither lowering the interface MTU nor path MTU
// discovery can express that: the overlay is IPv6, 1280 is the floor for both,
// and a relayed packet needs less than the floor allows. Clamping the segment
// size can, because a segment is not an IP MTU and has no minimum.
//
// Applied per peer rather than per interface, because which peers are relayed
// changes at runtime — failing over is an endpoint swap, with no handshake to
// renegotiate at. A peer on the LAN keeps full-size segments throughout.
func (m *Mesh) setMSSLimits(peers []wg.Peer) {
	relayed := make(map[netip.Addr]bool, len(peers))
	for _, p := range peers {
		if p.RelayVia != nil {
			relayed[p.AllowedIP] = true
		}
	}
	m.dev.SetMSSLimit(func(dst netip.Addr) uint16 {
		if relayed[dst] {
			return wg.RelayedMSS
		}
		return 0
	})
}

// RoamFights is how many times an endpoint may be pulled back before this node
// accepts where WireGuard has roamed it to.
const RoamFights = 3

// RoamTruce is how long to leave a peer's endpoint alone after conceding.
const RoamTruce = 2 * time.Minute

// yieldRoam decides whether to keep correcting an endpoint or accept the one
// the device learned, and reports true when it is time to stop.
//
// Correcting drift is right when it is a one-off: WireGuard roams a peer to the
// source of any authenticated packet, so a single relayed reply can move a
// tunnel onto a relay and leave it there, which is the bug the drift check was
// written for.
//
// It is wrong when the peer's source address genuinely keeps changing. A phone
// behind carrier NAT is given a different source port often enough that the
// address we probed and the address its packets arrive from never agree, so
// every sync saw drift, rewrote the endpoint, and the next packet moved it
// again — a rewrite every few seconds, for hours, visible in the log as an
// unexplained repair loop on a mesh where nothing was wrong.
//
// After a few attempts this concedes. What the device learned is not a guess:
// WireGuard only roams to an address that sent a packet it could authenticate,
// which is stronger evidence than a probe reply. The truce expires, so a peer
// that settles is corrected again if it ever needs to be.
func (m *Mesh) yieldRoam(id, name string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.roams == nil {
		m.roams = map[string]roamFight{}
	}
	f := m.roams[id]
	if now.Before(f.until) {
		return true
	}
	// A peer that stopped drifting starts again from zero, so an occasional
	// correction never accumulates into a concession.
	if !f.last.IsZero() && now.Sub(f.last) > RoamTruce {
		f.count = 0
	}
	f.count++
	f.last = now
	if f.count < RoamFights {
		m.roams[id] = f
		return false
	}
	f.count = 0
	f.until = now.Add(RoamTruce)
	m.roams[id] = f
	m.log.Info("keeping the endpoint WireGuard learned; ours kept being overwritten",
		"peer", name, "for", RoamTruce)
	return true
}

// roamFight is how often one peer's endpoint has been corrected lately.
type roamFight struct {
	count int
	last  time.Time
	until time.Time
}

// hostEntries is what the managed /etc/hosts block should contain.
//
// Split out from refreshHosts, which writes to a fixed path and so cannot be
// tested. That is not a hypothetical concern: there are two writers of this
// block — this one and `shrooms hosts` — and when the IPv4 aliases were added
// only the command got them, so the daemon quietly kept overwriting the file
// with IPv6-only entries. Building the entries in one testable place is what
// stops the two drifting again.
func (m *Mesh) hostEntries() []hosts.Entry {
	// Both families, because /etc/hosts is what most software actually
	// resolves through — systemd-resolved answers from it synthetically, ahead
	// of the mesh resolver — so a block with only overlay addresses hides the
	// IPv4 aliases (ADR-021) from everything that cannot use IPv6. The resolver
	// has always served both; nothing was asking it.
	entries := []hosts.Entry{{
		Name: m.cfg.Name, Addr: m.self.String(), AddrV4: m.aliasOf(m.self), Self: true,
	}}
	for _, p := range m.roster.Peers() {
		entries = append(entries, hosts.Entry{
			Name: p.Name, Addr: p.Overlay.String(), AddrV4: m.aliasOf(p.Overlay),
		})
	}
	return entries
}

// aliasOf is a peer's synthetic IPv4 address, or empty when it has none.
func (m *Mesh) aliasOf(overlay netip.Addr) string {
	if a, ok := m.LookupV4(overlay); ok {
		return a.String()
	}
	return ""
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

	entries := m.hostEntries()

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
