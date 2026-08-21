// Package ctrl is the framing that lets control traffic share the WireGuard
// socket.
//
// Path probes and relay frames travel on the same UDP port as WireGuard itself,
// which is what makes NAT traversal possible at all: a hole punched for one is
// a hole punched for the other, and a second port would be a second thing for a
// firewall to block. So every control packet is prefixed with a magic value
// WireGuard's own framing cannot produce, plus a byte saying which
// sub-protocol it belongs to.
//
// This lives in its own package because two very different things need it and
// only one of them can afford the other's dependencies. The daemon wraps and
// unwraps inside its WireGuard bind; the standalone relay has no WireGuard at
// all — it is a map and a socket, and importing the bind to learn five bytes
// would have cost it a third more dependencies than it has in total.
//
// Splitting it also fixes the bug that prompted it. The standalone relay read
// raw frames while every real client sent wrapped ones, so registrations were
// rejected as unreadable and counted as drops. It went unnoticed because the
// probe spoke raw too: a test that agreed with the implementation and with
// nothing else.
package ctrl

// Magic prefixes a control packet.
//
// First byte 0x6d ('m') is greater than 0x04, which is what keeps it out of
// WireGuard's own message-type space — WireGuard uses 1 to 4 and drops the rest,
// so a peer that does not understand these ignores them rather than misreading
// them.
var Magic = [4]byte{0x6d, 0x76, 0x70, 0x6e} // "mvpn"

// MagicLen is the length of the magic prefix.
const MagicLen = 4

// HeaderLen is the whole prefix: magic plus the sub-protocol byte.
const HeaderLen = MagicLen + 1

// Sub identifies which control sub-protocol a packet belongs to.
type Sub uint8

const (
	SubDisco Sub = 1 // path discovery probes
	SubRelay Sub = 2 // relayed WireGuard traffic
)

// Is reports whether a packet carries the control magic.
func Is(pkt []byte) bool {
	return len(pkt) >= HeaderLen &&
		pkt[0] == Magic[0] && pkt[1] == Magic[1] &&
		pkt[2] == Magic[2] && pkt[3] == Magic[3]
}

// Wrap prefixes a payload for the given sub-protocol.
//
// One allocation and one copy, because a partially written packet cannot be
// recovered from: the reader would resynchronise on the wrong boundary and stay
// wrong.
func Wrap(sub Sub, payload []byte) []byte {
	out := make([]byte, HeaderLen+len(payload))
	copy(out, Magic[:])
	out[MagicLen] = byte(sub)
	copy(out[HeaderLen:], payload)
	return out
}

// Unwrap strips the prefix, reporting the sub-protocol and whether the packet
// was framed at all.
func Unwrap(pkt []byte) (Sub, []byte, bool) {
	if !Is(pkt) {
		return 0, pkt, false
	}
	return Sub(pkt[MagicLen]), pkt[HeaderLen:], true
}
