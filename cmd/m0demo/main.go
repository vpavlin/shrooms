// Command m0demo is milestone M0: prove the data plane and socket sharing.
//
// It stands up two userspace WireGuard peers in one process, connected over
// loopback UDP, each using a netstack TUN so no root or real interface is
// needed. It then:
//
//  1. derives both overlay addresses from device keys (no IPAM)
//  2. brings up a real WireGuard tunnel between them
//  3. carries TCP traffic over the tunnel
//  4. sends control packets over the SAME UDP socket and checks they arrive
//     without disturbing the tunnel
//
// Point 4 is the milestone: it is what makes NAT traversal possible later, and
// what kernel WireGuard cannot do.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/wg"
)

const (
	portA = 51820
	portB = 51821
)

type node struct {
	name  string
	id    *identity.Identity
	addr  netip.Addr
	dev   *wg.Device
	net   *netstack.Net
	ctrl  chan string
	quiet bool
}

func newNode(name string, nk identity.NetworkKey, port uint16, verbose bool) (*node, error) {
	id, err := identity.New()
	if err != nil {
		return nil, err
	}
	addr := identity.OverlayAddr(nk, id.DevicePub)

	tunDev, netTun, err := netstack.CreateNetTUN([]netip.Addr{addr}, nil, 1420)
	if err != nil {
		return nil, fmt.Errorf("create netstack tun: %w", err)
	}

	level := device.LogLevelError
	if verbose {
		level = device.LogLevelVerbose
	}
	dev, err := wg.NewDevice(tunDev, id.WGPriv, port, device.NewLogger(level, "["+name+"] "))
	if err != nil {
		return nil, err
	}

	n := &node{name: name, id: id, addr: addr, dev: dev, net: netTun, ctrl: make(chan string, 16)}

	dev.Bind.SetControlHandler(func(_ wg.Sub, payload []byte, ep conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		select {
		case n.ctrl <- fmt.Sprintf("%s from %s", string(payload), ep.DstToString()):
		default:
		}
		// Consumed: control traffic is not WireGuard's business.
		return nil, nil, false
	})
	return n, nil
}

func main() {
	verbose := len(os.Args) > 1 && os.Args[1] == "-v"
	log.SetFlags(log.Ltime)

	nk, err := identity.NewNetworkKey()
	if err != nil {
		log.Fatalf("network key: %v", err)
	}
	log.Printf("network key : %s", nk)
	log.Printf("mesh prefix : %s", nk.Prefix())

	a, err := newNode("A", nk, portA, verbose)
	if err != nil {
		log.Fatalf("node A: %v", err)
	}
	defer a.dev.Close()

	b, err := newNode("B", nk, portB, verbose)
	if err != nil {
		log.Fatalf("node B: %v", err)
	}
	defer b.dev.Close()

	log.Printf("A overlay   : %s", a.addr)
	log.Printf("B overlay   : %s", b.addr)

	// Addresses are derived, so neither node needed an allocator to agree.
	if !nk.Prefix().Contains(a.addr) || !nk.Prefix().Contains(b.addr) {
		log.Fatalf("derived addresses fall outside the mesh prefix")
	}

	psk := identity.PairPSK(nk, a.id.WGPub, b.id.WGPub)

	if err := a.dev.SetPeers([]wg.Peer{{
		WGPub:     b.id.WGPub,
		Endpoint:  fmt.Sprintf("127.0.0.1:%d", portB),
		AllowedIP: b.addr,
		PSK:       psk,
		Keepalive: 25,
	}}); err != nil {
		log.Fatalf("A peers: %v", err)
	}
	if err := b.dev.SetPeers([]wg.Peer{{
		WGPub:     a.id.WGPub,
		Endpoint:  fmt.Sprintf("127.0.0.1:%d", portA),
		AllowedIP: a.addr,
		PSK:       psk,
		Keepalive: 25,
	}}); err != nil {
		log.Fatalf("B peers: %v", err)
	}

	log.Printf("--- tunnel ---")
	if err := tunnelTest(a, b, 8080); err != nil {
		log.Fatalf("FAIL tunnel: %v", err)
	}

	log.Printf("--- control packets on the same socket ---")
	if err := controlTest(a, b); err != nil {
		log.Fatalf("FAIL control: %v", err)
	}

	log.Printf("--- tunnel still healthy after control traffic ---")
	if err := tunnelTest(a, b, 8081); err != nil {
		log.Fatalf("FAIL tunnel after control: %v", err)
	}

	rxA, txA, _ := a.dev.Bind.Stats()
	rxB, txB, _ := b.dev.Bind.Stats()
	log.Printf("control packets  A rx=%d tx=%d  B rx=%d tx=%d", rxA, txA, rxB, txB)
	log.Printf("M0 PASS")
}

// tunnelTest runs a TCP echo from A to B over the WireGuard tunnel.
func tunnelTest(a, b *node, port uint16) error {
	const payload = "hello over the overlay"

	ln, err := b.net.ListenTCP(&net.TCPAddr{Port: int(port)})
	if err != nil {
		return fmt.Errorf("listen on B: %w", err)
	}
	defer ln.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c) // echo
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	c, err := a.net.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(b.addr, port))
	if err != nil {
		return fmt.Errorf("dial B over tunnel: %w", err)
	}
	defer c.Close()

	if _, err := c.Write([]byte(payload)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if string(buf) != payload {
		return fmt.Errorf("echo mismatch: %q", buf)
	}

	log.Printf("  TCP echo %d bytes A->B->A in %s", len(payload), time.Since(start).Round(time.Millisecond))
	c.Close()
	ln.Close()
	wg.Wait()
	return nil
}

// controlTest sends control packets in both directions over the shared socket.
func controlTest(a, b *node) error {
	epB, err := a.dev.Bind.ParseEndpoint(fmt.Sprintf("127.0.0.1:%d", portB))
	if err != nil {
		return fmt.Errorf("parse B endpoint: %w", err)
	}
	epA, err := b.dev.Bind.ParseEndpoint(fmt.Sprintf("127.0.0.1:%d", portA))
	if err != nil {
		return fmt.Errorf("parse A endpoint: %w", err)
	}

	if err := a.dev.Bind.SendControl(wg.SubDisco, []byte("ping-from-A"), epB); err != nil {
		return fmt.Errorf("A send: %w", err)
	}
	if err := b.dev.Bind.SendControl(wg.SubDisco, []byte("ping-from-B"), epA); err != nil {
		return fmt.Errorf("B send: %w", err)
	}

	for _, want := range []struct {
		n    *node
		text string
	}{{b, "ping-from-A"}, {a, "ping-from-B"}} {
		select {
		case got := <-want.n.ctrl:
			log.Printf("  %s received %q", want.n.name, got)
		case <-time.After(5 * time.Second):
			return fmt.Errorf("%s never received %q", want.n.name, want.text)
		}
	}
	return nil
}
