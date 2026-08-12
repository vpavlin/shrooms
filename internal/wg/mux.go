package wg

import (
	"errors"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// One TUN, several meshes (ADR-015).
//
// A WireGuard device holds one static key and allows one preshared key per
// peer, and both are per mesh — so meshes cannot share a device. On Linux each
// gets its own TUN and the kernel routes between them. Android hands back
// exactly one descriptor from VpnService, so something has to do that routing
// in userspace.
//
// That something is this: one reader on the real device, dispatching each
// packet to the mesh that owns its destination. The key is exact rather than
// heuristic — every mesh has a distinct /48 (ADR-005) and a distinct block of
// the synthetic IPv4 range (ADR-021), so a packet belongs to exactly one mesh
// or to none.
//
// Writes go straight through: what comes back from a mesh is already addressed
// correctly, and the operating system does not care which mesh produced it.

// Mux fans one TUN device out to several virtual ones.
type Mux struct {
	dev tun.Device

	mu    sync.RWMutex
	ports []*Port

	closeOnce sync.Once
	done      chan struct{}

	// unrouted counts packets whose destination belongs to no mesh. Usually
	// zero; anything else means the interface holds an address or route that
	// no mesh claims, which is worth being able to see.
	//
	// Atomic rather than guarded by mu: it is written on the dispatch path,
	// which holds mu for *reading* so that several meshes can be matched at
	// once, and incrementing under a read lock is a data race however
	// harmless a lost count would be. The race detector was right and the
	// counter was wrong.
	unrouted atomic.Uint64
}

// Port is one mesh's view of the shared device. It satisfies tun.Device, so a
// WireGuard device cannot tell the difference.
type Port struct {
	mux      *Mux
	prefixes []netip.Prefix
	in       chan []byte
	events   chan tun.Event
	closed   chan struct{}
	once     sync.Once
}

// queueDepth is per mesh. Deep enough to absorb a burst while a WireGuard
// device is busy, shallow enough that a stalled mesh cannot hoard memory.
const queueDepth = 128

// NewMux takes ownership of a TUN device.
func NewMux(dev tun.Device) *Mux {
	m := &Mux{dev: dev, done: make(chan struct{})}
	go m.run()
	return m
}

// Port creates a virtual device receiving packets addressed to prefixes.
func (m *Mux) Port(prefixes ...netip.Prefix) *Port {
	p := &Port{
		mux:      m,
		prefixes: prefixes,
		in:       make(chan []byte, queueDepth),
		events:   make(chan tun.Event, 1),
		closed:   make(chan struct{}),
	}
	// WireGuard waits for this before it will do anything at all.
	p.events <- tun.EventUp

	m.mu.Lock()
	m.ports = append(m.ports, p)
	m.mu.Unlock()
	return p
}

// Unrouted reports packets that belonged to no mesh.
func (m *Mux) Unrouted() uint64 { return m.unrouted.Load() }

// run reads the real device and dispatches by destination.
func (m *Mux) run() {
	bufs := make([][]byte, m.dev.BatchSize())
	sizes := make([]int, len(bufs))
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}

	for {
		select {
		case <-m.done:
			return
		default:
		}
		n, err := m.dev.Read(bufs, sizes, 0)
		if err != nil {
			if errors.Is(err, tun.ErrTooManySegments) {
				continue
			}
			m.closePorts()
			return
		}
		for i := 0; i < n; i++ {
			m.dispatch(bufs[i][:sizes[i]])
		}
	}
}

func (m *Mux) dispatch(pkt []byte) {
	dst, ok := destination(pkt)
	if !ok {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.ports {
		if !p.owns(dst) {
			continue
		}
		// Copied, because the read buffer is reused on the next iteration.
		cp := make([]byte, len(pkt))
		copy(cp, pkt)
		select {
		case p.in <- cp:
		default:
			// Dropped rather than blocked: one mesh that has stopped reading
			// must not stall every other mesh on the device.
		}
		return
	}
	m.unrouted.Add(1)
}

// destination reads a packet's destination address, for either family.
func destination(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 20 {
		return netip.Addr{}, false
	}
	switch pkt[0] >> 4 {
	case 4:
		return netip.AddrFrom4([4]byte(pkt[16:20])), true
	case 6:
		if len(pkt) < 40 {
			return netip.Addr{}, false
		}
		return netip.AddrFrom16([16]byte(pkt[24:40])), true
	}
	return netip.Addr{}, false
}

func (m *Mux) closePorts() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.ports {
		p.shut()
	}
}

// Close tears down the mux and the device beneath it.
func (m *Mux) Close() error {
	var err error
	m.closeOnce.Do(func() {
		close(m.done)
		m.closePorts()
		err = m.dev.Close()
	})
	return err
}

func (p *Port) owns(dst netip.Addr) bool {
	for _, pfx := range p.prefixes {
		if pfx.Contains(dst) {
			return true
		}
	}
	return false
}

func (p *Port) shut() { p.once.Do(func() { close(p.closed) }) }

// Read hands WireGuard the packets addressed to this mesh.
func (p *Port) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-p.closed:
		return 0, errors.New("tun mux closed")
	case pkt := <-p.in:
		sizes[0] = copy(bufs[0][offset:], pkt)
		return 1, nil
	}
}

// Write passes packets to the real device unchanged.
func (p *Port) Write(bufs [][]byte, offset int) (int, error) {
	return p.mux.dev.Write(bufs, offset)
}

func (p *Port) MTU() (int, error)        { return p.mux.dev.MTU() }
func (p *Port) Name() (string, error)    { return p.mux.dev.Name() }
func (p *Port) File() *os.File           { return p.mux.dev.File() }
func (p *Port) Events() <-chan tun.Event { return p.events }
func (p *Port) BatchSize() int           { return 1 }

// Close detaches one mesh. The device stays up for the others.
func (p *Port) Close() error {
	p.shut()
	return nil
}
