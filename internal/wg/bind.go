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
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/relay"
)

// relayScheme prefixes an endpoint routed through a relay. WireGuard treats
// endpoint strings as opaque, so we can define our own form.
const relayScheme = "relay:"

// Magic prefixes our control packets. First byte 0x6d ('m') is > 0x04 and
// != 0x54, so it cannot be confused with WireGuard, disco or STUN.
var Magic = [4]byte{0x6d, 0x76, 0x70, 0x6e} // "mvpn"

// MagicLen is the length of the control-packet magic prefix.
const MagicLen = 4

// Sub identifies which control sub-protocol a packet belongs to. Both ride the
// same socket, so they need distinguishing without either having to be
// self-describing.
type Sub uint8

const (
	SubDisco Sub = 1 // path discovery probes
	SubRelay Sub = 2 // relayed WireGuard traffic
)

// headerLen is magic plus the sub-protocol byte.
const headerLen = MagicLen + 1

// ControlHandler receives a control packet and the endpoint it arrived from.
// It is called on the receive path and MUST NOT block: copy what you need and
// hand off. The buffer is reused once the call returns.
//
// Returning a non-nil packet and true injects it back into the batch as though
// it had arrived from `inject` — this is how relayed WireGuard traffic is
// unwrapped and handed to WireGuard as if it had come from the peer directly.
type ControlHandler func(sub Sub, payload []byte, ep conn.Endpoint) (unwrapped []byte, inject conn.Endpoint, ok bool)

// Bind is a conn.Bind that splits control packets out of the WireGuard stream.
type Bind struct {
	inner conn.Bind

	mu       sync.RWMutex
	handler  ControlHandler
	relayKey relay.Key
	selfPub  identity.WGKey

	// stats
	ctrlRx  uint64
	ctrlTx  uint64
	relayTx uint64
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
			if handler == nil {
				continue
			}

			unwrapped, inject, ok := handler(Sub(pkt[MagicLen]), pkt[headerLen:], eps[i])
			if !ok {
				continue // consumed; not WireGuard's business
			}

			// Relayed traffic: replace the buffer with the inner packet and
			// present it to WireGuard as though it came from the peer. Copying
			// in place keeps it in the batch, so batching and offload survive.
			if len(unwrapped) > len(packets[i]) {
				continue
			}
			copy(packets[kept], unwrapped)
			sizes[kept] = len(unwrapped)
			eps[kept] = inject
			kept++
		}
		return kept, nil
	}
}

// isControl reports whether a packet carries our magic prefix.
func isControl(pkt []byte) bool {
	if len(pkt) < headerLen {
		return false
	}
	return pkt[0] == Magic[0] && pkt[1] == Magic[1] && pkt[2] == Magic[2] && pkt[3] == Magic[3]
}

// SendControl sends a control packet to ep over the shared socket.
func (b *Bind) SendControl(sub Sub, payload []byte, ep conn.Endpoint) error {
	if ep == nil {
		return errors.New("nil endpoint")
	}
	buf := make([]byte, headerLen+len(payload))
	copy(buf, Magic[:])
	buf[MagicLen] = byte(sub)
	copy(buf[headerLen:], payload)

	b.ctrlTx++
	return b.inner.Send([][]byte{buf}, ep)
}

// Stats reports control packets received and sent, and packets sent via a relay.
func (b *Bind) Stats() (rx, tx, relayed uint64) { return b.ctrlRx, b.ctrlTx, b.relayTx }

// --- remaining conn.Bind methods delegate to the inner bind ---

func (b *Bind) Close() error              { return b.inner.Close() }
func (b *Bind) SetMark(mark uint32) error { return b.inner.SetMark(mark) }

// Send routes to the relay when the endpoint is a relayed one. WireGuard is
// unaware: it holds a single opaque endpoint per peer, and this is where a
// relayed peer's traffic gets wrapped.
func (b *Bind) Send(bufs [][]byte, ep conn.Endpoint) error {
	re, relayed := ep.(*RelayEndpoint)
	if !relayed {
		return b.inner.Send(bufs, ep)
	}

	inner, err := b.inner.ParseEndpoint(re.Relay.String())
	if err != nil {
		return fmt.Errorf("relay endpoint %s: %w", re.Relay, err)
	}
	for _, buf := range bufs {
		frame, err := re.Wrap(buf)
		if err != nil {
			return err
		}
		out := make([]byte, headerLen+len(frame))
		copy(out, Magic[:])
		out[MagicLen] = byte(SubRelay)
		copy(out[headerLen:], frame)

		if err := b.inner.Send([][]byte{out}, inner); err != nil {
			return err
		}
		b.relayTx++
	}
	return nil
}

// ParseEndpoint understands our relay form as well as a plain ip:port.
//
// This round-trip matters: WireGuard is configured over the UAPI with an
// endpoint *string*, and it calls this to turn that back into an endpoint. If
// we only ever constructed RelayEndpoint values directly, WireGuard would hand
// Send a plain endpoint and the traffic would never be wrapped.
//
//	relay:<hex peer pubkey>@<relay ip:port>
func (b *Bind) ParseEndpoint(s string) (conn.Endpoint, error) {
	if !strings.HasPrefix(s, relayScheme) {
		return b.inner.ParseEndpoint(s)
	}
	rest := strings.TrimPrefix(s, relayScheme)
	keyHex, addrStr, ok := strings.Cut(rest, "@")
	if !ok {
		return nil, fmt.Errorf("malformed relay endpoint %q", s)
	}
	peer, err := identity.ParseWGKey(keyHex)
	if err != nil {
		return nil, fmt.Errorf("relay endpoint %q: %w", s, err)
	}
	addr, err := netip.ParseAddrPort(addrStr)
	if err != nil {
		return nil, fmt.Errorf("relay endpoint %q: %w", s, err)
	}

	b.mu.RLock()
	key, self := b.relayKey, b.selfPub
	b.mu.RUnlock()

	// Fail loudly rather than building an endpoint whose MAC the relay will
	// reject. Without this the symptom is a relay that silently drops every
	// frame, which looks like a network problem rather than a missing call.
	if key == (relay.Key{}) || self.IsZero() {
		return nil, errors.New("relay endpoint requested before SetRelayIdentity was called")
	}

	return NewRelayEndpoint(key, addr, self, peer), nil
}

// SetRelayIdentity supplies what ParseEndpoint needs to rebuild relay endpoints.
func (b *Bind) SetRelayIdentity(key relay.Key, selfPub identity.WGKey) {
	b.mu.Lock()
	b.relayKey, b.selfPub = key, selfPub
	b.mu.Unlock()
}
func (b *Bind) BatchSize() int { return b.inner.BatchSize() }
