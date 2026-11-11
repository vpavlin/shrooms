package ustack

import (
	"context"
	"io"
	"net/netip"
	"testing"
	"time"
)

// Two stacks wired back to back, as two phones would be through WireGuard: a
// connection made by one is taken by the other, from the right address and a
// port above the kernel's.
func TestBackToBack(t *testing.T) {
	a1 := netip.MustParseAddr("fd00:1:2::a")
	b1 := netip.MustParseAddr("fd00:1:2::b")
	other := netip.MustParseAddr("fd99:1:2::a") // another mesh's address on A
	var a, b *Stack
	var err error
	a, err = New(1280, func(p []byte) { b.Deliver(p) })
	if err != nil {
		t.Fatal(err)
	}
	b, err = New(1280, func(p []byte) {
		if !Wants(p, 9181) {
			t.Errorf("A is sent a packet it would not take")
		}
		a.Deliver(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	for _, x := range []netip.Addr{a1, other} {
		if err := a.Add(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Add(b1); err != nil {
		t.Fatal(err)
	}
	ln, err := b.Listen(netip.AddrPortFrom(b1, 9181))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer c.Close()
		buf, _ := io.ReadAll(c)
		got <- c.RemoteAddr().String() + " " + string(buf)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := a.DialContext(ctx, "tcp", netip.AddrPortFrom(b1, 9181).String())
	if err != nil {
		t.Fatal(err)
	}
	ap := netip.MustParseAddrPort(c.LocalAddr().String())
	if ap.Addr() != a1 || ap.Port() < PortLow {
		t.Errorf("dialled from %s: want %s, port %d and up", ap, a1, PortLow)
	}
	c.Write([]byte("hello"))
	c.Close()
	select {
	case s := <-got:
		if want := ap.String() + " hello"; s != want {
			t.Fatalf("B took %q, want %q", s, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}
}

func TestWants(t *testing.T) {
	tcp := func(src, dst uint16) []byte {
		p := make([]byte, 60)
		p[0], p[6] = 0x60, 6
		p[40], p[41], p[42], p[43] = byte(src>>8), byte(src), byte(dst>>8), byte(dst)
		return p
	}
	for _, c := range []struct {
		p    []byte
		want bool
	}{
		{tcp(40000, 9181), true},  // a peer connecting in
		{tcp(9181, 61234), true},  // a reply to the stack's own
		{tcp(9181, 45000), false}, // a reply to another app's kernel socket
		{tcp(443, 61234), false},  // anything else
	} {
		if Wants(c.p, 9181) != c.want {
			t.Errorf("%v: want %v", c.p[40:44], c.want)
		}
	}
}
