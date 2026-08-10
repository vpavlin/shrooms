package mobile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

// forwardTimeout bounds one upstream query. Short: a phone changing networks
// leaves stale resolvers behind, and waiting on one blocks the name the user is
// actually waiting for.
const forwardTimeout = 3 * time.Second

// newForwarder proxies queries we are not authoritative for to the underlying
// network's resolvers.
//
// Two things make this necessary rather than optional. Android has no
// split-DNS: a VpnService that sets a resolver receives every query the device
// makes, so refusing non-mesh names removes name resolution entirely. And the
// forwarding socket must be PROTECTED, or it routes into the tunnel we are
// resolving for and the query never leaves the device — the same trap as the
// WireGuard socket, in a place it is easy to forget.
//
// servers is a comma-separated list from ConnectivityManager. It changes when
// the phone changes network, so the forwarder holds it in a field that Set can
// replace rather than capturing it — which is what it used to do, and the bug
// that caused: after roaming from mobile data to wifi the list still named the
// carrier's resolvers, every non-mesh query spent forwardTimeout failing
// against each of them in turn, and Android concluded our resolver was dead and
// stopped querying it. Mesh names went with it, because they are served by the
// same resolver the platform had just given up on.
type forwarder struct {
	mu       sync.RWMutex
	upstream []netip.AddrPort
	dialer   *net.Dialer
}

// Set replaces the upstream resolvers.
//
// An empty or unparseable list is ignored rather than applied: a phone between
// networks reports no resolvers for a moment, and forwarding to nothing is
// worse than forwarding to something stale.
func (f *forwarder) Set(servers string) bool {
	next := parseServers(servers)
	if len(next) == 0 {
		return false
	}
	f.mu.Lock()
	f.upstream = next
	f.mu.Unlock()
	return true
}

func (f *forwarder) servers() []netip.AddrPort {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.upstream
}

func parseServers(servers string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, s := range strings.Split(servers, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		addr, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		out = append(out, netip.AddrPortFrom(addr, 53))
	}
	return out
}

func newForwarder(servers string, p Protector) (*forwarder, func([]byte) ([]byte, error), error) {
	upstream := parseServers(servers)
	if len(upstream) == 0 {
		return nil, nil, errors.New("no upstream resolvers")
	}

	dialer := &net.Dialer{
		Timeout: forwardTimeout,
		Control: func(_, _ string, c syscall.RawConn) error {
			var protectErr error
			err := c.Control(func(fd uintptr) {
				if !p.Protect(int(fd)) {
					protectErr = errors.New("VpnService.protect refused the forwarding socket")
				}
			})
			if err != nil {
				return err
			}
			return protectErr
		},
	}

	f := &forwarder{upstream: upstream, dialer: dialer}
	return f, func(query []byte) ([]byte, error) {
		var lastErr error
		// Read the list per query, not per connect. Try each in turn: on a
		// phone the first is regularly a stale entry from the network it just
		// left, and the list may have been replaced mid-flight.
		for _, up := range f.servers() {
			resp, err := exchange(f.dialer, up, query)
			if err == nil {
				return resp, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			return nil, errors.New("no upstream resolvers")
		}
		return nil, fmt.Errorf("all upstream resolvers failed: %w", lastErr)
	}, nil
}

func exchange(d *net.Dialer, up netip.AddrPort, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()

	conn, err := d.DialContext(ctx, "udp", up.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 1232)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
