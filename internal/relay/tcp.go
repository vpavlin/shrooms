package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Relay frames over TCP.
//
// The relay's own protocol does not change at all — these are the same frames,
// carried differently. What TCP needs and UDP gives for free is a boundary
// between one frame and the next, so each is prefixed with its length.
//
// Why a second transport exists at all: hosted infrastructure frequently
// forwards TCP and not UDP. Measured on an Akash provider, 2026-08-21 — TCP
// node ports wide open, the same range on UDP answering nothing — which put the
// only reachable relay behind a dedicated IPv4 costing ten times the compute
// under it. The same applies to the networks people sit on: hotel wifi,
// corporate guest networks and a few mobile carriers block UDP outright, and a
// device on one of those cannot reach the mesh by any path today.
//
// This is a fallback and must stay one. A WireGuard tunnel carries the user's
// own TCP, so running the outer leg over TCP as well means a lost segment
// stalls everything behind it while the inner stack retransmits on top of the
// outer one. Under loss that degrades badly, and it fails confusingly — a
// tunnel that is up and unusable. UDP first, always; this only when UDP cannot
// get through.

// maxFrame bounds one frame on the wire.
//
// A forward frame is a header plus a WireGuard packet, so this is generous.
// It is also exactly what a 16-bit length can express, which is why the prefix
// is two bytes rather than four: a longer field could describe a frame this
// side would refuse anyway.
const maxFrame = 65535

// lenPrefix is the size of the length field.
const lenPrefix = 2

// ErrFrameTooBig is a frame that cannot be expressed on this transport.
var ErrFrameTooBig = errors.New("relay frame exceeds the maximum for TCP")

// WriteFrame writes one length-prefixed frame.
//
// A single Write of one buffer rather than two, so a frame cannot be torn
// across a partial write with the length already sent — which would desynchronise
// the stream permanently rather than dropping one packet.
func WriteFrame(w io.Writer, frame []byte) error {
	if len(frame) > maxFrame {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooBig, len(frame))
	}
	buf := make([]byte, lenPrefix+len(frame))
	binary.BigEndian.PutUint16(buf, uint16(len(frame)))
	copy(buf[lenPrefix:], frame)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one length-prefixed frame into buf and returns the slice
// holding it.
//
// buf must be at least maxFrame bytes. Reusing the caller's buffer keeps this
// off the allocator on a path that runs per packet.
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	if len(buf) < maxFrame {
		return nil, fmt.Errorf("read buffer is %d bytes, need %d", len(buf), maxFrame)
	}
	var hdr [lenPrefix]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 {
		// A zero-length frame carries nothing and would be indistinguishable
		// from a stall. Used as a keepalive, so it is not an error — the caller
		// gets an empty slice and skips it.
		return buf[:0], nil
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Keepalive is a zero-length frame.
//
// Something has to cross an idle connection, because the middleboxes between a
// device and a relay drop idle TCP state as readily as they drop idle UDP
// mappings, and a connection nobody has written to looks alive from this end
// until the moment it is needed.
var Keepalive = []byte{0, 0}
