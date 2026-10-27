package lanproxy

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
	"golang.zx2c4.com/wireguard/tun/tuntest"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/lanhost"
)

// echo is the LAN appliance: it answers whatever it is sent.
func echo(t *testing.T) (*net.TCPListener, uint16) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln, uint16(ln.Addr().(*net.TCPAddr).Port)
}

// rig is a member and a gateway wired together as WireGuard would wire them:
// what the member's stack sends is what the gateway's Write receives, and what
// the gateway's Read yields is delivered to the member.
type rig struct {
	member *netstack.Net
	gw     *Gateway
	host   netip.Addr
	inner  *tuntest.ChannelTUN // the gateway's kernel side
}

func newRig(t *testing.T, sp lanhost.Spec, grant GrantFunc) *rig {
	t.Helper()
	prefix := netip.MustParsePrefix("fdaa:bbbb:cccc::/48")
	gwKey, _, _ := ed25519.GenerateKey(nil)
	self := netip.MustParseAddr("fdaa:bbbb:cccc::1")
	memberAddr := netip.MustParseAddr("fdaa:bbbb:cccc::2")

	inner := tuntest.NewChannelTUN()
	g, err := NewGateway(inner.TUN(), []lanhost.Spec{sp}, prefix, gwKey, self, 1280, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	g.onLink = func(netip.Addr) error { return nil } // the LAN is loopback in a test
	if grant != nil {
		g.SetGrants(grant)
	}
	t.Cleanup(func() { g.Close() })

	mdev, mnet, err := netstack.CreateNetTUN([]netip.Addr{memberAddr}, nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mdev.Close() })
	// member -> gateway
	go func() {
		bufs := [][]byte{make([]byte, packetOffset+65535)}
		sizes := []int{0}
		for {
			n, err := mdev.Read(bufs, sizes, packetOffset)
			if err != nil {
				return
			}
			if n == 1 {
				p := append(make([]byte, packetOffset), bufs[0][packetOffset:packetOffset+sizes[0]]...)
				g.Write([][]byte{p}, packetOffset)
			}
		}
	}()
	// gateway -> member
	go func() {
		bufs := [][]byte{make([]byte, packetOffset+65535)}
		sizes := []int{0}
		for {
			n, err := g.Read(bufs, sizes, packetOffset)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				p := append(make([]byte, packetOffset), bufs[i][packetOffset:packetOffset+sizes[i]]...)
				mdev.Write([][]byte{p}, packetOffset)
			}
		}
	}()
	return &rig{member: mnet, gw: g, host: lanhost.Overlay(prefix, gwKey, sp.Name), inner: inner}
}

func grantFor(ports string) GrantFunc {
	rs, _ := cred.ParseLanPorts(ports)
	g := &cred.LanGrant{Host: "printer", Ports: rs, NotBefore: 0, NotAfter: time.Now().Add(time.Hour).Unix()}
	return func(host string, now time.Time) *cred.LanGrant {
		if host == "printer" {
			return g
		}
		return nil
	}
}

// talk dials the host from the member and reports what came back.
func (r *rig) talk(t *testing.T, port uint16) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := r.member.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(r.host, port))
	if err != nil {
		return "dial: " + err.Error()
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		return "write: " + err.Error()
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		return "read: " + err.Error()
	}
	return string(buf)
}

// A member reaches the LAN host on a granted port, through the gateway's own
// stack, by the host's overlay address (ADR-068).
func TestAMemberReachesAGrantedLanHost(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	r := newRig(t, lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs},
		grantFor(cred.FormatLanPorts(rs)))
	if got := r.talk(t, port); got != "hello" {
		t.Fatalf("through the gateway: %q", got)
	}
}

// Without a grant in force, or on a port the grant does not open, the
// connection is refused even though the gateway's config lists it.
func TestALanHostWithoutAGrantIsRefused(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	sp := lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs}

	if got := newRig(t, sp, nil).talk(t, port); got == "hello" {
		t.Error("served with no grants known")
	}
	other := port + 1
	if other == 0 {
		other = port - 1
	}
	if got := newRig(t, sp, grantFor(cred.FormatLanPorts([]cred.PortRange{{From: other, To: other}}))).talk(t, port); got == "hello" {
		t.Error("served on a port the grant does not open")
	}
}

// The LAN check refuses this gateway's own addresses and anything off its
// links.
func TestOnLinkRefusesTheGatewayAndElsewhere(t *testing.T) {
	if OnLink(netip.MustParseAddr("203.0.113.9")) == nil {
		t.Error("a documentation address counted as on-link")
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 || !physicalLink(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				own, _ := netip.AddrFromSlice(ipn.IP.To4())
				if OnLink(own) == nil {
					t.Errorf("this machine's own %s counted as a LAN host", own)
				}
				return
			}
		}
	}
}

// packet6 is a minimal IPv6 packet from a member to dst, with headroom.
func packet6(dst netip.Addr, proto byte, port uint16) []byte {
	p := make([]byte, packetOffset+40+20)
	h := p[packetOffset:]
	h[0] = 6 << 4
	h[4], h[5] = 0, 20
	h[6], h[7] = proto, 64
	src := netip.MustParseAddr("fdaa:bbbb:cccc::2").As16()
	copy(h[8:24], src[:])
	d := dst.As16()
	copy(h[24:40], d[:])
	h[42], h[43] = byte(port>>8), byte(port)
	return p
}

// Only packets for the gateway itself reach its kernel. Anything else a
// member addresses there, the gateway's LAN or public IPv6, another member,
// a host's UDP, IPv4, is dropped rather than routed onward (F-26).
func TestAGatewayHandsItsKernelOnlyItsOwnPackets(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	r := newRig(t, lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs},
		grantFor(cred.FormatLanPorts(rs)))
	got := make(chan []byte, 16)
	go func() {
		for p := range r.inner.Inbound {
			got <- p
		}
	}()

	v4 := make([]byte, packetOffset+40)
	v4[packetOffset] = 4 << 4
	for name, p := range map[string][]byte{
		"TCP to another address": packet6(netip.MustParseAddr("fdaa:bbbb:cccc::99"), 6, 22),
		"UDP to another address": packet6(netip.MustParseAddr("2001:db8::1"), 17, 53),
		"UDP to a host":          packet6(r.host, 17, port),
		"IPv4":                   v4,
	} {
		r.gw.Write([][]byte{p}, packetOffset)
		select {
		case <-got:
			t.Errorf("%s reached the gateway's kernel", name)
		case <-time.After(50 * time.Millisecond):
		}
	}

	r.gw.Write([][]byte{packet6(netip.MustParseAddr("fdaa:bbbb:cccc::1"), 6, 22)}, packetOffset)
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Error("a packet for the gateway itself did not reach its kernel")
	}
}

// A connection open when the grant is withdrawn, or narrowed off its port, is
// closed: the grant holds for as long as the connection lasts (F-27).
func TestAWithdrawalClosesAnOpenConnection(t *testing.T) {
	_, port := echo(t)
	rs := []cred.PortRange{{From: port, To: port}}
	var current atomic.Pointer[cred.LanGrant]
	current.Store(&cred.LanGrant{Host: "printer", Ports: rs, NotAfter: time.Now().Add(time.Hour).Unix()})
	r := newRig(t, lanhost.Spec{Name: "printer", Addr: netip.MustParseAddr("127.0.0.1"), Ports: rs},
		func(host string, now time.Time) *cred.LanGrant { return current.Load() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := r.member.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(r.host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 5)
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("before the withdrawal: %v", err)
	}

	current.Store(nil)
	r.gw.Recheck()
	c.Write([]byte("again"))
	if n, err := io.ReadFull(c, buf); err == nil {
		t.Fatalf("still answered after the withdrawal: %q", buf[:n])
	}
}

// A subnet's network and broadcast addresses are not hosts (F-35).
func TestASubnetsEdgesAreNotHosts(t *testing.T) {
	lan := netip.MustParsePrefix("192.168.10.0/24")
	for a, want := range map[string]bool{
		"192.168.10.0": true, "192.168.10.255": true, "192.168.10.143": false, "192.168.10.1": false,
	} {
		if edge(lan, netip.MustParseAddr(a)) != want {
			t.Errorf("edge(%s) = %v", a, !want)
		}
	}
	if edge(netip.MustParsePrefix("10.0.0.0/31"), netip.MustParseAddr("10.0.0.1")) {
		t.Error("a /31 has no broadcast address")
	}
}

// A LAN is hardware, or a bridge or bond over it; a container bridge, a veth
// and a tunnel are not, whatever they are called (F-30).
func TestOnlyAPhysicalLinkIsALan(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, parts ...string) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			if err := os.MkdirAll(filepath.Join(root, name, p), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("eth0", "device")
	mk("wlan0", "device")
	mk("br0", "brif/eth0")
	mk("docker0", "brif/veth1")
	mk("veth1")
	mk("eth1") // a tunnel with an ordinary name
	mk("bond0", "lower_wlan0")
	for name, want := range map[string]bool{
		"eth0": true, "wlan0": true, "br0": true, "bond0": true,
		"docker0": false, "veth1": false, "eth1": false, "missing": false, "../eth0": false,
	} {
		if got := physicalUnder(root, name, 0); got != want {
			t.Errorf("%s: physical = %v, want %v", name, got, want)
		}
	}
}

// A spliced connection that carries nothing either way is closed after the
// idle limit; one that keeps carrying, even one way, is not.
func TestAnIdleLANConnectionIsClosed(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	ended := make(chan struct{})
	go func() { splice(a2, b1, 200*time.Millisecond); close(ended) }()

	// Busy one way for longer than the limit.
	go io.Copy(io.Discard, b2)
	stop := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(stop) {
		if _, err := a1.Write([]byte("x")); err != nil {
			t.Fatalf("a busy connection was cut: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-ended:
		t.Fatal("a connection busy one way was taken for idle")
	default:
	}
	// Then silent.
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("an idle connection was never closed")
	}
}
