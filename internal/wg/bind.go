// Package wg embeds wireguard-go and shares its UDP socket with our control
// protocol.
//
// Why share a socket at all: NAT traversal and the tunnel MUST use the same
// socket, or the reflexive address you discover via STUN/echo is not the port
// your data actually arrives on, and hole punches land on the wrong mapping.
// This is why Tailscale runs userspace WireGuard everywhere despite the kernel
// module existing — kernel WireGuard owns its socket and will not share.
//
// The demux follows Tailscale's magicsock discriminators:
//
//	WireGuard : msg[0] in 0x01..0x04 and msg[1:4] == 0x000000
//	disco     : msg[0] == 0x54 ("TS💬")
//	STUN      : msg[1] == 0x01 with the magic cookie at offset 4
//
// Our magic therefore starts with a byte > 0x04 and != 0x54, so all of these
// separate cleanly on the first two bytes.
//
// Implementation follows NetBird's ICEBind: wrap the ReceiveFuncs returned by
// StdNetBind and filter in place. Critically this preserves StdNetBind's
// batching and GSO/GRO offload — we never route the data path through a
// channel, which is what makes go-libp2p's shared-conn approach unsuitable for
// a data plane.
package wg

import (
	"errors"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
)

// Magic prefixes our control packets. First byte 0x6d ('m') is > 0x04 and
// != 0x54, so it cannot be confused with WireGuard, disco or STUN.
var Magic = [4]byte{0x6d, 0x76, 0x70, 0x6e} // "mvpn"

// MagicLen is the length of the control-packet magic prefix.
const MagicLen = 4

// ControlHandler receives a control packet and the endpoint it arrived from.
// It is called on the receive path and MUST NOT block: copy what you need and
// hand off. The buffer is reused once the call returns.
type ControlHandler func(payload []byte, ep conn.Endpoint)

// Bind is a conn.Bind that splits control packets out of the WireGuard stream.
type Bind struct {
	inner conn.Bind

	mu      sync.RWMutex
	handler ControlHandler

	// stats
	ctrlRx uint64
	ctrlTx uint64
}

// NewBind wraps a StdNetBind.
func NewBind() *Bind {
	return &Bind{inner: conn.NewStdNetBind()}
}

// SetControlHandler installs the handler for control packets.
func (b *Bind) SetControlHandler(h ControlHandler) {
	b.mu.Lock()
	b.handler = h
	b.mu.Unlock()
}

// Open implements conn.Bind, wrapping each ReceiveFunc with the demux.
func (b *Bind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := b.inner.Open(port)
	if err != nil {
		return nil, 0, err
	}
	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		wrapped[i] = b.demux(fn)
	}
	return wrapped, actualPort, nil
}

// demux returns a ReceiveFunc that removes control packets from the batch
// before WireGuard sees it.
//
// packets, sizes and eps are parallel arrays. Removing an entry means compacting
// all three, which is why this is written as an explicit two-index loop rather
// than a filter.
func (b *Bind) demux(fn conn.ReceiveFunc) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, err := fn(packets, sizes, eps)
		if n == 0 || err != nil {
			return n, err
		}

		b.mu.RLock()
		handler := b.handler
		b.mu.RUnlock()

		kept := 0
		for i := 0; i < n; i++ {
			pkt := packets[i][:sizes[i]]
			if !isControl(pkt) {
				// Keep for WireGuard, compacting if earlier entries were removed.
				if kept != i {
					packets[kept], packets[i] = packets[i], packets[kept]
					sizes[kept] = sizes[i]
					eps[kept] = eps[i]
				}
				kept++
				continue
			}

			b.ctrlRx++
			if handler != nil {
				handler(pkt[MagicLen:], eps[i])
			}
		}
		return kept, nil
	}
}

// isControl reports whether a packet carries our magic prefix.
func isControl(pkt []byte) bool {
	if len(pkt) < MagicLen {
		return false
	}
	return pkt[0] == Magic[0] && pkt[1] == Magic[1] && pkt[2] == Magic[2] && pkt[3] == Magic[3]
}

// SendControl sends a control packet to ep over the shared socket.
func (b *Bind) SendControl(payload []byte, ep conn.Endpoint) error {
	if ep == nil {
		return errors.New("nil endpoint")
	}
	buf := make([]byte, MagicLen+len(payload))
	copy(buf, Magic[:])
	copy(buf[MagicLen:], payload)

	b.ctrlTx++
	return b.inner.Send([][]byte{buf}, ep)
}

// Stats reports control packets received and sent.
func (b *Bind) Stats() (rx, tx uint64) { return b.ctrlRx, b.ctrlTx }

// --- remaining conn.Bind methods delegate to the inner bind ---

func (b *Bind) Close() error                                { return b.inner.Close() }
func (b *Bind) SetMark(mark uint32) error                   { return b.inner.SetMark(mark) }
func (b *Bind) Send(bufs [][]byte, ep conn.Endpoint) error  { return b.inner.Send(bufs, ep) }
func (b *Bind) ParseEndpoint(s string) (conn.Endpoint, error) { return b.inner.ParseEndpoint(s) }
func (b *Bind) BatchSize() int                              { return b.inner.BatchSize() }
