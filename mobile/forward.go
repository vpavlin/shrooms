package mobile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
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
// servers is a comma-separated list from ConnectivityManager; it changes when
// the phone changes network, which is why it is passed in rather than probed.
func newForwarder(servers string, p Protector) (func([]byte) ([]byte, error), error) {
	var upstream []netip.AddrPort
	for _, s := range strings.Split(servers, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		addr, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		upstream = append(upstream, netip.AddrPortFrom(addr, 53))
	}
	if len(upstream) == 0 {
		return nil, errors.New("no upstream resolvers")
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

	return func(query []byte) ([]byte, error) {
		var lastErr error
		// Try each resolver in turn: on a phone the first is regularly a stale
		// entry from the network it just left.
		for _, up := range upstream {
			resp, err := exchange(dialer, up, query)
			if err == nil {
				return resp, nil
			}
			lastErr = err
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
