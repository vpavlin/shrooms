package v4

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// Device translates between the synthetic IPv4 addresses and the overlay.
//
// It wraps the tun the same way the DNS intercept does, and for the same
// reason: this is where packets are already being handled, and neither the
// WireGuard side nor the operating system side needs to know.
//
// **The inbound direction needs state, and ADR-021 was wrong to imply
// otherwise.** Outbound is a pure function — an IPv4 packet addressed to an
// alias becomes an IPv6 packet addressed to that peer. Inbound is not: a packet
// arriving from a peer is either the reply to a translated flow, which must
// become IPv4 again, or ordinary IPv6 traffic, which must not. Nothing in the
// packet says which. Translating everything would break `http://[fd93:…]/`,
// which is how the mesh's web interfaces are reached today and the one thing
// that definitely works.
//
// So a flow is remembered when it is translated, and inbound packets matching
// one are translated back. The table is small — one entry per connection made
// to a v4 alias — and entries expire on idleness.
type Device struct {
	tun.Device

	table *Table
	mss   uint16

	mu    sync.Mutex
	flows map[flowKey]time.Time

	translatedOut atomic.Uint64
	translatedIn  atomic.Uint64
	dropped       atomic.Uint64
}

// flowKey identifies a conversation, from this device's point of view.
//
// The local port and the peer are enough: the alias and the overlay address are
// the same peer by construction, so there is no need to record both.
type flowKey struct {
	proto     byte
	localPort uint16
	peer      netip.Addr // overlay address
	peerPort  uint16
}

// FlowIdle is how long a flow is remembered after its last packet.
//
// Two minutes covers a page load and a keepalive interval, and losing an entry
// early costs one connection rather than corrupting anything: the reply arrives
// as IPv6, the application does not recognise it, and the client retries.
const FlowIdle = 2 * time.Minute

// NewDevice wraps a tun. mss is the largest segment a translated TCP connection
// may use; zero disables clamping.
func NewDevice(dev tun.Device, table *Table, mss uint16) *Device {
	return &Device{Device: dev, table: table, mss: mss, flows: make(map[flowKey]time.Time)}
}

// Stats reports how much has been translated in each direction, and how many
// packets were dropped for want of a mapping.
func (d *Device) Stats() (out, in, dropped uint64) {
	return d.translatedOut.Load(), d.translatedIn.Load(), d.dropped.Load()
}

// Read takes packets from the operating system towards WireGuard, translating
// any addressed to an alias.
func (d *Device) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := d.Device.Read(bufs, sizes, offset)
	if n == 0 || err != nil {
		return n, err
	}

	kept := 0
	for k := 0; k < n; k++ {
		pkt := bufs[k][offset : offset+sizes[k]]

		if len(pkt) > 0 && pkt[0]>>4 == 4 {
			six := d.toOverlay(pkt)
			if six == nil {
				// Dropped, not forwarded: an IPv4 packet has no meaning on an
				// IPv6-only overlay, and WireGuard would send it to a peer that
				// cannot answer it.
				d.dropped.Add(1)
				continue
			}
			sizes[kept] = copy(bufs[kept][offset:], six)
			kept++
			d.translatedOut.Add(1)
			continue
		}

		if kept != k {
			sizes[kept] = copy(bufs[kept][offset:], pkt)
		}
		kept++
	}
	return kept, nil
}

// Write takes packets from WireGuard towards the operating system, translating
// those that belong to a flow this device started over IPv4.
func (d *Device) Write(bufs [][]byte, offset int) (int, error) {
	out := make([][]byte, 0, len(bufs))
	for _, buf := range bufs {
		pkt := buf[offset:]
		four := d.toAlias(pkt)
		if four == nil {
			out = append(out, buf)
			continue
		}
		// Rebuilt with the same headroom, because wireguard-go hands us its own
		// buffers and the caller owns everything before offset.
		framed := make([]byte, offset+len(four))
		copy(framed[offset:], four)
		out = append(out, framed)
		d.translatedIn.Add(1)
	}
	return d.Device.Write(out, offset)
}

// toOverlay translates an outbound IPv4 packet and records the flow.
func (d *Device) toOverlay(pkt []byte) []byte {
	if len(pkt) < v4HeaderLen {
		return nil
	}
	dst, ok := netip.AddrFromSlice(pkt[16:20])
	if !ok {
		return nil
	}
	peer, mapped := d.table.Overlay(dst)
	if !mapped {
		// Not one of ours. The range is ours alone, so anything else addressed
		// into it is a misconfiguration rather than traffic to carry.
		return nil
	}
	six := To6(pkt, d.table.SelfOverlay(), peer, d.mss)
	if six == nil {
		return nil
	}
	if key, ok := keyOf(pkt[9], pkt[v4HeaderLen:], peer, true); ok {
		d.remember(key)
	}
	return six
}

// toAlias translates an inbound IPv6 packet if it belongs to a remembered flow.
func (d *Device) toAlias(pkt []byte) []byte {
	if len(pkt) < v6HeaderLen || pkt[0]>>4 != 6 {
		return nil
	}
	src, ok := netip.AddrFromSlice(pkt[8:24])
	if !ok {
		return nil
	}
	alias, mapped := d.table.Alias(src)
	if !mapped {
		return nil
	}
	key, ok := keyOf(pkt[6], pkt[v6HeaderLen:], src, false)
	if !ok || !d.known(key) {
		return nil // ordinary IPv6 traffic; leave it alone
	}
	return To4(pkt, alias, d.table.Self())
}

// keyOf builds a flow key from a packet's transport header. outbound says
// whether the ports are ours-then-theirs or the other way round.
func keyOf(proto byte, body []byte, peer netip.Addr, outbound bool) (flowKey, bool) {
	switch proto {
	case protoTCP, protoUDP:
		if len(body) < 4 {
			return flowKey{}, false
		}
		a := binary.BigEndian.Uint16(body[0:2])
		b := binary.BigEndian.Uint16(body[2:4])
		if !outbound {
			a, b = b, a
		}
		return flowKey{proto: proto, localPort: a, peer: peer, peerPort: b}, true
	case protoICMP, protoICMPv6:
		if len(body) < 8 {
			return flowKey{}, false
		}
		// The echo identifier stands in for a port, which is what makes a ping
		// reply attributable to the ping that caused it.
		id := binary.BigEndian.Uint16(body[4:6])
		return flowKey{proto: protoICMP, localPort: id, peer: peer}, true
	}
	return flowKey{}, false
}

func (d *Device) remember(k flowKey) {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flows[k] = now
	// Swept here rather than on a timer: the table only grows when packets are
	// being translated, so the moment one arrives is exactly when it is worth
	// looking, and a daemon that is idle does no work.
	if len(d.flows) > 64 {
		for key, seen := range d.flows {
			if now.Sub(seen) > FlowIdle {
				delete(d.flows, key)
			}
		}
	}
}

func (d *Device) known(k flowKey) bool {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	seen, ok := d.flows[k]
	if !ok || now.Sub(seen) > FlowIdle {
		return false
	}
	// Refreshed on use, so a long-lived connection does not expire underneath
	// itself while it is still carrying traffic.
	d.flows[k] = now
	return true
}
