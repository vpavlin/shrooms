package disco

import (
	"encoding/hex"
	"net/netip"
	"sync"
	"time"
)

const (
	// ProbeInterval is how often we re-probe a peer's candidates while it has
	// no working path. Aggressive while searching, silent once settled.
	ProbeInterval = 3 * time.Second

	// PathFresh is how long a working path is trusted without a new pong.
	// Tailscale uses 6.5s for the equivalent; the NAT mapping lifetimes that
	// matter are 35-65s, so this is comfortably inside them.
	PathFresh = 15 * time.Second

	// PathRefresh is when a working path is re-probed. It must be comfortably
	// less than PathFresh.
	//
	// Refreshing only once a path has already expired leaves a gap, every
	// PathFresh, in which the peer has no usable path at all — it lasts until
	// the next probe's pong returns, which on a real link is a round trip and
	// not the ~0 it is in a container. During that gap endpoint selection falls
	// back, and a node using a relay drops it and re-acquires it seconds later.
	// Refreshing early means a path is renewed while it is still good, so the
	// state never lapses.
	PathRefresh = 5 * time.Second

	// ProbeTimeout discards an unanswered probe.
	ProbeTimeout = 10 * time.Second

	// SwitchMargin is how much better a challenger path must be before we move
	// to it, when the one in use is still working.
	//
	// Without it, two paths to the same peer with near-identical RTT trade
	// places on every probe — observed alternating every 3s between a host's
	// two bridge addresses. Each flip rewrites the peer's endpoint in
	// WireGuard, and endpoint churn is the most expensive kind of bug this
	// project has: a swap mid-handshake costs a full retry.
	//
	// Large enough to ignore jitter on a LAN, small enough that a genuinely
	// better route still wins.
	SwitchMargin = 15 * time.Millisecond

	// MaxReflexive caps how many distinct self-addresses we remember. Under
	// endpoint-dependent (symmetric) NAT every peer observes a different port,
	// so this set grows with peers rather than converging — which is itself the
	// signal that punching will not work.
	MaxReflexive = 16
)

// probe is an outstanding ping.
type probe struct {
	peerID string
	addr   netip.AddrPort
	sentAt time.Time
}

// Path is a candidate endpoint's state for one peer.
type Path struct {
	Addr     netip.AddrPort
	RTT      time.Duration
	LastPong time.Time
}

// Fresh reports whether the path has answered recently enough to trust.
func (p Path) Fresh(now time.Time) bool {
	return !p.LastPong.IsZero() && now.Sub(p.LastPong) < PathFresh
}

// Prober drives disco probing: it sprays pings at a peer's candidates, records
// which answer, and reports the best one.
//
// It never picks an endpoint from an announce alone. A candidate is only
// usable once it has answered a probe, which is what distinguishes "the peer
// says it is at this address" from "packets actually reach the peer there".
type Prober struct {
	key     Key
	selfPub []byte

	// send delivers a disco packet to a raw address over the shared socket.
	send func(pkt []byte, to netip.AddrPort) error

	mu        sync.Mutex
	pending   map[TxID]probe
	paths     map[string]map[netip.AddrPort]*Path // peerID -> addr -> path
	reflexive map[netip.AddrPort]time.Time        // self-addresses peers reported

	// selected is the path currently in use per peer, so Best can keep it
	// rather than re-racing near-equal candidates on every call.
	selected map[string]netip.AddrPort

	// selfAddrs are this machine's own addresses, so a candidate that is
	// really us can be recognised. Set by SetSelfAddrs as interfaces change.
	selfAddrs map[netip.Addr]bool
}

// NewProber returns a prober that sends via the given function.
func NewProber(key Key, selfPub []byte, send func([]byte, netip.AddrPort) error) *Prober {
	return &Prober{
		key:       key,
		selfPub:   selfPub,
		send:      send,
		pending:   make(map[TxID]probe),
		paths:     make(map[string]map[netip.AddrPort]*Path),
		selected:  make(map[string]netip.AddrPort),
		reflexive: make(map[netip.AddrPort]time.Time),
		selfAddrs: make(map[netip.Addr]bool),
	}
}

// SetSelfAddrs tells the prober which addresses belong to this machine.
//
// Kept current rather than sampled once: a laptop changes address whenever it
// moves network, and a stale set would let the case this exists for back in.
func (p *Prober) SetSelfAddrs(addrs []netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.selfAddrs = make(map[netip.Addr]bool, len(addrs))
	for _, a := range addrs {
		p.selfAddrs[a.Unmap()] = true
	}
}

// isSelf reports whether an address is one of ours.
func (p *Prober) isSelf(ap netip.AddrPort) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selfAddrs[ap.Addr().Unmap()]
}

// Probe sends a ping to every candidate for a peer.
//
// All candidates are probed every round rather than tried in sequence: a peer
// behind NAT may only be reachable on one of them, and which one is not
// predictable. This is also the punch — the outbound packet opens our NAT
// mapping so the peer's own probe can get back in.
func (p *Prober) Probe(peerID string, candidates []netip.AddrPort, now time.Time) {
	for _, addr := range candidates {
		if !addr.IsValid() {
			continue
		}
		// Never probe ourselves. A peer can announce an address we also hold —
		// after a DHCP lease moves between machines, for instance — and
		// probing it means talking to our own socket. HandlePong rejects the
		// reply on identity, so this is belt and braces, but it also stops us
		// sending traffic to ourselves at all.
		if p.isSelf(addr) {
			continue
		}
		tx, err := NewTxID()
		if err != nil {
			continue
		}
		pkt, err := EncodePing(p.key, p.selfPub, tx)
		if err != nil {
			continue
		}

		p.mu.Lock()
		p.pending[tx] = probe{peerID: peerID, addr: addr, sentAt: now}
		p.mu.Unlock()

		// A send failure is normal: a candidate may be an unroutable LAN
		// address from the peer's side of the world.
		_ = p.send(pkt, addr)
	}
	p.expire(now)
}

// HandlePing answers a probe.
//
// The reply goes to the address the packet ARRIVED FROM, never to anything the
// peer announced. Under endpoint-dependent mapping a peer that sprays at our
// several candidates creates a different external port per destination, so the
// only address that can reach it is the one we observed. Replying to an
// announced address silently breaks exactly the case punching exists for.
func (p *Prober) HandlePing(m *Message, from netip.AddrPort) {
	pkt, err := EncodePong(p.key, p.selfPub, m.TxID, from)
	if err != nil {
		return
	}
	_ = p.send(pkt, from)
}

// HandlePong records a working path and our reflexive address.
func (p *Prober) HandlePong(m *Message, from netip.AddrPort, now time.Time) (peerID string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pr, known := p.pending[m.TxID]
	if !known {
		// Unsolicited or already expired. Not an error — a late pong for a
		// probe we gave up on looks exactly like this.
		return "", false
	}

	// The pong must come from the device we probed.
	//
	// Matching on the transaction id alone is not enough, and the way it fails
	// is spectacular: probe one of our OWN addresses and our own responder
	// answers, in about a millisecond. That beats every real path, so it is
	// selected, and WireGuard then sends the peer's handshakes to this machine
	// — which reports them as "Received invalid initiation" and never connects.
	// Observed in the field: a peer sat at "no handshake" with 32 KB sent and
	// nothing received, its selected path a 1ms route to ourselves.
	//
	// SECURITY.md notes that disco authenticates mesh membership rather than
	// device identity. This closes that for path selection specifically: a
	// member can no longer answer for another member, by accident or design.
	if hex.EncodeToString(m.SenderPub[:]) != pr.peerID {
		return "", false
	}
	delete(p.pending, m.TxID)

	// Record where the peer saw us. This is reflexive discovery: with several
	// peers we get several independent vantage points and need no STUN server.
	if m.Observed.IsValid() {
		if len(p.reflexive) < MaxReflexive || !p.reflexive[m.Observed].IsZero() {
			p.reflexive[m.Observed] = now
		}
	}

	// Credit the address the pong arrived from, which may differ from the one
	// we probed if the peer is behind a NAT that rewrote it.
	byAddr := p.paths[pr.peerID]
	if byAddr == nil {
		byAddr = make(map[netip.AddrPort]*Path)
		p.paths[pr.peerID] = byAddr
	}
	path := byAddr[from]
	if path == nil {
		path = &Path{Addr: from}
		byAddr[from] = path
	}
	path.RTT = now.Sub(pr.sentAt)
	path.LastPong = now

	return pr.peerID, true
}

// NeedsProbe reports whether a peer should be probed now: either it has no
// working path, or its best one is due for renewal before it expires.
func (p *Prober) NeedsProbe(peerID string, now time.Time) bool {
	best, ok := p.Best(peerID, now)
	return !ok || now.Sub(best.LastPong) >= PathRefresh
}

// Best returns the preferred working path for a peer.
//
// Sticky: once a path is in use it is kept while it still works, unless a
// challenger is decisively better. Re-racing every call makes near-equal paths
// alternate, and every alternation rewrites WireGuard's endpoint for that peer.
func (p *Prober) Best(peerID string, now time.Time) (Path, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var cand Path
	found := false
	for _, path := range p.paths[peerID] {
		if !path.Fresh(now) {
			continue
		}
		if !found || better(*path, cand) {
			cand, found = *path, true
		}
	}
	if !found {
		delete(p.selected, peerID)
		return Path{}, false
	}

	// Stay where we are unless the challenger clearly beats it.
	if cur, ok := p.paths[peerID][p.selected[peerID]]; ok && cur.Fresh(now) {
		if !decisivelyBetter(cand, *cur) {
			return *cur, true
		}
	}
	p.selected[peerID] = cand.Addr
	return cand, true
}

// decisivelyBetter reports whether a challenger justifies abandoning a working
// path.
//
// A change of address family always does: IPv6 means no NAT to traverse, which
// is worth more than any latency difference. Within a family, the challenger
// must beat the incumbent by SwitchMargin rather than merely by a hair.
func decisivelyBetter(cand, cur Path) bool {
	c6 := cand.Addr.Addr().Is6() && !cand.Addr.Addr().Is4In6()
	u6 := cur.Addr.Addr().Is6() && !cur.Addr.Addr().Is4In6()
	if c6 != u6 {
		return c6
	}
	return cand.RTT+SwitchMargin < cur.RTT
}

// Paths returns every known path for a peer, for diagnostics.
func (p *Prober) Paths(peerID string) []Path {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Path, 0, len(p.paths[peerID]))
	for _, path := range p.paths[peerID] {
		out = append(out, *path)
	}
	return out
}

// Reflexive returns the self-addresses peers have reported, most recent first.
func (p *Prober) Reflexive(now time.Time) []netip.AddrPort {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]netip.AddrPort, 0, len(p.reflexive))
	for addr, seen := range p.reflexive {
		if now.Sub(seen) < 10*time.Minute {
			out = append(out, addr)
		}
	}
	return out
}

// Forget drops a peer's path state, e.g. on revocation.
func (p *Prober) Forget(peerID string) {
	p.mu.Lock()
	delete(p.paths, peerID)
	delete(p.selected, peerID)
	p.mu.Unlock()
}

// expire discards probes that were never answered.
func (p *Prober) expire(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for tx, pr := range p.pending {
		if now.Sub(pr.sentAt) > ProbeTimeout {
			delete(p.pending, tx)
		}
	}
}

// better ranks two working paths.
//
// Prefer IPv6: where both ends have global v6 there is no NAT at all, only
// filtering, which a single outbound packet solves. Otherwise prefer lower RTT.
// Tailscale scores this way for the same reason.
func better(a, b Path) bool {
	a6, b6 := a.Addr.Addr().Is6() && !a.Addr.Addr().Is4In6(), b.Addr.Addr().Is6() && !b.Addr.Addr().Is4In6()
	if a6 != b6 {
		return a6
	}
	return a.RTT < b.RTT
}
