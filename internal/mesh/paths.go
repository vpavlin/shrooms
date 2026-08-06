package mesh

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/vpavlin/logos-vpn/internal/disco"
)

// handleControl dispatches a packet that arrived on the shared WireGuard socket
// carrying our magic prefix.
//
// Called on the receive path, so it must not block. Everything here is a
// bounded map operation plus at most one send.
func (m *Mesh) handleControl(payload []byte, ep conn.Endpoint) {
	from, err := endpointAddrPort(ep)
	if err != nil {
		return
	}

	msg, err := disco.Decode(m.discoKey, payload)
	if err != nil {
		// Unauthenticated or malformed. Expected if something else on the
		// network happens to hit our port; not worth logging per packet.
		return
	}

	switch msg.Type {
	case disco.TypePing:
		m.prober.HandlePing(msg, from)

	case disco.TypePong:
		peerID, ok := m.prober.HandlePong(msg, from, time.Now())
		if !ok {
			return
		}
		// A newly usable path may be better than what WireGuard is using, so
		// re-evaluate. syncPeers is cheap and idempotent.
		if err := m.syncPeers(); err != nil {
			m.log.Warn("failed to apply discovered path", "peer", peerID[:8], "err", err)
		}
	}
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
	return m.dev.Bind.SendControl(pkt, ep)
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
		m.prober.Probe(p.ID(), parseCandidates(p.Endpoints), now)
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
