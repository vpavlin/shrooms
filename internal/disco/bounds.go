package disco

import (
	"net/netip"
	"time"
)

// Bounds on what a member can make this node send, and to where.
//
// Pings and pongs are signed by the device that sends them (ADR-029), so
// nobody outside a mesh can steer them. A member can: it announces whatever
// endpoints it likes, and every other member probes all of them, on every
// change and every round while it has no direct path. Announcing a stranger's
// address therefore made the whole mesh probe the stranger, and churning
// announces made it do so as fast as announces travel. A captured ping replayed
// from a spoofed source made us answer the spoofed address. Each is 1:1 in
// bytes; the cost is a victim's address being probed by machines it never
// heard of. The review of ADR-027 (laptop/reviewer, 2026-11-08) found both;
// these limits are its, set so nothing an honest peer does comes near them.
const (
	// MaxCandidates is how many of a peer's announced endpoints are probed. A
	// machine announces one address per interface it has; four or five is
	// usual, a host with several container bridges a few more.
	MaxCandidates = 8

	// ProbeFloor is the least time between two probe rounds for one peer. An
	// announce that changes triggers a round at once (that is the punch), and a
	// peer that changes its announce many times a second would otherwise make
	// as many rounds. ProbeInterval is 3 s, so this never delays an honest one.
	ProbeFloor = time.Second

	// PongBurst and PongEvery bound the pongs sent to one source address: a
	// burst of PongBurst, then one per PongEvery. An honest peer pings each of
	// our candidates from one socket every ProbeInterval: a handful per 3 s.
	PongBurst = 12
	PongEvery = 250 * time.Millisecond
)

// usableTarget reports whether an address may be probed at all: something a
// datagram could reach a peer at. Private and CGNAT addresses stay, because a
// peer on the same LAN or the same carrier network is reached there and that
// is the best path there is.
func usableTarget(ap netip.AddrPort) bool {
	if !ap.IsValid() || ap.Port() == 0 {
		return false
	}
	a := ap.Addr().Unmap()
	switch {
	case a.IsUnspecified(), a.IsLoopback(), a.IsMulticast(),
		a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(), a.IsInterfaceLocalMulticast():
		return false
	case a.Is4() && a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return false
	}
	return true
}

// Candidates is a peer's announced endpoints as probed: unusable ones dropped,
// duplicates removed, and at most MaxCandidates kept, globally routable ones
// first, so a peer with many private bridges keeps its public address.
func Candidates(announced []netip.AddrPort) []netip.AddrPort {
	seen := make(map[netip.AddrPort]bool, len(announced))
	var global, rest []netip.AddrPort
	for _, ap := range announced {
		if !usableTarget(ap) || seen[ap] {
			continue
		}
		seen[ap] = true
		if a := ap.Addr().Unmap(); a.IsGlobalUnicast() && !a.IsPrivate() && !isCGNAT(a) {
			global = append(global, ap)
		} else {
			rest = append(rest, ap)
		}
	}
	out := append(global, rest...)
	if len(out) > MaxCandidates {
		out = out[:MaxCandidates]
	}
	return out
}

func isCGNAT(a netip.Addr) bool {
	return a.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// pongBucket is the pongs one source address may still be sent.
type pongBucket struct {
	tokens float64
	at     time.Time
}

// allowPong takes a pong from from's bucket, refilled one per PongEvery up to
// PongBurst. Caller holds p.mu.
func (p *Prober) allowPong(from netip.AddrPort, now time.Time) bool {
	if p.pongs == nil {
		p.pongs = make(map[netip.AddrPort]*pongBucket)
	}
	b := p.pongs[from]
	if b == nil {
		// Forgetting idle sources keeps this from growing with every address
		// a ping ever came from.
		if len(p.pongs) > 4096 {
			for k, v := range p.pongs {
				if now.Sub(v.at) > time.Minute {
					delete(p.pongs, k)
				}
			}
		}
		b = &pongBucket{tokens: PongBurst, at: now}
		p.pongs[from] = b
	}
	b.tokens = min(PongBurst, b.tokens+float64(now.Sub(b.at))/float64(PongEvery))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
