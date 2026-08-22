package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

// start runs the real serve loop on a real socket and returns where to reach
// it. The point of testing here rather than in internal/relay is that this is
// the only place a wire exists — the forwarding rules are covered there.
func start(t *testing.T, opts relay.Options) (netip.AddrPort, relay.Key, *relay.Server) {
	t.Helper()
	opts.Blind, opts.Open = true, true
	key := relay.OpenKey()
	srv := relay.NewServerWith(key, nil, opts)

	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serve(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pc, srv)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return netip.MustParseAddrPort(pc.LocalAddr().String()), key, srv
}

// device is one end: its own socket, its own keys.
type device struct {
	conn *net.UDPConn
	priv ed25519.PrivateKey
	wg   identity.WGKey
}

func newDevice(t *testing.T, n byte) *device {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg identity.WGKey
	wg[0], wg[1] = n, 0x5a
	return &device{conn: c, priv: priv, wg: wg}
}

func (d *device) send(t *testing.T, to netip.AddrPort, pkt []byte) {
	t.Helper()
	if _, err := d.conn.WriteToUDPAddrPort(pkt, to); err != nil {
		t.Fatal(err)
	}
}

func (d *device) recv(t *testing.T, k relay.Key) *relay.Frame {
	t.Helper()
	d.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65535)
	n, _, err := d.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("nothing came back: %v", err)
	}
	f, err := relay.Decode(k, buf[:n])
	if err != nil {
		t.Fatalf("undecodable reply: %v", err)
	}
	return f
}

// waitPeers blocks until the relay holds n registrations.
//
// A confirm is fire-and-forget — the relay sends nothing back — so a client
// learns it is registered only by traffic arriving. That is fine on a real
// mesh, where registrations repeat on a timer, but it means a test cannot
// assert on the table the instant after it sends one.
func waitPeers(t *testing.T, srv *relay.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Stats().Peers == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("relay holds %d registrations, want %d", srv.Stats().Peers, n)
}

// join runs the registration exchange the way a client has to.
func (d *device) join(t *testing.T, at netip.AddrPort, k relay.Key, tag identity.WGKey) {
	t.Helper()
	d.send(t, at, relay.EncodeRegister(k, tag, d.priv, time.Now()))
	f := d.recv(t, k)
	if f.Type != relay.TypeChallenge {
		t.Fatalf("expected a challenge, got frame type %d", f.Type)
	}
	d.send(t, at, relay.EncodeConfirm(k, tag, f.Nonce, d.priv, time.Now()))
}

// End to end over a real socket: two devices that have never met the relay,
// and a packet that arrives at the other one. This is the thing the whole
// design is for, and it is worth having a test that would notice if the wire
// stopped agreeing with the table.
func TestTwoDevicesReachEachOtherThroughABlindRelay(t *testing.T) {
	at, k, srv := start(t, relay.Options{})

	// The tags a mesh would derive. The relay never learns the tunnel keys
	// behind them, which is the point — here it simply never sees them.
	meshKey := relay.TokenKey("a mesh's own relay key")
	a, b := newDevice(t, 1), newDevice(t, 2)
	tagA, tagB := relay.Tag(meshKey, at, a.wg), relay.Tag(meshKey, at, b.wg)

	a.join(t, at, k, tagA)
	b.join(t, at, k, tagB)
	waitPeers(t, srv, 2)

	payload := []byte("wireguard would be in here")
	frame, err := relay.EncodeForward(k, tagB, identity.WGKey{}, payload)
	if err != nil {
		t.Fatal(err)
	}
	a.send(t, at, frame)

	got := b.recv(t, k)
	if got.Type != relay.TypeForward {
		t.Fatalf("b received frame type %d", got.Type)
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("payload came through as %q", got.Payload)
	}
	// The relay fills in the source from its own table rather than trusting
	// what the sender claimed, so b learns who sent it without trusting the
	// relay to tell the truth separately.
	if got.Src != tagA {
		t.Errorf("source came through as %x, want a's tag %x", got.Src, tagA)
	}
}

// A relay that never receives a confirm must not install anything, tested here
// on a real socket because this is the property that makes it safe to run one
// open to the internet.
func TestAnUnansweredChallengeLeavesNothingBehind(t *testing.T) {
	at, k, srv := start(t, relay.Options{})
	d := newDevice(t, 1)

	d.send(t, at, relay.EncodeRegister(k, d.wg, d.priv, time.Now()))
	if f := d.recv(t, k); f.Type != relay.TypeChallenge {
		t.Fatalf("expected a challenge, got %d", f.Type)
	}
	// Walk away without answering. The challenge came back, so the relay has
	// certainly processed the register by now and there is nothing to wait for.
	if got := srv.Stats().Peers; got != 0 {
		t.Errorf("an unanswered challenge left %d registrations", got)
	}
	if got := srv.Stats().Challenged; got != 1 {
		t.Errorf("challenged counter is %d, want 1", got)
	}
}

// Garbage on the port must not stop the relay serving. It is open to the
// internet, so it will receive scans, and a loop that exits on a bad frame is a
// relay that anybody can turn off.
func TestJunkOnThePortDoesNotStopIt(t *testing.T) {
	at, k, srv := start(t, relay.Options{})
	noise := newDevice(t, 9)

	for _, junk := range [][]byte{
		{}, {0}, {255, 255, 255}, make([]byte, 1200),
	} {
		noise.send(t, at, junk)
	}

	// And a real device still works afterwards.
	d := newDevice(t, 1)
	d.join(t, at, k, d.wg)
	waitPeers(t, srv, 1) // fails here if the junk stopped the loop
}
