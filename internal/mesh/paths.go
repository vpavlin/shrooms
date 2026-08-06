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
		m.log.Info("path confirmed", "peer", peerID[:8], "via", from, "observed_us_at", msg.Observed)
		// A newly usable path may be better than what WireGuard is using, so
		// re-evaluate. syncPeers is cheap and idempotent.
		if err := m.syncPeers(); err != nil {
			m.log.Warn("failed to apply discovered path", "peer", peerID[:8], "err", err)
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

// registerWithRelay tells our relay where to reach us.
//
// Re-sent on every probe tick rather than once: the relay's mapping is soft
// state with a TTL, and a NAT rebinding invalidates our address without either
// side noticing.
func (m *Mesh) registerWithRelay() {
	if !m.relayAddr.IsValid() || m.relaySrv != nil {
		return
	}
	ep, err := m.dev.Bind.ParseEndpoint(m.relayAddr.String())
	if err != nil {
		return
	}
	frame := relay.EncodeRegister(m.relayKey, m.st.Identity.WGPub)
	if err := m.dev.Bind.SendControl(wg.SubRelay, frame, ep); err != nil {
		m.log.Debug("relay registration failed", "relay", m.relayAddr, "err", err)
	}
}

// probeAll probes every known peer's candidates.
//
// Peers with a fresh working path are skipped: probing is for finding a path,
// and WireGuard's own keepalives maintain one once found.
func (m *Mesh) probeAll(now time.Time) {
	for _, p := range m.roster.Peers() {
		if !p.Online(now) {
			continue
		}
		if _, ok := m.prober.Best(p.ID(), now); ok {
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
