package wg

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"

	"golang.zx2c4.com/wireguard/conn"
)

// fakeEndpoint is a minimal conn.Endpoint for tests.
type fakeEndpoint struct{ id string }

func (f fakeEndpoint) ClearSrc()             {}
func (f fakeEndpoint) SrcToString() string   { return f.id }
func (f fakeEndpoint) DstToString() string   { return f.id }
func (f fakeEndpoint) DstToBytes() []byte    { return []byte(f.id) }
func (f fakeEndpoint) DstIP() (a netip.Addr) { return }
func (f fakeEndpoint) SrcIP() (a netip.Addr) { return }

// wgPacket builds a packet that looks like WireGuard to the demux:
// first byte in 0x01..0x04, next three zero.
func wgPacket(typ byte, body ...byte) []byte {
	return append([]byte{typ, 0, 0, 0}, body...)
}

// ctrlPacket builds one of ours: magic, sub-protocol byte, then the body.
func ctrlPacket(body ...byte) []byte {
	return append(append(Magic[:], byte(SubDisco)), body...)
}

// runDemux feeds a fixed batch through the demux and reports what survived and
// what was handed to the control handler.
func runDemux(t *testing.T, batch [][]byte) (kept [][]byte, ctrl [][]byte, keptEps []string) {
	t.Helper()

	b := &Bind{}
	b.SetControlHandler(func(_ Sub, payload []byte, ep conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		ctrl = append(ctrl, append([]byte(nil), payload...))
		return nil, nil, false
	})

	packets := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i] = buf
		sizes[i] = len(p)
		eps[i] = fakeEndpoint{id: string(rune('a' + i))}
	}

	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) {
		return len(batch), nil
	}

	n, err := b.demux(inner)(packets, sizes, eps)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	for i := 0; i < n; i++ {
		kept = append(kept, append([]byte(nil), packets[i][:sizes[i]]...))
		keptEps = append(keptEps, eps[i].DstToString())
	}
	return kept, ctrl, keptEps
}

func TestDemuxSeparatesControlFromWireGuard(t *testing.T) {
	kept, ctrl, _ := runDemux(t, [][]byte{
		wgPacket(1, 0xaa),
		ctrlPacket(0x01, 0x02),
		wgPacket(4, 0xbb),
		ctrlPacket(0x03),
	})

	if len(kept) != 2 {
		t.Fatalf("kept %d wireguard packets, want 2: %v", len(kept), kept)
	}
	if len(ctrl) != 2 {
		t.Fatalf("got %d control packets, want 2: %v", len(ctrl), ctrl)
	}
	if !bytes.Equal(kept[0], wgPacket(1, 0xaa)) || !bytes.Equal(kept[1], wgPacket(4, 0xbb)) {
		t.Errorf("wireguard packets corrupted by compaction: %v", kept)
	}
	// Magic must be stripped before the handler sees it.
	if !bytes.Equal(ctrl[0], []byte{0x01, 0x02}) || !bytes.Equal(ctrl[1], []byte{0x03}) {
		t.Errorf("control payloads wrong: %v", ctrl)
	}
}

// Compaction must keep packets, sizes and eps aligned. A bug here silently
// attributes a packet to the wrong peer, which is the kind of thing that only
// shows up as a baffling handshake failure later.
func TestDemuxKeepsEndpointsAligned(t *testing.T) {
	_, _, eps := runDemux(t, [][]byte{
		ctrlPacket(0x00),  // a — removed
		wgPacket(1, 0x11), // b — kept
		ctrlPacket(0x00),  // c — removed
		wgPacket(2, 0x22), // d — kept
	})
	if len(eps) != 2 || eps[0] != "b" || eps[1] != "d" {
		t.Fatalf("endpoints misaligned after compaction: %v", eps)
	}
}

func TestDemuxAllControlYieldsNothingForWireGuard(t *testing.T) {
	kept, ctrl, _ := runDemux(t, [][]byte{ctrlPacket(1), ctrlPacket(2), ctrlPacket(3)})
	if len(kept) != 0 {
		t.Fatalf("kept %d packets, want 0", len(kept))
	}
	if len(ctrl) != 3 {
		t.Fatalf("got %d control packets, want 3", len(ctrl))
	}
}

func TestDemuxPassesThroughWhenNoControl(t *testing.T) {
	batch := [][]byte{wgPacket(1), wgPacket(2), wgPacket(3), wgPacket(4)}
	kept, ctrl, _ := runDemux(t, batch)
	if len(kept) != 4 {
		t.Fatalf("kept %d packets, want 4", len(kept))
	}
	if len(ctrl) != 0 {
		t.Fatalf("got %d control packets, want 0", len(ctrl))
	}
}

// A short packet must not be mistaken for control, and must not panic. Note a
// bare magic with no sub-protocol byte is too short to be valid.
func TestIsControlShortPacket(t *testing.T) {
	for _, p := range [][]byte{{}, {0x6d}, {0x6d, 0x76}, {0x6d, 0x76, 0x70}, {0x6d, 0x76, 0x70, 0x6e}} {
		if isControl(p) {
			t.Errorf("isControl(%v) = true, want false", p)
		}
	}
}

// Our magic must not collide with the discriminators Tailscale uses, so the
// three protocols remain separable on the first two bytes.
func TestMagicDoesNotCollide(t *testing.T) {
	if Magic[0] >= 0x01 && Magic[0] <= 0x04 {
		t.Errorf("magic[0]=%#x collides with the WireGuard message-type range", Magic[0])
	}
	if Magic[0] == 0x54 {
		t.Errorf("magic[0]=%#x collides with Tailscale disco", Magic[0])
	}
	// STUN binding requests carry msg[1]==0x01 with the cookie at offset 4.
	if Magic[1] == 0x01 {
		t.Errorf("magic[1]=%#x risks confusion with STUN", Magic[1])
	}
}

func TestDemuxPropagatesInnerError(t *testing.T) {
	b := &Bind{}
	want := errors.New("boom")
	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) { return 0, want }
	if _, err := b.demux(inner)(nil, nil, nil); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
