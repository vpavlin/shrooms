package mesh

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/relay"
	"github.com/vpavlin/shrooms/internal/wg"
)

// handleControl dispatches a packet that arrived on the shared WireGuard socket
// carrying our magic prefix.
//
// Called on the receive path, so it must not block. Everything here is a
// bounded map operation plus at most one send.
func (m *Mesh) handleControl(sub wg.Sub, payload []byte, ep conn.Endpoint) ([]byte, conn.Endpoint, bool) {
	from, err := endpointAddrPort(ep)
	if err != nil {
		return nil, nil, false
	}

	if sub == wg.SubRelay {
		return m.handleRelayFrame(payload, from)
	}
	if sub != wg.SubDisco {
		return nil, nil, false
	}

	msg, err := disco.Decode(m.discoKey, payload)
	if err != nil {
		m.log.Debug("undecodable control packet", "from", from, "bytes", len(payload), "err", err)
		return nil, nil, false
	}

	switch msg.Type {
	case disco.TypePing:
		m.log.Debug("ping received", "from", from)
		m.prober.HandlePing(msg, from)

	case disco.TypePong:
		// The endpoint in use before this pong is recorded, so the resync below
		// happens only if it actually moves. The sender's identity is on the
		// message and is verified inside HandlePong, so it is safe to key on
		// here — a pong that fails that check simply yields ok=false.
		now := time.Now()
		sender := hex.EncodeToString(msg.SenderPub[:])
		before, _ := m.prober.Best(sender, now)

		peerID, ok := m.prober.HandlePong(msg, from, now)
		if !ok {
			m.log.Debug("pong for an unknown probe", "from", from)
			return nil, nil, false
		}
		if m.timing.mark(peerID, func(x *Milestones) *time.Time { return &x.PathConfirmed }, now) {
			m.log.Info("path confirmed", "peer", peerID[:8], "via", from,
				"observed_us_at", msg.Observed,
				"after", m.Timing(peerID).PathAfter.Round(time.Millisecond))
		} else {
			m.log.Debug("path confirmed", "peer", peerID[:8], "via", from, "observed_us_at", msg.Observed)
		}
		// Only when this actually changed the endpoint in use. Every pong used
		// to request a resync, and since a confirmed path is re-probed every
		// PathRefresh, that was a full roster rebuild plus a UAPI dump every
		// few seconds per peer, forever, to discover nothing had changed.
		//
		// Doing the work here rather than in the main loop would block the
		// receive path, so this only decides whether to ask.
		if best, ok := m.prober.Best(peerID, now); ok && best.Addr != before.Addr {
			m.requestResync()
		}
	}
	return nil, nil, false
}

// handleRelayFrame processes relayed traffic.
//
// Two roles share this path. If we are acting as a relay, we forward. If we are
// a client, an inbound frame is unwrapped and handed back to the caller so it
// can be injected into the batch as though WireGuard had received it directly.
func (m *Mesh) handleRelayFrame(payload []byte, from netip.AddrPort) ([]byte, conn.Endpoint, bool) {
	if m.relaySrv != nil {
		// Relay role: forward and consume.
		out, to, ok := m.relaySrv.Handle(payload, from, time.Now())
		if ok {
			if ep, err := m.dev.Bind.ParseEndpoint(to.String()); err == nil {
				_ = m.dev.Bind.SendControl(wg.SubRelay, out, ep)
			}
		}
		return nil, nil, false
	}

	// Client role: unwrap and hand to WireGuard.
	f, err := relay.Decode(m.relayKey, payload)
	if err != nil || f.Type != relay.TypeForward {
		return nil, nil, false
	}
	ep := wg.NewRelayEndpoint(m.relayKey, from, m.st.Identity.WGPub, f.Src)
	return f.Payload, ep, true
}

// endpointAddrPort extracts the source address from a wireguard-go endpoint.
func endpointAddrPort(ep conn.Endpoint) (netip.AddrPort, error) {
	if ep == nil {
		return netip.AddrPort{}, fmt.Errorf("nil endpoint")
	}
	return netip.ParseAddrPort(ep.DstToString())
}

// sendDisco delivers a disco packet to a raw address over the shared socket.
func (m *Mesh) sendDisco(pkt []byte, to netip.AddrPort) error {
	ep, err := m.dev.Bind.ParseEndpoint(to.String())
	if err != nil {
		return fmt.Errorf("parse endpoint %s: %w", to, err)
	}
	if err := m.dev.Bind.SendControl(wg.SubDisco, pkt, ep); err != nil {
		// A probe that never leaves the host looks identical to one dropped in
		// transit, so this is worth surfacing.
		m.log.Debug("disco send failed", "to", to, "err", err)
		return err
	}
	return nil
}

// relayChoice is the relay this node routes through, if any.
type relayChoice struct {
	ok   bool
	id   string // hex device pub; empty when pinned by config
	addr netip.AddrPort
}

// selectRelay picks which relay to route through when no direct path exists.
//
// Relaying only works if BOTH ends pick the same relay: the relay forwards by
// destination WireGuard key and can only do so for a peer that has registered
// with it. So this must be a pure function of state both ends share. Lowest
// device ID wins — deliberately not lowest RTT, which each side measures
// differently and would therefore disagree on.
func (m *Mesh) selectRelay(now time.Time) relayChoice {
	// An explicit relay_addr overrides discovery.
	if m.relayPin.IsValid() {
		return relayChoice{ok: true, addr: m.relayPin}
	}
	// A relay is publicly reachable by definition, so it has no use for one.
	if m.relaySrv != nil {
		return relayChoice{}
	}

	var best relayChoice
	for _, p := range m.roster.Peers() {
		if !p.Relay || !p.Online(now) {
			continue
		}
		// Only ever use a probed address. An unverified candidate would
		// blackhole every relayed packet with nothing to show for it, and
		// unlike a direct endpoint there is no second chance: WireGuard cannot
		// relearn a relay path from an inbound packet.
		path, ok := m.prober.Best(p.ID(), now)
		if !ok {
			continue
		}
		if !best.ok || p.ID() < best.id {
			best = relayChoice{ok: true, id: p.ID(), addr: path.Addr}
		}
	}
	return best
}

// RelayRefresh is how often a relay registration is renewed.
//
// Half the relay's RegistrationTTL, which is the standard way to refresh soft
// state: late enough to be cheap, early enough that one lost packet does not
// expire the mapping. It used to go out on every probe tick — every 3s against
// a 2 minute TTL, about 40x more often than the mapping needed, forever, even
// when every peer had a direct path and the relay carried nothing.
const RelayRefresh = relay.RegistrationTTL / 2

// registerWithRelay tells our relay where to reach us.
//
// The relay's mapping is soft state with a TTL, and a NAT rebinding invalidates
// our address without either side noticing, so it is renewed rather than sent
// once. A changed relay re-registers immediately: waiting out the refresh
// interval there would leave us unreachable through it for up to a minute.
func (m *Mesh) registerWithRelay() {
	now := time.Now()
	rl := m.selectRelay(now)
	if !rl.ok {
		if m.relayNow.IsValid() {
			m.log.Info("no relay available", "was", m.relayNow)
			m.relayNow = netip.AddrPort{}
		}
		return
	}
	changed := rl.addr != m.relayNow
	if changed {
		m.log.Info("using relay", "addr", rl.addr, "discovered", rl.id != "")
		m.relayNow = rl.addr
		// The set of usable endpoints just changed, so the data plane is stale.
		m.requestResync()
	}
	if !changed && now.Sub(m.relayRegistered) < RelayRefresh {
		return
	}
	m.relayRegistered = now

	ep, err := m.dev.Bind.ParseEndpoint(rl.addr.String())
	if err != nil {
		return
	}
	frame := relay.EncodeRegister(m.relayKey, m.st.Identity.WGPub)
	if err := m.dev.Bind.SendControl(wg.SubRelay, frame, ep); err != nil {
		m.log.Debug("relay registration failed", "relay", rl.addr, "err", err)
	}
}

// probeAll probes every known peer's candidates.
//
// Peers whose best path is not yet due for renewal are skipped. Probing is not
// only for finding a path: a path that is never re-probed expires, and the peer
// then has no usable endpoint until the next probe completes. See PathRefresh.
func (m *Mesh) probeAll(now time.Time) {
	// Refreshed each round: this machine's addresses change when it moves
	// network, and the prober uses them to recognise a candidate that is
	// really us.
	m.prober.SetSelfAddrs(localAddrs())

	for _, p := range m.roster.Peers() {
		if !p.Online(now) {
			continue
		}
		if !m.prober.NeedsProbe(p.ID(), now) {
			continue
		}
		cands := parseCandidates(p.Endpoints)
		m.log.Debug("probing", "peer", p.Name, "candidates", p.Endpoints, "parsed", len(cands))
		m.prober.Probe(p.ID(), cands, now)
	}
}

// parseCandidates converts announced endpoint strings to addresses.
func parseCandidates(eps []string) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(eps))
	for _, e := range eps {
		if ap, err := netip.ParseAddrPort(e); err == nil {
			out = append(out, ap)
		}
	}
	return out
}

// SetMapped records an address the router handed us (ADR-024), or clears it
// when passed the zero value.
//
// Kept separate from cfg.Advertise even though both end up in the same list,
// because they answer to different things: advertise is what a person
// configured and stays until they change it, this is a lease that expires and
// may come back different after the router reboots.
func (m *Mesh) SetMapped(ap netip.AddrPort) {
	m.mu.Lock()
	m.mapped = ap
	m.mu.Unlock()
	// Say so now rather than at the next tick. The whole value of a mapping is
	// that peers can dial it, and they cannot until they have been told.
	m.requestAnnounce()
}

// candidates returns the endpoints we advertise, most useful first.
//
// Order matters: reflexive addresses (what peers actually observed) come first
// because they are the only ones that work from outside a NAT. A port mapping
// from the router follows — it is a claim rather than an observation, but it is
// a claim about the outside, which is more than a LAN address ever is, and on a
// mesh with no publicly reachable member it is the only such claim available.
// Configured advertise entries follow, then local addresses, which help peers
// on the same LAN and are harmless elsewhere.
func (m *Mesh) candidates() []string {
	seen := map[string]bool{}
	var out []string

	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	for _, ap := range m.prober.Reflexive(time.Now()) {
		add(ap.String())
	}
	m.mu.Lock()
	mapped := m.mapped
	m.mu.Unlock()
	if mapped.IsValid() {
		add(mapped.String())
	}
	for _, a := range m.cfg.Advertise {
		add(a)
	}
	for _, ip := range localAddrs() {
		add(net.JoinHostPort(ip.String(), strconv.Itoa(int(m.cfg.ListenPort))))
	}

	// Keep the announce inside its fixed padding. Four is the real ceiling for
	// a typical name; announceWith trims further if a longer one needs it, so
	// this is a cheap first cut rather than the guarantee.
	out = reserveLocal(out)
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// reserveLocal makes sure a LAN address survives truncation.
//
// Both cuts above drop from the end, and local addresses are ordered last, so
// on a node with several reflexive addresses the LAN address is the first thing
// discarded. That is exactly backwards: a node with many reflexive addresses is
// behind endpoint-dependent NAT, which is the case where its public addresses
// are least likely to work — and it takes the LAN address away from peers in
// the same building, who then reach it by hairpinning through the router, or
// through a relay on another continent, for traffic that never had to leave the
// house.
//
// So one place is held for the best private address, at index 1 rather than 0:
// a peer that cannot reach us at all is worse off than one paying for a
// hairpin, so the first slot stays with an address that works from outside.
func reserveLocal(in []string) []string {
	for i, s := range in {
		if !isPrivate(s) {
			continue
		}
		if i < 2 {
			return in // already safe
		}
		out := make([]string, 0, len(in))
		out = append(out, in[0], s)
		out = append(out, in[1:i]...)
		return append(out, in[i+1:]...)
	}
	return in
}

// isPrivate reports whether an announced endpoint is on a private network.
//
// Classified by the address rather than by where it came from, because a peer
// on the same LAN observes us at a private address and reports it — so a
// reflexive address is not necessarily an outside one.
func isPrivate(s string) bool {
	ap, err := netip.ParseAddrPort(s)
	return err == nil && ap.Addr().IsPrivate()
}
