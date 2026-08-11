package v4

import (
	"encoding/binary"
	"net/netip"
)

// Stateless header translation, per RFC 6145 and only as much of it as a
// personal overlay needs.
//
// Checksums are recomputed rather than adjusted incrementally. The incremental
// form is the textbook answer and is also where this kind of code goes wrong;
// at a 1420-byte MTU a full recompute is a few hundred additions, and ADR-021
// accepts a copy per packet. If this ever shows up in a profile, the delta form
// is a contained change with a test already around it.

const (
	v4HeaderLen = 20
	v6HeaderLen = 40

	protoICMP   = 1
	protoTCP    = 6
	protoUDP    = 17
	protoICMPv6 = 58

	icmpEchoRequest  = 8
	icmpEchoReply    = 0
	icmp6EchoRequest = 128
	icmp6EchoReply   = 129
	defaultHopLimit  = 64
	tcpFlagSyn       = 0x02
	tcpOptionEnd     = 0
	tcpOptionNop     = 1
	tcpOptionMSS     = 2
	tcpOptionMSSLen  = 4
)

// To6 rewrites an IPv4 packet as IPv6. It returns nil if the packet is not one
// we can translate, in which case the caller must drop it — passing it on
// unchanged would put an IPv4 packet on an IPv6-only overlay.
func To6(pkt []byte, src, dst netip.Addr, mss uint16) []byte {
	if len(pkt) < v4HeaderLen || pkt[0]>>4 != 4 {
		return nil
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < v4HeaderLen || len(pkt) < ihl {
		return nil
	}
	total := int(binary.BigEndian.Uint16(pkt[2:4]))
	if total < ihl || total > len(pkt) {
		return nil
	}
	// Fragments are refused rather than reassembled. An overlay whose MTU we
	// control should not be producing them, and mishandling one silently is
	// worse than not carrying it.
	if binary.BigEndian.Uint16(pkt[6:8])&0x3fff != 0 {
		return nil
	}

	proto := pkt[9]
	payload := pkt[ihl:total]
	next := proto
	if proto == protoICMP {
		next = protoICMPv6
	} else if proto != protoTCP && proto != protoUDP {
		return nil // nothing else has a checksum we know how to fix
	}

	out := make([]byte, v6HeaderLen+len(payload))
	out[0] = 6 << 4
	binary.BigEndian.PutUint16(out[4:6], uint16(len(payload)))
	out[6] = next
	// Not decremented. This is a translator on one host rather than a hop, and
	// a peer that sees the TTL fall by one for v4 and not for v6 is a puzzle
	// nobody needs.
	out[7] = pkt[8]
	if out[7] == 0 {
		out[7] = defaultHopLimit
	}
	s16, d16 := src.As16(), dst.As16()
	copy(out[8:24], s16[:])
	copy(out[24:40], d16[:])
	body := out[v6HeaderLen:]
	copy(body, payload)

	switch proto {
	case protoTCP:
		if len(body) < 20 {
			return nil
		}
		// The v6 header is 20 bytes larger, so a segment sized for the v4 MTU
		// no longer fits. Clamping the MSS on the handshake is how every
		// tunnel solves this, and it is far more reliable than hoping path MTU
		// discovery works through a VPN.
		if mss > 0 && body[13]&tcpFlagSyn != 0 {
			clampMSS(body, mss)
		}
		putChecksum(body, 16, tcpUDPChecksum(s16[:], d16[:], protoTCP, body))
	case protoUDP:
		if len(body) < 8 {
			return nil
		}
		sum := tcpUDPChecksum(s16[:], d16[:], protoUDP, body)
		if sum == 0 {
			sum = 0xffff // UDP over IPv6 may not carry a zero checksum
		}
		putChecksum(body, 6, sum)
	case protoICMP:
		if len(body) < 8 {
			return nil
		}
		switch body[0] {
		case icmpEchoRequest:
			body[0] = icmp6EchoRequest
		case icmpEchoReply:
			body[0] = icmp6EchoReply
		default:
			// Errors carry the offending packet inside them and would need
			// translating too. Dropped for now, which costs path MTU discovery
			// on the v4 path — noted in ADR-021 as the thing to fix next.
			return nil
		}
		// ICMPv6 checksums cover a pseudo-header; ICMPv4's do not, so this is
		// a recompute and not a copy.
		putChecksum(body, 2, tcpUDPChecksum(s16[:], d16[:], protoICMPv6, body))
	}
	return out
}

// To4 rewrites an IPv6 packet as IPv4.
func To4(pkt []byte, src, dst netip.Addr) []byte {
	if len(pkt) < v6HeaderLen || pkt[0]>>4 != 6 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(pkt[4:6]))
	if length == 0 || v6HeaderLen+length > len(pkt) {
		return nil
	}
	next := pkt[6]
	proto := next
	if next == protoICMPv6 {
		proto = protoICMP
	} else if next != protoTCP && next != protoUDP {
		return nil
	}
	payload := pkt[v6HeaderLen : v6HeaderLen+length]

	out := make([]byte, v4HeaderLen+len(payload))
	out[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
	// Don't Fragment, and no identifier: with DF set there is nothing to
	// reassemble, so a predictable ID cannot be used to count our traffic.
	binary.BigEndian.PutUint16(out[6:8], 0x4000)
	out[8] = pkt[7]
	out[9] = proto
	s4, d4 := src.As4(), dst.As4()
	copy(out[12:16], s4[:])
	copy(out[16:20], d4[:])
	putChecksum(out[:v4HeaderLen], 10, checksum(out[:v4HeaderLen]))

	body := out[v4HeaderLen:]
	copy(body, payload)

	switch proto {
	case protoTCP:
		if len(body) < 20 {
			return nil
		}
		putChecksum(body, 16, tcpUDPChecksum4(s4[:], d4[:], protoTCP, body))
	case protoUDP:
		if len(body) < 8 {
			return nil
		}
		putChecksum(body, 6, tcpUDPChecksum4(s4[:], d4[:], protoUDP, body))
	case protoICMP:
		if len(body) < 8 {
			return nil
		}
		switch body[0] {
		case icmp6EchoRequest:
			body[0] = icmpEchoRequest
		case icmp6EchoReply:
			body[0] = icmpEchoReply
		default:
			return nil
		}
		// Zeroed first: the field still holds the ICMPv6 checksum, and summing
		// over it produces a number a receiver will not agree with.
		putChecksum(body, 2, 0)
		putChecksum(body, 2, checksum(body)) // no pseudo-header in ICMPv4
	}
	return out
}

// clampMSS lowers the MSS option in a SYN, if it is higher than the limit.
func clampMSS(tcp []byte, max uint16) {
	offset := int(tcp[12]>>4) * 4
	if offset <= 20 || offset > len(tcp) {
		return
	}
	opts := tcp[20:offset]
	for i := 0; i < len(opts); {
		switch opts[i] {
		case tcpOptionEnd:
			return
		case tcpOptionNop:
			i++
		case tcpOptionMSS:
			if i+tcpOptionMSSLen > len(opts) || opts[i+1] != tcpOptionMSSLen {
				return
			}
			if binary.BigEndian.Uint16(opts[i+2:i+4]) > max {
				binary.BigEndian.PutUint16(opts[i+2:i+4], max)
			}
			return
		default:
			if i+1 >= len(opts) || opts[i+1] < 2 {
				return
			}
			i += int(opts[i+1])
		}
	}
}

func putChecksum(b []byte, at int, sum uint16) {
	binary.BigEndian.PutUint16(b[at:at+2], sum)
}

// checksum is the one's complement sum used by IPv4 and ICMPv4 headers. The
// field being computed must already be zero.
func checksum(b []byte) uint16 {
	return finish(sum(b, 0))
}

func sum(b []byte, acc uint32) uint32 {
	for i := 0; i+1 < len(b); i += 2 {
		acc += uint32(binary.BigEndian.Uint16(b[i : i+2]))
	}
	if len(b)%2 == 1 {
		acc += uint32(b[len(b)-1]) << 8
	}
	return acc
}

func finish(acc uint32) uint16 {
	for acc>>16 != 0 {
		acc = acc&0xffff + acc>>16
	}
	return ^uint16(acc)
}

// tcpUDPChecksum computes a transport checksum over an IPv6 pseudo-header.
func tcpUDPChecksum(src, dst []byte, proto byte, body []byte) uint16 {
	at := 16
	switch proto {
	case protoUDP:
		at = 6
	case protoICMPv6:
		at = 2
	}
	putChecksum(body, at, 0)

	acc := sum(src, 0)
	acc = sum(dst, acc)
	acc += uint32(len(body)) // upper 16 bits of the length are zero at this MTU
	acc += uint32(proto)
	return finish(sum(body, acc))
}

// tcpUDPChecksum4 is the same over an IPv4 pseudo-header.
func tcpUDPChecksum4(src, dst []byte, proto byte, body []byte) uint16 {
	at := 16
	if proto == protoUDP {
		at = 6
	}
	putChecksum(body, at, 0)

	acc := sum(src, 0)
	acc = sum(dst, acc)
	acc += uint32(proto)
	acc += uint32(len(body))
	return finish(sum(body, acc))
}
