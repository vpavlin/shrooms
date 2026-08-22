package relay

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// Finding out how large a packet can actually reach a relay.
//
// Everything about a relayed path used to rest on a constant. It was 1420,
// which was wrong; then 1280, which looked inarguable because it is the IPv6
// minimum and was also wrong; then 1200, which is a guess with margin and is
// wrong in the other direction — it costs throughput on every path that could
// have carried more.
//
// A constant cannot be right, because the answer is a property of one hop
// between one device and one relay. Measured on Akash, the same relay that a
// laptop reached at about 1265 bytes was reached by a phone on mobile data at
// the same figure, and a different provider would differ again.
//
// So ask. A probe is padded to a size, and a relay that receives it echoes back
// the size it saw. The largest that comes back is what the path carries. It
// needs no negotiation and no agreement about units — the relay reports what
// arrived, and arriving is the whole question.

const (
	// TypeMTUProbe is a padded packet asking a relay what size arrived.
	TypeMTUProbe Type = 5
	// TypeMTUEcho is the answer, carrying the size the relay saw.
	TypeMTUEcho Type = 6
)

const (
	// probeIDLen distinguishes one probe from another, so a late echo from a
	// smaller probe is not mistaken for a larger one succeeding. That mistake
	// would be silent and would raise the MTU above what the path carries,
	// which is the failure this exists to prevent.
	probeIDLen = 8
	// mtuProbeHeaderLen is what a probe costs before padding.
	mtuProbeHeaderLen = 1 + probeIDLen + macLen
	// mtuEchoLen is the answer: the id it refers to and the size seen.
	mtuEchoLen = 1 + probeIDLen + 2 + macLen
)

// ErrProbeTooSmall is a probe that cannot even carry its own header.
var ErrProbeTooSmall = errors.New("probe smaller than its own header")

// EncodeMTUProbe builds a probe of exactly total bytes.
//
// Padding is random rather than zeroes. A path that compresses — some mobile
// links do — would otherwise report carrying more than it will for real
// traffic, and real traffic here is WireGuard, which is indistinguishable from
// random by design.
func EncodeMTUProbe(k Key, id [probeIDLen]byte, total int) ([]byte, error) {
	if total < mtuProbeHeaderLen {
		return nil, fmt.Errorf("%w: %d bytes, need %d", ErrProbeTooSmall, total, mtuProbeHeaderLen)
	}
	if total > MaxPayload {
		return nil, fmt.Errorf("probe of %d bytes exceeds %d", total, MaxPayload)
	}
	buf := make([]byte, 0, total)
	buf = append(buf, byte(TypeMTUProbe))
	buf = append(buf, id[:]...)

	pad := make([]byte, total-mtuProbeHeaderLen)
	if _, err := rand.Read(pad); err != nil {
		return nil, err
	}
	buf = append(buf, pad...)
	return append(buf, mac(k, buf)...), nil
}

// EncodeMTUEcho answers a probe with the size that arrived.
//
// The size is what the relay measured rather than what the sender intended,
// which is the only figure worth having: a probe that was fragmented and
// reassembled, or truncated, is not evidence the path carries it whole.
func EncodeMTUEcho(k Key, id [probeIDLen]byte, saw int) []byte {
	buf := make([]byte, 0, mtuEchoLen)
	buf = append(buf, byte(TypeMTUEcho))
	buf = append(buf, id[:]...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(saw))
	return append(buf, mac(k, buf)...)
}

// NewProbeID returns an identifier for one probe.
func NewProbeID() ([probeIDLen]byte, error) {
	var id [probeIDLen]byte
	_, err := rand.Read(id[:])
	return id, err
}

func decodeMTUProbe(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) < mtuProbeHeaderLen {
		return nil, fmt.Errorf("probe frame is %d bytes, want at least %d",
			len(pkt), mtuProbeHeaderLen)
	}
	body := pkt[:len(pkt)-macLen]
	if !verify(k, body, pkt[len(pkt)-macLen:]) {
		return nil, errors.New("authentication failed")
	}
	f := &Frame{Type: TypeMTUProbe, Saw: len(pkt)}
	copy(f.ProbeID[:], pkt[1:1+probeIDLen])
	return f, nil
}

func decodeMTUEcho(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) != mtuEchoLen {
		return nil, fmt.Errorf("echo frame is %d bytes, want %d", len(pkt), mtuEchoLen)
	}
	if !verify(k, pkt[:mtuEchoLen-macLen], pkt[mtuEchoLen-macLen:]) {
		return nil, errors.New("authentication failed")
	}
	f := &Frame{Type: TypeMTUEcho}
	copy(f.ProbeID[:], pkt[1:1+probeIDLen])
	f.Saw = int(binary.BigEndian.Uint16(pkt[1+probeIDLen:]))
	return f, nil
}

// DiscoverPathMTU finds the largest frame that reaches a relay, by asking it.
//
// A binary search rather than a walk down from the top: a path that carries
// 1400 and one that carries 1200 should cost the same handful of round trips,
// and on a phone waking a radio each one is expensive.
//
// send must deliver exactly the bytes given and return the echo, or an error if
// nothing came back within a sensible time. Timeouts are treated as "too big"
// rather than as failures, which is the whole mechanism: a packet that does not
// fit is discarded silently by whatever could not carry it, and silence is the
// signal.
//
// Returns the largest size confirmed to arrive. When nothing arrives at any
// size — an unreachable relay, or one too old to answer — it reports zero, and
// the caller should fall back to assuming the worst rather than to assuming
// anything.
func DiscoverPathMTU(k Key, low, high int, send func(pkt []byte) (*Frame, error)) (int, error) {
	if low < mtuProbeHeaderLen {
		low = mtuProbeHeaderLen
	}
	if high > MaxPayload {
		high = MaxPayload
	}
	if low > high {
		return 0, fmt.Errorf("range %d..%d is empty", low, high)
	}

	try := func(size int) bool {
		id, err := NewProbeID()
		if err != nil {
			return false
		}
		pkt, err := EncodeMTUProbe(k, id, size)
		if err != nil {
			return false
		}
		f, err := send(pkt)
		if err != nil || f == nil || f.Type != TypeMTUEcho {
			return false
		}
		// The id must match, or a late echo from a smaller probe would be read
		// as a larger one succeeding — and the result would be an MTU above
		// what the path carries, which fails silently and later.
		if f.ProbeID != id {
			return false
		}
		// And the relay must have seen the whole thing. A path that fragments
		// and reassembles will report the full size, which is honest: it did
		// arrive. One that truncates reports less, and less is not a pass.
		return f.Saw >= size
	}

	// The floor has to work before a search means anything, and if it does not
	// there is nothing to bisect.
	if !try(low) {
		return 0, nil
	}
	best := low
	for low <= high {
		mid := low + (high-low)/2
		if try(mid) {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best, nil
}
