package portmap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// procNetRoute is the kernel's IPv4 routing table. Reading it is the only way
// to learn the default gateway without either cgo or a netlink implementation,
// both of which this package exists to avoid.
const procNetRoute = "/proc/net/route"

// Linux route flags, from linux/route.h. Only these two matter here.
const (
	rtfUp      = 0x0001
	rtfGateway = 0x0002
)

// DiscoverGateway returns the address of the default IPv4 router.
//
// Two strategies, in order of how much they can be trusted.
//
// First the kernel routing table, on Linux via /proc/net/route. This is the
// real answer: the lowest-metric default route's next hop is by definition the
// device our packets leave through, and on a machine with several interfaces —
// a laptop on wifi with a container bridge up, which is the normal case for
// this project — it is the only strategy that picks the right one.
//
// If that is unavailable (any non-Linux platform, or a container with a masked
// /proc) we fall back to a guess. Connecting a UDP socket to a public address
// sends no packets at all; it only makes the kernel run its route lookup and
// bind the source address it would have used. That gives us our own address on
// the outbound interface, and we then assume the router is .1 on a /24. That
// assumption is wrong often enough that it must never be preferred to the
// routing table: it is here so that a home network with the overwhelmingly
// common 192.168.x.1 layout still gets a mapping, and a wrong guess costs one
// timeout and nothing else, since a device that is not a router will not answer
// on port 5351.
func DiscoverGateway() (netip.Addr, error) {
	f, err := os.Open(procNetRoute)
	if err == nil {
		defer f.Close()
		gw, err := parseProcNetRoute(f)
		if err == nil {
			return gw, nil
		}
	}

	gw, guessErr := guessGateway()
	if guessErr != nil {
		return netip.Addr{}, fmt.Errorf("no default gateway found in %s and none could be guessed: %w", procNetRoute, guessErr)
	}
	return gw, nil
}

// parseProcNetRoute picks the next hop of the best default route.
//
// The format is a header line then tab-separated fields, of which we need
// Destination (2), Gateway (3), Flags (4) and Metric (6). Addresses are
// little-endian hex of the wire order, so 0101A8C0 is 192.168.1.1 — reading
// them big-endian yields a plausible-looking address that is simply wrong,
// which is the mistake this function is most likely to be misread as making.
//
// Rows are skipped rather than rejected on any parse failure. The table gains
// columns and route types over kernel versions, and one unreadable row from
// some tunnel driver must not cost us the default route sitting below it.
func parseProcNetRoute(r io.Reader) (netip.Addr, error) {
	var best netip.Addr
	bestMetric := int64(-1)

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 7 {
			continue
		}
		dest, err := strconv.ParseUint(fields[1], 16, 32)
		if err != nil || dest != 0 {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&rtfUp == 0 || flags&rtfGateway == 0 {
			continue
		}
		raw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		gw := leHexAddr(uint32(raw))
		// A default route through a point-to-point link (a VPN tun, typically)
		// has no next hop at all. There is nothing there to ask for a mapping,
		// and taking it would mean sending PCP into the tunnel.
		if gw.IsUnspecified() {
			continue
		}
		metric, err := strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			continue
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = gw, metric
		}
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("reading the routing table: %w", err)
	}
	if !best.IsValid() {
		return netip.Addr{}, errors.New("the routing table has no default IPv4 route with a next hop")
	}
	return best, nil
}

// leHexAddr converts the little-endian address /proc/net/route prints into an
// address.
func leHexAddr(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

// guessGateway assumes the router is host .1 on our own /24.
//
// The address dialled is in TEST-NET-1 (RFC 5737), which is documentation space
// and therefore guaranteed not to be something we have a specific route to. No
// packet is sent, so nothing reaches it even if it were routable.
func guessGateway() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("finding the outbound interface: %w", err)
	}
	defer conn.Close()

	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, errors.New("finding the outbound interface: local address is not UDP")
	}
	local, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("finding the outbound interface: unusable local address %s", ua.IP)
	}
	local = local.Unmap()
	if !local.Is4() {
		return netip.Addr{}, fmt.Errorf("outbound address %s is not IPv4", local)
	}
	if local.IsLoopback() || local.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("outbound address %s is not on a real network", local)
	}
	b := local.As4()
	b[3] = 1
	gw := netip.AddrFrom4(b)
	if gw == local {
		return netip.Addr{}, fmt.Errorf("outbound address %s is itself the guessed gateway", local)
	}
	return gw, nil
}
