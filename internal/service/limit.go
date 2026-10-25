package service

import (
	"net"
	"net/netip"
	"sync"
)

// Connection caps for published services, all services of a device together,
// and per member. A service is somebody's own application, so these are wide:
// they are not a quota, only the end of "one member opens connections until
// the daemon runs out" — each one a goroutine pair in a root process, which
// the LAN gateway already capped (lanproxy.MaxConnsPerMember) and this did
// not (2026-10-25, prompted by the Tailscale SSH report's session churn).
const (
	MaxServiceConns          = 1024
	MaxServiceConnsPerMember = 128
)

// connLimit counts live connections, in total and by remote address.
type connLimit struct {
	mu      sync.Mutex
	total   int
	perPeer map[netip.Addr]int
	max     int
	maxPeer int
}

func newConnLimit(max, maxPeer int) *connLimit {
	return &connLimit{perPeer: map[netip.Addr]int{}, max: max, maxPeer: maxPeer}
}

// take reserves a connection for c's remote, or reports that it is over a cap.
func (l *connLimit) take(c net.Conn) (netip.Addr, bool) {
	from := remoteOf(c)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.max || l.perPeer[from] >= l.maxPeer {
		return from, false
	}
	l.total++
	l.perPeer[from]++
	return from, true
}

func (l *connLimit) give(from netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perPeer[from]--; l.perPeer[from] <= 0 {
		delete(l.perPeer, from)
	}
}

func remoteOf(c net.Conn) netip.Addr {
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}
