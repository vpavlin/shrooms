package wg

import (
	"encoding/binary"
	"net/netip"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// Making TCP fit a path that a relayed packet cannot.
//
// A relayed packet carries 146 bytes over the tunnel payload, and a phone on
// mobile data commonly sits behind a path narrower than that leaves room for
// (docs/relay-mtu.md). The obvious answer — lower the interface MTU — is not
// available: the overlay is IPv6 and 1280 is the minimum the kernel will accept.
//
// Path MTU discovery is not available either, for the same reason at one
// remove. RFC 8201 says a node must not reduce its path MTU below 1280; a host
// told "packet too big, use 1134" raises it back to 1280 and starts adding
// Fragment headers instead. So the honest signal cannot express what we need to
// say.
//
// Clamping the MSS can, because a TCP segment size is not an IP MTU and has no
// floor. Rewriting the option in a SYN makes both ends choose segments that fit
// for the life of the connection, which is what PPPoE links and VPNs have done
// for this exact problem for twenty years.
//
// It fixes TCP and nothing else. A UDP application sending datagrams larger
// than the path still breaks, and there is no way to tell it not to — but
// applications that send large UDP datagrams over a link they do not control
// are already rare, and bulk transfer is overwhelmingly TCP.

const (
	// tcpProto is IPv6's next-header value for TCP.
	tcpProto = 6
	// ipv6HeaderLen is fixed; extension headers are handled by not touching
	// packets that have them.
	ipv6HeaderLen = 40
	// optMSS is the TCP option kind for maximum segment size, and optMSSLen its
	// total length including kind and length bytes.
	optMSS    = 2
	optMSSLen = 4
	// optEnd and optNop are the two options with no length byte.
	optEnd = 0
	optNop = 1
)

// ClampMSS lowers the MSS advertised by a TCP SYN, and reports whether it
// changed anything.
//
// Only SYN segments carry the option, and only they matter: the value agreed at
// the handshake governs the whole connection.
//
// The packet is modified in place and its checksum repaired incrementally
// rather than recomputed. Recomputing means summing the payload, which on a
// path this sits in front of is work done per packet for no benefit — and the
// incremental form is exact rather than an approximation (RFC 1624).
func ClampMSS(pkt []byte, max uint16) bool {
	if max == 0 || len(pkt) < ipv6HeaderLen+20 {
		return false
	}
	// IPv6 only: the overlay has no IPv4, and the synthetic v4 addresses are
	// translated to it before they reach here (ADR-021).
	if pkt[0]>>4 != 6 || pkt[6] != tcpProto {
		return false
	}
	tcp := pkt[ipv6HeaderLen:]

	// SYN, with or without ACK. A segment that is not a SYN carries no MSS
	// option and rewriting one would be meaningless.
	const synFlag = 0x02
	if tcp[13]&synFlag == 0 {
		return false
	}
	dataOff := int(tcp[12]>>4) * 4
	if dataOff < 20 || dataOff > len(tcp) {
		return false
	}

	for i := 20; i+1 < dataOff; {
		switch tcp[i] {
		case optEnd:
			return false
		case optNop:
			i++
			continue
		}
		optLen := int(tcp[i+1])
		// A length of zero or one would not advance, and a length past the end
		// is a malformed packet. Either way, stop rather than loop or read out
		// of bounds.
		if optLen < 2 || i+optLen > dataOff {
			return false
		}
		if tcp[i] == optMSS && optLen == optMSSLen {
			old := binary.BigEndian.Uint16(tcp[i+2:])
			if old <= max {
				return false
			}
			binary.BigEndian.PutUint16(tcp[i+2:], max)
			fixChecksum(tcp[16:18], old, max)
			return true
		}
		i += optLen
	}
	return false
}

// fixChecksum applies RFC 1624's incremental update for one changed 16-bit word.
//
//	HC' = ~(~HC + ~m + m')
//
// Written out rather than taken from a library because the subtlety is the
// ones-complement arithmetic — a naive subtraction gets the wrong answer when
// the sum wraps, and the failure mode is a packet silently dropped by the far
// end's checksum test rather than anything visible here.
func fixChecksum(sum []byte, old, new uint16) {
	hc := binary.BigEndian.Uint16(sum)
	s := uint32(^hc) + uint32(^old) + uint32(new)
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	binary.BigEndian.PutUint16(sum, ^uint16(s))
}

// RelayOverhead is what a relayed packet carries beyond the tunnel payload.
//
//	32  WireGuard transport header and tag
//	 5  control header, so relay frames share the WireGuard socket
//	81  relay forward header: type, destination, source, MAC
//	 8  UDP
//	20  IPv4 underlay — the smaller of the two, so this under-reserves on IPv6
//	    by 20 bytes and is corrected by SafeUnderlay being conservative
const RelayOverhead = 32 + 5 + 81 + 8 + 20

// SafeUnderlay is what the path to a relay is assumed to carry.
//
// 1200, and the number is measured rather than reasoned. 1280 looked like the
// obvious answer — it is the IPv6 minimum, so nothing may carry less — and it
// was wrong: probing a relay on Akash put the limit at about 1265 bytes on the
// wire. Hosted networks stack their own encapsulation underneath, and an
// overlay eating twenty bytes below the IPv6 floor is apparently ordinary.
//
// So this is a guess with margin, and the margin is the point rather than the
// value. Being too small costs throughput; being too large costs the whole
// transfer, silently, with TCP retransmitting into a path that will never carry
// the packet.
//
// **The right answer is to measure each relay rather than assume any constant.**
// A client already exchanges registration frames with its relay and could pad
// one to find the largest that survives, which is real path MTU discovery on
// the one hop that needs it. Until then this is deliberately pessimistic.
const SafeUnderlay = 1200

// RelayedMSS is the largest TCP segment that fits through a relay, allowing for
// the IPv6 and TCP headers the segment sits inside.
const RelayedMSS = SafeUnderlay - RelayOverhead - 40 - 20

// mssTun clamps TCP segment sizes on the way past.
//
// It wraps whatever tun the device is given rather than being a method on one,
// because the thing handed to NewDevice is not always the same type: a single
// mesh gets a v4 translator over a real device, several meshes get a mux port,
// and a test gets a netstack. The first attempt asserted on one of those and was
// a silent no-op everywhere else — the clamp was installed on nothing, and the
// symptom was identical to not having written it.
//
// Wrapping outside the v4 translator is deliberate: by the time a packet
// reaches here on the way out, a synthetic IPv4 destination has already become
// the peer's overlay address, which is what the limit is keyed on.
type mssTun struct {
	tun.Device
	// mssFor answers how large a segment may be for a given peer. Read on the
	// packet path, so swapped atomically rather than locked.
	mssFor atomic.Pointer[func(netip.Addr) uint16]
}

// Read is outbound: packets from the local stack, heading for a peer. The limit
// is keyed on where they are going.
func (t *mssTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := t.Device.Read(bufs, sizes, offset)
	f := t.mssFor.Load()
	if f == nil || n == 0 {
		return n, err
	}
	for i := 0; i < n && i < len(bufs); i++ {
		pkt := bufs[i][offset : offset+sizes[i]]
		if limit := (*f)(dstOf(pkt)); limit > 0 {
			ClampMSS(pkt, limit)
		}
	}
	return n, err
}

// Write is inbound: what a peer sent us, on its way to the local stack. Keyed
// on where it came from, because it is that peer's advertisement being
// corrected — and correcting it here is what lets one updated end repair a
// connection without the other being rebuilt.
func (t *mssTun) Write(bufs [][]byte, offset int) (int, error) {
	if f := t.mssFor.Load(); f != nil {
		for _, b := range bufs {
			if len(b) <= offset {
				continue
			}
			pkt := b[offset:]
			if limit := (*f)(srcOf(pkt)); limit > 0 {
				ClampMSS(pkt, limit)
			}
		}
	}
	return t.Device.Write(bufs, offset)
}

// srcOf and dstOf read the addresses out of an IPv6 packet, or return the zero
// value for anything else — including IPv4, which the overlay does not carry.
func srcOf(pkt []byte) netip.Addr { return addrAt(pkt, 8) }
func dstOf(pkt []byte) netip.Addr { return addrAt(pkt, 24) }

func addrAt(pkt []byte, off int) netip.Addr {
	if len(pkt) < ipv6HeaderLen || pkt[0]>>4 != 6 {
		return netip.Addr{}
	}
	return netip.AddrFrom16([16]byte(pkt[off : off+16]))
}
