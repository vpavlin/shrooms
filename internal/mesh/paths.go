package mesh

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/vpavlin/logos-vpn/internal/disco"
	"github.com/vpavlin/logos-vpn/internal/relay"
	"github.com/vpavlin/logos-vpn/internal/wg"
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
		peerID, ok := m.prober.HandlePong(msg, from, time.Now())
		if !ok {
			m.log.Debug("pong for an unknown probe", "from", from)
			return nil, nil, false
		}
		if m.timing.mark(peerID, func(x *Milestones) *time.Time { return &x.PathConfirmed }, time.Now()) {
			m.log.Info("path confirmed", "peer", peerID[:8], "via", from,
				"observed_us_at", msg.Observed,
				"after", m.Timing(peerID).PathAfter.Round(time.Millisecond))
		} else {
			m.log.Debug("path confirmed", "peer", peerID[:8], "via", from, "observed_us_at", msg.Observed)
		}
		// A newly usable path may be better than what WireGuard is using, so
		// ask the main loop to re-evaluate. Doing it here would block the
		// receive path.
		m.requestResync()
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

// registerWithRelay tells our relay where to reach us.
//
// Re-sent on every probe tick rather than once: the relay's mapping is soft
// state with a TTL, and a NAT rebinding invalidates our address without either
// side noticing.
func (m *Mesh) registerWithRelay() {
	rl := m.selectRelay(time.Now())
	if !rl.ok {
		if m.relayNow.IsValid() {
			m.log.Info("no relay available", "was", m.relayNow)
			m.relayNow = netip.AddrPort{}
		}
		return
	}
	if rl.addr != m.relayNow {
		m.log.Info("using relay", "addr", rl.addr, "discovered", rl.id != "")
		m.relayNow = rl.addr
		// The set of usable endpoints just changed, so the data plane is stale.
		m.requestResync()
	}

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

// candidates returns the endpoints we advertise, most useful first.
//
// Order matters: reflexive addresses (what peers actually observed) come first
// because they are the only ones that work from outside a NAT. Configured
// advertise entries follow, then local addresses, which help peers on the same
// LAN and are harmless elsewhere.
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
	for _, a := range m.cfg.Advertise {
		add(a)
	}
	for _, ip := range localAddrs() {
		add(net.JoinHostPort(ip.String(), strconv.Itoa(int(m.cfg.ListenPort))))
	}

	// Keep the announce inside its fixed padding.
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}
