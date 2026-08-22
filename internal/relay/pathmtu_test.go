package relay

import (
	"errors"
	"net/netip"
	"testing"
	"time"
)

// A relay reports the size it actually received, which is the only figure worth
// having: what the sender intended proves nothing about what the path carried.
func TestARelayEchoesTheSizeItSaw(t *testing.T) {
	s, k := blindServer(t, Options{})
	from := netip.MustParseAddrPort("198.51.100.10:51820")
	id, err := NewProbeID()
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := EncodeMTUProbe(k, id, 900)
	if err != nil {
		t.Fatal(err)
	}
	out, to, send := s.Handle(pkt, from, time.Now())
	if !send || to != from {
		t.Fatal("a probe was not answered to its sender")
	}
	f, err := Decode(k, out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeMTUEcho {
		t.Fatalf("got frame type %d, want an echo", f.Type)
	}
	if f.ProbeID != id {
		t.Error("the echo names a different probe")
	}
	if f.Saw != 900 {
		t.Errorf("relay reports seeing %d bytes, sent 900", f.Saw)
	}
	// And the answer is far smaller than the question, so this cannot be used
	// to amplify traffic at somebody.
	if len(out) >= 900 {
		t.Errorf("the echo is %d bytes against a 900-byte probe", len(out))
	}
}

// The search has to find the real limit rather than approach it, since every
// round trip on a phone costs a radio wake.
func TestDiscoveryFindsTheLimit(t *testing.T) {
	for _, limit := range []int{200, 1200, 1265, 1500} {
		k := OpenKey()
		calls := 0
		got, err := DiscoverPathMTU(k, 100, 1500, func(pkt []byte) (*Frame, error) {
			calls++
			if len(pkt) > limit {
				return nil, errors.New("timeout") // silently discarded, as a real path does
			}
			f, derr := decodeMTUProbe(k, pkt)
			if derr != nil {
				return nil, derr
			}
			return Decode(k, EncodeMTUEcho(k, f.ProbeID, f.Saw))
		})
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if got != limit {
			t.Errorf("limit %d: discovered %d", limit, got)
		}
		// log2(1400) is about 11, and a walk would be hundreds.
		if calls > 14 {
			t.Errorf("limit %d took %d round trips", limit, calls)
		}
	}
}

// A relay that answers nothing — unreachable, or too old to know these frames —
// must report zero rather than a number, so the caller falls back to assuming
// the worst instead of to assuming anything.
func TestNothingAnsweringReportsZero(t *testing.T) {
	got, err := DiscoverPathMTU(OpenKey(), 100, 1500, func([]byte) (*Frame, error) {
		return nil, errors.New("timeout")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("discovered %d from a relay that answered nothing", got)
	}
}

// A late echo from an earlier, smaller probe must not be read as a larger one
// succeeding. That mistake would raise the MTU above what the path carries and
// fail silently later, which is the failure this whole mechanism exists to
// remove.
func TestAMismatchedEchoIsNotAPass(t *testing.T) {
	k := OpenKey()
	stale, err := NewProbeID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverPathMTU(k, 100, 1500, func(pkt []byte) (*Frame, error) {
		// Always answers, but always about a different probe.
		return Decode(k, EncodeMTUEcho(k, stale, len(pkt)))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("accepted an echo naming a different probe: %d", got)
	}
}

// A path that truncates rather than discarding must not count as carrying the
// size. It arrived, but not whole, and half a packet is not a smaller packet.
func TestATruncatedProbeIsNotAPass(t *testing.T) {
	k := OpenKey()
	got, err := DiscoverPathMTU(k, 100, 1500, func(pkt []byte) (*Frame, error) {
		f, err := decodeMTUProbe(k, pkt)
		if err != nil {
			return nil, err
		}
		// Reports seeing ten bytes fewer than were sent.
		return Decode(k, EncodeMTUEcho(k, f.ProbeID, f.Saw-10))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("a truncating path was reported as carrying %d", got)
	}
}

// Padding is random rather than zeroes, so a compressing link cannot report
// carrying more than it will for real traffic — which is WireGuard, and
// indistinguishable from random by design.
func TestProbePaddingIsNotCompressible(t *testing.T) {
	id, _ := NewProbeID()
	a, err := EncodeMTUProbe(OpenKey(), id, 600)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeMTUProbe(OpenKey(), id, 600)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatal("two probes of the same size are identical, so the padding is fixed")
	}
	zeroes := 0
	for _, c := range a[mtuProbeHeaderLen-macLen:] {
		if c == 0 {
			zeroes++
		}
	}
	if zeroes > len(a)/4 {
		t.Errorf("%d of %d padding bytes are zero", zeroes, len(a))
	}
}

// A probe too small to hold its own header is refused rather than producing
// something malformed.
func TestATooSmallProbeIsRefused(t *testing.T) {
	id, _ := NewProbeID()
	if _, err := EncodeMTUProbe(OpenKey(), id, 4); !errors.Is(err, ErrProbeTooSmall) {
		t.Errorf("got %v, want ErrProbeTooSmall", err)
	}
}
