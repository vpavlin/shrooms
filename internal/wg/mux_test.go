package wg

import (
	"encoding/binary"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// fakeTun is the one descriptor Android gives us.
type fakeTun struct {
	mu      sync.Mutex
	in      chan []byte
	written [][]byte
}

func newFakeTun() *fakeTun { return &fakeTun{in: make(chan []byte, 16)} }

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	pkt, ok := <-f.in
	if !ok {
		return 0, tun.ErrTooManySegments
	}
	sizes[0] = copy(bufs[0][offset:], pkt)
	return 1, nil
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range bufs {
		f.written = append(f.written, append([]byte(nil), b[offset:]...))
	}
	return len(bufs), nil
}

func (f *fakeTun) MTU() (int, error)        { return 1420, nil }
func (f *fakeTun) Name() (string, error)    { return "fake0", nil }
func (f *fakeTun) File() *os.File           { return nil }
func (f *fakeTun) Events() <-chan tun.Event { return make(chan tun.Event) }
func (f *fakeTun) BatchSize() int           { return 1 }
func (f *fakeTun) Close() error             { close(f.in); return nil }

func v6packet(dst netip.Addr) []byte {
	p := make([]byte, 48)
	p[0] = 6 << 4
	binary.BigEndian.PutUint16(p[4:6], 8)
	p[6] = 17
	d := dst.As16()
	copy(p[24:40], d[:])
	return p
}

func v4packet(dst netip.Addr) []byte {
	p := make([]byte, 28)
	p[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[9] = 17
	d := dst.As4()
	copy(p[16:20], d[:])
	return p
}

func readOne(t *testing.T, p *Port) []byte {
	t.Helper()
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	done := make(chan []byte, 1)
	go func() {
		n, err := p.Read(bufs, sizes, 16)
		if err != nil || n == 0 {
			done <- nil
			return
		}
		done <- append([]byte(nil), bufs[0][16:16+sizes[0]]...)
	}()
	select {
	case b := <-done:
		return b
	case <-time.After(2 * time.Second):
		return nil
	}
}

// Each mesh must receive its own traffic and nobody else's. Getting this wrong
// on Android would look like one mesh working and the other silently dead,
// since a WireGuard device drops what it cannot decrypt.
func TestMuxRoutesByPrefix(t *testing.T) {
	home := netip.MustParsePrefix("fd3b:ffe9:f81::/48")
	shared := netip.MustParsePrefix("fd7b:15fb:5ec1::/48")
	homeV4 := netip.MustParsePrefix("198.18.32.0/19")
	sharedV4 := netip.MustParsePrefix("198.19.224.0/19")

	ft := newFakeTun()
	mux := NewMux(ft)
	defer mux.Close()
	pHome := mux.Port(home, homeV4)
	pShared := mux.Port(shared, sharedV4)

	ft.in <- v6packet(netip.MustParseAddr("fd3b:ffe9:f81::5"))
	if got := readOne(t, pHome); got == nil {
		t.Fatal("the home mesh did not receive its own packet")
	}

	ft.in <- v6packet(netip.MustParseAddr("fd7b:15fb:5ec1::9"))
	if got := readOne(t, pShared); got == nil {
		t.Fatal("the shared mesh did not receive its own packet")
	}

	// The synthetic IPv4 blocks route the same way, which is the path a
	// browser takes.
	ft.in <- v4packet(netip.MustParseAddr("198.19.224.7"))
	if got := readOne(t, pShared); got == nil {
		t.Fatal("the shared mesh did not receive its own IPv4 packet")
	}

	// Nothing leaked to the other mesh: one packet in, one packet out, each.
	select {
	case <-pHome.in:
		t.Error("a packet for the shared mesh was delivered to home")
	default:
	}
}

// A packet for no mesh is counted rather than delivered to whichever port
// happens to be first — that would hand one mesh another's traffic.
func TestMuxCountsUnrouted(t *testing.T) {
	ft := newFakeTun()
	mux := NewMux(ft)
	defer mux.Close()
	mux.Port(netip.MustParsePrefix("fd3b:ffe9:f81::/48"))

	ft.in <- v6packet(netip.MustParseAddr("fd00:dead:beef::1"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mux.Unrouted() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("a packet belonging to no mesh was not counted")
}

// Writes are shared: what a mesh sends goes to the one real device.
func TestMuxWritesPassThrough(t *testing.T) {
	ft := newFakeTun()
	mux := NewMux(ft)
	defer mux.Close()
	p := mux.Port(netip.MustParsePrefix("fd3b:ffe9:f81::/48"))

	pkt := v6packet(netip.MustParseAddr("fd3b:ffe9:f81::1"))
	framed := make([]byte, 16+len(pkt))
	copy(framed[16:], pkt)
	if _, err := p.Write([][]byte{framed}, 16); err != nil {
		t.Fatal(err)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.written) != 1 {
		t.Fatalf("wrote %d packets to the device", len(ft.written))
	}
}
