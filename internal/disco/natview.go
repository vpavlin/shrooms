package disco

import (
	"net/netip"
	"sort"
	"time"
)

// NATView is how this node's NAT maps its socket, as the peers that observed
// it tell (ADR-027's review asked for it: without it, "why is this pair
// relayed" has no answer short of reading endpoints).
type NATView struct {
	// Mapping: "independent" when two observers agree on an address (the NAT
	// maps the socket the same way for every destination, so a punch can
	// work); "dependent" when one public IP was seen with different ports by
	// different observers (symmetric: every destination gets its own port, and
	// only a relay helps unless the other side is forgiving); "unknown" with
	// fewer than two vantage points; "" with no observation at all.
	Mapping  string        `json:"mapping,omitempty"`
	Observed []Observation `json:"observed,omitempty"`
}

// Observation is one address peers saw this node at, and how many saw it.
type Observation struct {
	Addr string `json:"addr"`
	By   int    `json:"by"`
}

// NATView reads the raw observations, the ones Reflexive leaves out because
// peers disagree about them, which is the evidence for the verdict.
func (p *Prober) NATView(now time.Time) NATView {
	p.mu.Lock()
	defer p.mu.Unlock()
	var v NATView
	ports := map[netip.Addr]map[uint16]bool{} // public IP -> ports seen
	agreed := false
	for addr, e := range p.reflexive {
		n := 0
		for _, at := range e.by {
			if now.Sub(at) < reflexiveTTL {
				n++
			}
		}
		if n == 0 {
			continue
		}
		v.Observed = append(v.Observed, Observation{Addr: addr.String(), By: n})
		if n >= 2 {
			agreed = true
		}
		a := addr.Addr().Unmap()
		// A private address is a LAN peer's view of us, which says nothing
		// about the NAT between us and everybody else.
		if a.IsPrivate() || isCGNAT(a) {
			continue
		}
		if ports[a] == nil {
			ports[a] = map[uint16]bool{}
		}
		ports[a][addr.Port()] = true
	}
	sort.Slice(v.Observed, func(i, j int) bool {
		if v.Observed[i].By != v.Observed[j].By {
			return v.Observed[i].By > v.Observed[j].By
		}
		return v.Observed[i].Addr < v.Observed[j].Addr
	})
	dependent := false
	for _, ps := range ports {
		if len(ps) > 1 {
			dependent = true
		}
	}
	switch {
	case len(v.Observed) == 0:
	case dependent:
		v.Mapping = "dependent"
	case agreed:
		v.Mapping = "independent"
	default:
		// One vantage point, or several that saw different IPs (a LAN view
		// and a public one): nothing to compare.
		v.Mapping = "unknown"
	}
	return v
}

// HeardAt is where a peer's last probe came from and when: whether its probes
// reach this node at all, the other half of a pair that stays relayed.
func (p *Prober) HeardAt(peerID string) (netip.AddrPort, time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.heard[peerID]
	return a, p.heardAt[peerID], ok
}
