package dns

import (
	"encoding/binary"
	"net/netip"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// Intercept answers DNS queries addressed to this device's own overlay address
// before WireGuard sees them.
//
// On Android this is the only way a mesh resolver can be reached at all.
// VpnService routes queries for the tunnel's DNS server THROUGH the tunnel, so
// the query arrives on the tun with our own address as its destination.
// WireGuard then looks for a peer that owns that address, finds none — it is
// ours — and drops it. Nothing is listening anywhere a packet can reach.
//
// Handling it here also removes the need to bind port 53, which an unprivileged
// app may not do. The resolver never touches a socket.
//
// Linux does not need this: there the address is on a real interface and the
// kernel delivers locally. Wrapping is harmless there, and the same code
// running on both platforms is worth more than the few instructions it costs.
type Intercept struct {
	tun.Device

	// self is the address the resolver answers ON — dns.ServiceAddr, not this
	// device's own address. See ServiceAddr for why that distinction is the
	// whole reason this works.
	self netip.Addr
	// answer takes a DNS query payload and returns a response payload.
	answer func([]byte) ([]byte, error)

	handled atomic.Uint64
	failed  atomic.Uint64

	// missed counts packets addressed to the resolver that this cannot answer:
	// TCP rather than UDP, or some other port.
	//
	// Here because "names do not resolve in one app and do in another" has
	// exactly two shapes and no way to tell them apart from outside — the query
	// never reached us, or it reached us in a form we drop. WireGuard then
	// looks for a peer owning the resolver's address, finds none, and discards
	// it silently, which is indistinguishable from the query never arriving.
	// Guessing at this once cost a day; counting is cheap.
	missed atomic.Uint64
}

// NewIntercept wraps a TUN device.
func NewIntercept(dev tun.Device, self netip.Addr, answer func([]byte) ([]byte, error)) *Intercept {
	return &Intercept{Device: dev, self: self, answer: answer}
}

// Stats reports how many queries were served and how many failed.
func (i *Intercept) Stats() (handled, failed uint64) {
	return i.handled.Load(), i.failed.Load()
}

// Missed reports packets aimed at the resolver that arrived in a form it does
// not answer — almost always DNS over TCP.
func (i *Intercept) Missed() uint64 { return i.missed.Load() }

// Read removes queries for us from the batch and answers them.
//
// The batch is compacted by COPYING bytes, never by swapping slice headers.
//
// Not for the reason the receive path had — this one reads back the same array
// it hands us. It is that wireguard-go pairs them: device/send.go sets
// `bufs[i] = elems[i].buffer[:]` and then `elem.packet = bufs[i][...]`, so
// rearranging bufs leaves an element whose packet points into a different
// element's buffer. Copying cannot desynchronise them, and the tests below
// cannot see the difference — which is the argument for the version that has no
// hazard rather than the one that needs a proof.
func (i *Intercept) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := i.Device.Read(bufs, sizes, offset)
	if n == 0 || err != nil {
		return n, err
	}

	kept := 0
	for k := 0; k < n; k++ {
		pkt := bufs[k][offset : offset+sizes[k]]

		if i.aimedAtUs(pkt) {
			i.missed.Add(1)
		}
		if query, ok := i.queryForUs(pkt); ok {
			if reply := i.respond(pkt, query, offset); reply != nil {
				// Straight back to the OS: this is a reply, not something to
				// encrypt and send to a peer.
				if _, werr := i.Device.Write([][]byte{reply}, offset); werr != nil {
					i.failed.Add(1)
				} else {
					i.handled.Add(1)
				}
			} else {
				i.failed.Add(1)
			}
			continue // consumed either way; WireGuard must not see it
		}

		if kept != k {
			sizes[kept] = copy(bufs[kept][offset:], pkt)
		}
		kept++
	}
	return kept, nil
}

const (
	ipv6HeaderLen = 40
	udpHeaderLen  = 8
	protoUDP      = 17
)

// aimedAtUs reports whether a packet is addressed to the resolver but is not a
// UDP query it can answer. Counting only, so that a client falling back to TCP
// is visible rather than looking like silence.
func (i *Intercept) aimedAtUs(pkt []byte) bool {
	if len(pkt) < ipv6HeaderLen || pkt[0]>>4 != 6 {
		return false
	}
	dst, ok := netip.AddrFromSlice(pkt[24:40])
	if !ok || dst != i.self {
		return false
	}
	if _, answerable := i.queryForUs(pkt); answerable {
		return false
	}
	return true
}

// queryForUs reports whether a packet is a DNS query addressed to this device,
// and returns the query payload.
func (i *Intercept) queryForUs(pkt []byte) ([]byte, bool) {
	if len(pkt) < ipv6HeaderLen+udpHeaderLen {
		return nil, false
	}
	if pkt[0]>>4 != 6 {
		return nil, false // the overlay is IPv6-only
	}
	if pkt[6] != protoUDP {
		// No extension-header walking: a DNS query does not carry them, and
		// guessing wrong here would mean swallowing a packet we cannot answer.
		return nil, false
	}
	dst, ok := netip.AddrFromSlice(pkt[24:40])
	if !ok || dst != i.self {
		return nil, false
	}

	udp := pkt[ipv6HeaderLen:]
	if binary.BigEndian.Uint16(udp[2:4]) != Port {
		return nil, false
	}
	length := int(binary.BigEndian.Uint16(udp[4:6]))
	if length < udpHeaderLen || ipv6HeaderLen+length > len(pkt) {
		return nil, false
	}
	return pkt[ipv6HeaderLen+udpHeaderLen : ipv6HeaderLen+length], true
}

// respond builds a reply packet, with `offset` bytes of headroom for the
// caller's virtio header.
func (i *Intercept) respond(req, query []byte, offset int) []byte {
	payload, err := i.answer(query)
	if err != nil || len(payload) == 0 {
		return nil
	}

	udpLen := udpHeaderLen + len(payload)
	total := offset + ipv6HeaderLen + udpLen
	out := make([]byte, total)
	ip := out[offset:]

	// IPv6 header: swap the addresses, keep everything else minimal.
	ip[0] = 6 << 4
	binary.BigEndian.PutUint16(ip[4:6], uint16(udpLen))
	ip[6] = protoUDP
	ip[7] = 64
	copy(ip[8:24], req[24:40]) // src = the address they sent to (ours)
	copy(ip[24:40], req[8:24]) // dst = whoever asked

	udp := ip[ipv6HeaderLen:]
	copy(udp[0:2], req[ipv6HeaderLen+2:ipv6HeaderLen+4]) // sport = their dport (53)
	copy(udp[2:4], req[ipv6HeaderLen+0:ipv6HeaderLen+2]) // dport = their sport
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLen))
	copy(udp[udpHeaderLen:], payload)

	// Mandatory in IPv6, unlike IPv4 where it may be zero. A packet with a
	// wrong or absent checksum is dropped by the receiver with no diagnostic.
	binary.BigEndian.PutUint16(udp[6:8], 0)
	binary.BigEndian.PutUint16(udp[6:8], udpChecksum(ip[8:24], ip[24:40], udp))
	return out
}

// udpChecksum computes the IPv6 UDP checksum over the pseudo-header and the
// datagram.
func udpChecksum(src, dst, udp []byte) uint16 {
	var sum uint32

	add := func(b []byte) {
		for len(b) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(b[:2]))
			b = b[2:]
		}
		if len(b) == 1 {
			sum += uint32(b[0]) << 8
		}
	}

	add(src)
	add(dst)
	var meta [8]byte
	binary.BigEndian.PutUint32(meta[0:4], uint32(len(udp)))
	meta[7] = protoUDP
	add(meta[:])
	add(udp)

	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	csum := ^uint16(sum)
	if csum == 0 {
		// Zero means "no checksum" in IPv4 and is illegal in IPv6; the
		// one's-complement of the sum is transmitted as all ones instead.
		csum = 0xffff
	}
	return csum
}
