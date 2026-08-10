package service

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	meshdns "github.com/vpavlin/logos-vpn/internal/dns"
)

// The whole claim, end to end: type a service name, get bytes back from an
// application that listens only on IPv4 loopback.
//
// Each half is unit-tested elsewhere; this exists because the halves meet at a
// seam that has been wrong before. The resolver used to assume the leftmost
// label named the device, which resolves immich.home-server.mesh to nothing —
// and a resolver test alone would not have shown that the forwarder was fine.
//
// What it cannot cover is the overlay address itself, which needs a tun device
// and root. ::1 stands in; the difference is which address is bound, not what
// happens after.
func TestServiceNameToApplication(t *testing.T) {
	const device = "home-server"

	// The application: IPv4 loopback only, which is the case that makes plain
	// DNS insufficient.
	app, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go func() {
		for {
			c, err := app.Accept()
			if err != nil {
				return
			}
			go func() { io.WriteString(c, "the application"); c.Close() }()
		}
	}()

	ln, port := freePort(t)
	ln.Close()
	here := netip.MustParseAddr("::1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pub := Publish(ctx, here, device+".mesh", []Spec{{
		Name: "immich", Port: port, Target: app.Addr().String(),
	}}, nil)
	defer pub.Close()

	// The resolver, wired the way internal/mesh wires it: it is handed
	// everything below the suffix and works out which label is the device.
	resolver := &meshdns.Server{
		Suffix: "mesh",
		Lookup: func(host string) (netip.Addr, bool) {
			labels := strings.Split(host, ".")
			if labels[0] == device {
				return here, true
			}
			if len(labels) >= 2 && labels[1] == device {
				return here, true
			}
			return netip.Addr{}, false
		},
	}
	pc, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	defer pc.Close()
	go resolver.Serve(ctx, pc)

	addr := resolve(t, pc.LocalAddr().String(), "immich."+device+".mesh.")

	c, err := net.DialTimeout("tcp6", netip.AddrPortFrom(addr, port).String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial %v: %v", addr, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))

	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the application" {
		t.Errorf("got %q from the service name", got)
	}
}

// resolve asks a resolver for one AAAA and returns it.
func resolve(t *testing.T, server, name string) netip.Addr {
	t.Helper()

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1, RecursionDesired: true})
	b.StartQuestions()
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  dnsmessage.TypeAAAA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	q, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}

	c, err := net.Dial("udp", server)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(q); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("no reply for %s: %v", name, err)
	}

	var p dnsmessage.Parser
	h, err := p.Start(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if h.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("%s: %v", name, h.RCode)
	}
	p.SkipAllQuestions()
	answers, err := p.AllAnswers()
	if err != nil || len(answers) == 0 {
		t.Fatalf("%s: no answer (%v)", name, err)
	}
	aaaa, ok := answers[0].Body.(*dnsmessage.AAAAResource)
	if !ok {
		t.Fatalf("%s: %T, not AAAA", name, answers[0].Body)
	}
	return netip.AddrFrom16(aaaa.AAAA)
}
