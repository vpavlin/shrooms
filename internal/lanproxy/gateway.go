// Package lanproxy is the gateway side of ADR-068: it answers for the LAN hosts
// a node offers, in a userspace TCP stack, and proxies to the LAN. Its own
// package so that only a desktop daemon links gVisor; the phone, which is never
// a gateway, does not.
package lanproxy

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/lanhost"
)

// A Gateway wraps the data plane's TUN device. Packets WireGuard delivers for
// a configured host's overlay address are diverted into a userspace TCP stack
// (gVisor, through wireguard-go's netstack) instead of the kernel, and the
// stack's replies are merged back into what WireGuard reads. The host's
// overlay address therefore exists in no kernel namespace at all: nothing the
// gateway binds to its own addresses is reachable through it, which is what
// the review asked a separate namespace for.
//
// The stack listens on each host's configured ports, but a packet reaches it
// only for a port the grant in force opens: anything else is dropped before
// the stack, so an ungranted host or port does not even answer with a reset
// (F-32). Every connection is checked again when it is accepted, the LAN
// address is checked on-link and not the gateway's own, and only then is the
// LAN dialled. Only TCP enters the stack: UDP and ICMP for a host's address
// are dropped, not answered.
//
// The grant is checked for as long as a connection lasts, not only when it
// opens: a withdrawal, a narrowing or the grant's end closes what it no longer
// covers (F-27). The audit found a stream that outlived its withdrawal, which
// a member that keeps the route in its own WireGuard config could hold open
// for as long as it liked.

// Connection caps, per host and per member: an appliance on a LAN is not built
// for a crowd, and one member should not be able to fill a host's sockets.
const (
	MaxConnsPerHost   = 64
	MaxConnsPerMember = 16
	dialTimeout       = 5 * time.Second
	// recheckEvery bounds how long a connection outlives its grant's end
	// when nothing else prompts a check; a withdrawal or a narrowing
	// prompts one at once (Recheck).
	recheckEvery = 5 * time.Second
	// packetOffset is the headroom wireguard-go reads and writes packets with
	// (device.MessageTransportHeaderSize); the kernel TUN needs it for its
	// virtio header.
	packetOffset = 16
)

// GrantFunc returns the grant in force for one of this gateway's hosts, or
// nil. It is the mesh's view, which changes as grants arrive and lapse.
type GrantFunc func(host string, now time.Time) *cred.LanGrant

type servedHost struct {
	spec    lanhost.Spec
	overlay netip.Addr
	// grant is the grant in force at the last check, or nil: what the packet
	// path admits by.
	grant   atomic.Pointer[cred.LanGrant]
	mu      sync.Mutex
	conns   int
	perPeer map[netip.Addr]int
	live    map[*liveConn]struct{}
}

// liveConn is one member's connection to a host, as long as it lasts.
type liveConn struct {
	port uint16
	from netip.Addr
	c    net.Conn
	lan  net.Conn // set once the LAN answers
}

// allows reports whether the grant last seen opens port now.
func (h *servedHost) allows(port uint16, now time.Time) bool {
	gr := h.grant.Load()
	return gr != nil && gr.Active(now) && gr.Contains(port)
}

type packet struct {
	b   []byte
	err error
}

// Gateway is a tun.Device that answers for LAN hosts.
type Gateway struct {
	inner  tun.Device
	ns     tun.Device
	net    *netstack.Net
	self   netip.Addr
	hosts  map[netip.Addr]*servedHost
	grant  atomic.Pointer[GrantFunc]
	log    *slog.Logger
	out    chan packet
	closed chan struct{}
	once   sync.Once
	lns    []net.Listener
	// onLink is the LAN check, a field so tests can stand in for the
	// machine's interfaces.
	onLink func(netip.Addr) error
}

// NewGateway serves specs behind inner. prefix is the mesh's /48, gateway this
// device's key, self its own overlay address.
func NewGateway(inner tun.Device, specs []lanhost.Spec, prefix netip.Prefix, gateway ed25519.PublicKey,
	self netip.Addr, mtu int, log *slog.Logger) (*Gateway, error) {

	if len(specs) == 0 {
		return nil, errors.New("no LAN hosts to serve")
	}
	g := &Gateway{
		inner:  inner,
		self:   self,
		hosts:  map[netip.Addr]*servedHost{},
		log:    log,
		out:    make(chan packet, 1024),
		closed: make(chan struct{}),
		onLink: OnLink,
	}
	var addrs []netip.Addr
	for _, sp := range specs {
		o := lanhost.Overlay(prefix, gateway, sp.Name)
		g.hosts[o] = &servedHost{spec: sp, overlay: o, perPeer: map[netip.Addr]int{}, live: map[*liveConn]struct{}{}}
		addrs = append(addrs, o)
	}
	ns, n, err := netstack.CreateNetTUN(addrs, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("LAN host stack: %w", err)
	}
	g.ns, g.net = ns, n
	for _, h := range g.hosts {
		for _, r := range h.spec.Ports {
			for p := int(r.From); p <= int(r.To); p++ {
				ln, err := n.ListenTCPAddrPort(netip.AddrPortFrom(h.overlay, uint16(p)))
				if err != nil {
					g.Close()
					return nil, fmt.Errorf("LAN host %s port %d: %w", h.spec.Name, p, err)
				}
				g.lns = append(g.lns, ln)
				go g.accept(h, uint16(p), ln)
			}
		}
	}
	// Said once at the start, so the owner sees where each host really is,
	// or why it will not be served, without waiting for a member to try.
	for name, where := range g.Links() {
		log.Info("LAN host", "host", name, "lan", where)
	}
	go g.pumpInner()
	go g.pumpStack()
	go g.recheckLoop()
	return g, nil
}

// SetGrants sets where grants are looked up. Until it is called, every
// connection is refused.
func (g *Gateway) SetGrants(f GrantFunc) {
	g.grant.Store(&f)
	g.Recheck()
}

// Recheck looks every host's grant up again, and closes each connection the
// grant no longer covers. Called when a grant changes, and on a timer for
// grants that lapse.
func (g *Gateway) Recheck() {
	now := time.Now()
	gf := g.grant.Load()
	for _, h := range g.hosts {
		var gr *cred.LanGrant
		if gf != nil {
			if gr = (*gf)(h.spec.Name, now); gr != nil && !gr.Active(now) {
				gr = nil
			}
		}
		h.grant.Store(gr)
		var ended []liveConn
		h.mu.Lock()
		for l := range h.live {
			if !h.allows(l.port, now) {
				ended = append(ended, *l)
			}
		}
		h.mu.Unlock()
		for _, l := range ended {
			g.log.Info("closed a LAN host connection: its grant no longer covers it",
				"host", h.spec.Name, "port", l.port, "from", l.from)
			l.c.Close()
			if l.lan != nil {
				l.lan.Close()
			}
		}
	}
}

func (g *Gateway) recheckLoop() {
	t := time.NewTicker(recheckEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			g.Recheck()
		case <-g.closed:
			return
		}
	}
}

// Overlays is the hosts' overlay addresses, for status.
func (g *Gateway) Overlays() map[string]netip.Addr {
	out := make(map[string]netip.Addr, len(g.hosts))
	for o, h := range g.hosts {
		out[h.spec.Name] = o
	}
	return out
}

// Links is where each host's LAN address is, for status: "192.168.10.143
// on wlan0", or why it cannot be served.
func (g *Gateway) Links() map[string]string {
	out := make(map[string]string, len(g.hosts))
	for _, h := range g.hosts {
		if ifc, err := LinkOf(h.spec.Addr); err != nil {
			out[h.spec.Name] = err.Error()
		} else {
			out[h.spec.Name] = h.spec.Addr.String() + " on " + ifc
		}
	}
	return out
}

// dst6 is an IPv6 packet's destination, and whether it is TCP with no
// extension header in front: anything else is not for the stack.
func dst6(p []byte) (netip.Addr, bool, bool) {
	if len(p) < 40 || p[0]>>4 != 6 {
		return netip.Addr{}, false, false
	}
	var a [16]byte
	copy(a[:], p[24:40])
	return netip.AddrFrom16(a), p[6] == 6 && len(p) >= 40+20, true
}

// tcpDstPort is the destination port of an IPv6 TCP packet dst6 accepted.
func tcpDstPort(p []byte) uint16 { return uint16(p[42])<<8 | uint16(p[43]) }

// Write is WireGuard handing packets to the machine: TCP for a host goes to
// the stack, anything for the gateway itself to the kernel, and the rest
// nowhere. The rest used to go to the kernel too, which made the gateway a
// router into its LAN for any member that addressed a packet there (F-26);
// the daemon also wraps every node's device in wg.SelfOnly, and this check
// stays so the gateway does not depend on what it is stacked on.
func (g *Gateway) Write(bufs [][]byte, offset int) (int, error) {
	var kernel, stack [][]byte
	for _, b := range bufs {
		dst, tcp, ok := dst6(b[offset:])
		if !ok {
			continue
		}
		if h, ours := g.hosts[dst]; ours {
			// UDP and ICMP for a host are dropped (ADR-068: TCP only), and so
			// is TCP to a port no grant in force opens.
			if tcp && h.allows(tcpDstPort(b[offset:]), time.Now()) {
				stack = append(stack, b)
			}
			continue
		}
		if dst == g.self {
			kernel = append(kernel, b)
		}
	}
	if len(stack) > 0 {
		if _, err := g.ns.Write(stack, offset); err != nil {
			g.log.Debug("LAN host stack refused packets", "err", err)
		}
	}
	if len(kernel) == 0 {
		return len(bufs), nil
	}
	if _, err := g.inner.Write(kernel, offset); err != nil {
		return 0, err
	}
	return len(bufs), nil
}

// pumpInner reads what the machine sends into the tunnel. A packet for a host
// (the gateway's own processes reaching it) goes to the stack; the rest is
// queued for WireGuard.
func (g *Gateway) pumpInner() {
	batch := g.inner.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, packetOffset+65535)
	}
	sizes := make([]int, batch)
	for {
		n, err := g.inner.Read(bufs, sizes, packetOffset)
		for i := 0; i < n; i++ {
			p := bufs[i][packetOffset : packetOffset+sizes[i]]
			if dst, tcp, ok := dst6(p); ok {
				if _, ours := g.hosts[dst]; ours {
					if tcp {
						g.ns.Write([][]byte{append(make([]byte, packetOffset), p...)}, packetOffset)
					}
					continue
				}
			}
			if !g.queue(append([]byte(nil), p...)) {
				return
			}
		}
		if err != nil {
			if errors.Is(err, tun.ErrTooManySegments) {
				continue
			}
			select {
			case g.out <- packet{err: err}:
			case <-g.closed:
			}
			return
		}
	}
}

// pumpStack reads the stack's replies. One to this gateway's own address goes
// back to the kernel; the rest is queued for WireGuard.
func (g *Gateway) pumpStack() {
	bufs := [][]byte{make([]byte, packetOffset+65535)}
	sizes := []int{0}
	for {
		n, err := g.ns.Read(bufs, sizes, packetOffset)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		p := bufs[0][packetOffset : packetOffset+sizes[0]]
		if len(p) >= 40 && p[0]>>4 == 6 {
			var a [16]byte
			copy(a[:], p[24:40])
			if netip.AddrFrom16(a) == g.self {
				g.inner.Write([][]byte{append(make([]byte, packetOffset), p...)}, packetOffset)
				continue
			}
		}
		if !g.queue(append([]byte(nil), p...)) {
			return
		}
	}
}

func (g *Gateway) queue(p []byte) bool {
	select {
	case g.out <- packet{b: p}:
		return true
	case <-g.closed:
		return false
	}
}

// Read is WireGuard taking packets to send: the machine's and the stack's,
// merged.
func (g *Gateway) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	var first packet
	select {
	case first = <-g.out:
	case <-g.closed:
		return 0, os.ErrClosed
	}
	if first.err != nil {
		return 0, first.err
	}
	n := 0
	put := func(p []byte) {
		sizes[n] = copy(bufs[n][offset:], p)
		n++
	}
	put(first.b)
	for n < len(bufs) {
		select {
		case p := <-g.out:
			if p.err != nil {
				// Delivered on the next call, so these packets are not lost.
				go func() { g.out <- p }()
				return n, nil
			}
			put(p.b)
		default:
			return n, nil
		}
	}
	return n, nil
}

func (g *Gateway) File() *os.File           { return g.inner.File() }
func (g *Gateway) MTU() (int, error)        { return g.inner.MTU() }
func (g *Gateway) Name() (string, error)    { return g.inner.Name() }
func (g *Gateway) Events() <-chan tun.Event { return g.inner.Events() }
func (g *Gateway) BatchSize() int           { return g.inner.BatchSize() }

// Close stops the stack and the device beneath.
func (g *Gateway) Close() error {
	var err error
	g.once.Do(func() {
		close(g.closed)
		for _, ln := range g.lns {
			ln.Close()
		}
		if g.ns != nil {
			g.ns.Close()
		}
		err = g.inner.Close()
	})
	return err
}

// accept serves one host port. The grant and the caps are checked here,
// before a goroutine is spent: a member opening connections as fast as it
// can costs the gateway a closed socket each, not a goroutine and a log line
// (F-32). Refusals are logged at Debug for the same reason: the remote sets
// their pace.
func (g *Gateway) accept(h *servedHost, port uint16, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		from := remoteAddr(c)
		if !h.allows(port, time.Now()) {
			g.log.Debug("refused a LAN host connection: no grant in force for that host and port",
				"host", h.spec.Name, "port", port, "from", from)
			c.Close()
			continue
		}
		l := &liveConn{port: port, from: from, c: c}
		if !h.take(l) {
			g.log.Debug("refused a LAN host connection: too many at once", "host", h.spec.Name, "from", from)
			c.Close()
			continue
		}
		go g.serve(h, l)
	}
}

func (g *Gateway) serve(h *servedHost, l *liveConn) {
	defer h.give(l)
	defer l.c.Close()
	if err := g.onLink(h.spec.Addr); err != nil {
		g.log.Warn("refused a LAN host connection: its LAN address is not usable here", "host", h.spec.Name, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	lan, err := (&net.Dialer{}).DialContext(ctx, "tcp", netip.AddrPortFrom(h.spec.Addr, l.port).String())
	cancel()
	if err != nil {
		g.log.Info("LAN host did not answer", "host", h.spec.Name, "port", l.port, "err", err)
		return
	}
	defer lan.Close()
	// Registered before the copy, then checked once more: a withdrawal that
	// landed while the LAN was being dialled found no LAN side to close.
	if !h.attach(l, lan) {
		return
	}
	g.log.Debug("LAN host connection", "host", h.spec.Name, "port", l.port, "from", l.from)
	splice(l.c, lan)
}

func (h *servedHost) take(l *liveConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns >= MaxConnsPerHost || h.perPeer[l.from] >= MaxConnsPerMember {
		return false
	}
	h.conns++
	h.perPeer[l.from]++
	h.live[l] = struct{}{}
	return true
}

// attach records the LAN side of a connection, and reports whether its grant
// still covers it.
func (h *servedHost) attach(l *liveConn, lan net.Conn) bool {
	h.mu.Lock()
	l.lan = lan
	h.mu.Unlock()
	return h.allows(l.port, time.Now())
}

func (h *servedHost) give(l *liveConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns--
	if h.perPeer[l.from]--; h.perPeer[l.from] <= 0 {
		delete(h.perPeer, l.from)
	}
	delete(h.live, l)
}

func remoteAddr(c net.Conn) netip.Addr {
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		return ap.Addr()
	}
	return netip.Addr{}
}

// idleAfter closes a LAN host connection that has carried nothing either way
// for this long. Without it a member could hold a host's connections, and
// the gateway's slots, open indefinitely (2026-10-25). Both directions count,
// so a one-way stream (a camera) is not idle; a printer's MQTT keepalive is a
// minute, far inside it.
var idleAfter = 30 * time.Minute

// splice copies both ways, passing on a half-close, until both sides end or
// nothing has moved either way for idleAfter.
func splice(a, b net.Conn) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, activity{src, &last})
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	tick := time.NewTicker(min(idleAfter/4, time.Minute))
	defer tick.Stop()
	for ended := 0; ended < 2; {
		select {
		case <-done:
			ended++
		case <-tick.C:
			if time.Since(time.Unix(0, last.Load())) >= idleAfter {
				a.Close()
				b.Close()
			}
		}
	}
}

// activity is a reader that notes when it last read anything.
type activity struct {
	r    io.Reader
	last *atomic.Int64
}

func (a activity) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// OnLink checks a LAN address against this machine's interfaces now.
func OnLink(a netip.Addr) error {
	_, err := LinkOf(a)
	return err
}

// LinkOf is the interface a LAN address is on: inside the subnet of an up
// interface that is a LAN, not one of this machine's own addresses, and not
// that subnet's network or broadcast address. It names the interface so that
// status can show where a host really is.
//
// A LAN is a physical interface, or a bridge or bond over one (physicalLink).
// This used to accept any interface not named after ours, which took in a
// WAN neighbour, a host behind another VPN and a container on docker0: each
// "on a link the gateway is on", none what the admin thought they granted
// (F-30). The type decides, not the name, so a tunnel called eth1 is still a
// tunnel.
func LinkOf(a netip.Addr) (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var off []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			own, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			own = own.Unmap()
			if own == a {
				return "", fmt.Errorf("%s is this gateway's own address", a)
			}
			ones, _ := ipn.Mask.Size()
			subnet := netip.PrefixFrom(own, ones).Masked()
			if !subnet.Contains(a) {
				continue
			}
			if !physicalLink(ifc.Name) {
				off = append(off, ifc.Name)
				continue
			}
			if edge(subnet, a) {
				return "", fmt.Errorf("%s is the network or broadcast address of %s", a, subnet)
			}
			return ifc.Name, nil
		}
	}
	if len(off) > 0 {
		return "", fmt.Errorf("%s is on %s, which is a tunnel, a container bridge or another virtual link, not a LAN",
			a, strings.Join(off, ", "))
	}
	return "", fmt.Errorf("%s is not on a LAN this gateway is on", a)
}

// edge reports whether a is the network or broadcast address of an IPv4
// subnet that has such things (/31 and /32 do not), so a broadcast is not
// offered as a host (F-35). IPv6 has neither.
func edge(subnet netip.Prefix, a netip.Addr) bool {
	if !a.Is4() || subnet.Bits() >= 31 {
		return false
	}
	if a == subnet.Addr() {
		return true
	}
	b := subnet.Addr().As4()
	host := 32 - subnet.Bits()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	n |= 1<<host - 1
	return a == netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
