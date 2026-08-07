package wg

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
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
	orig := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i] = buf
		orig[i] = buf
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
	// Read back through the ORIGINAL buffers, as wireguard-go does: it is handed
	// `bufs` but reads `bufsArrs[i][:size]`. A demux that rearranges slice
	// headers rather than bytes looks correct from the caller's view and hands
	// WireGuard the wrong buffer. Reading packets[i] here hid exactly that.
	for i := 0; i < n; i++ {
		kept = append(kept, append([]byte(nil), orig[i][:sizes[i]]...))
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

// runDemuxInject feeds a batch through the demux with a handler that UNWRAPS
// control packets back into the stream, as relayed traffic does.
//
// This is the path nothing tested. The consume path only ever shrinks the
// batch; injection rewrites buffers, sizes and endpoints in place while other
// entries are being compacted around it.
func runDemuxInject(t *testing.T, batch [][]byte) (kept [][]byte, keptEps []string) {
	t.Helper()

	b := &Bind{}
	b.SetControlHandler(func(_ Sub, payload []byte, ep conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		// Mirrors handleRelayFrame: the returned slice aliases the packet
		// buffer the demux handed us.
		return payload, fakeEndpoint{id: "relayed-" + ep.DstToString()}, true
	})

	packets := make([][]byte, len(batch))
	orig := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i] = buf
		orig[i] = buf
		sizes[i] = len(p)
		eps[i] = fakeEndpoint{id: string(rune('a' + i))}
	}

	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) { return len(batch), nil }

	n, err := b.demux(inner)(packets, sizes, eps)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	// Read back through the ORIGINAL buffers, as wireguard-go does: it is handed
	// `bufs` but reads `bufsArrs[i][:size]`. A demux that rearranges slice
	// headers rather than bytes looks correct from the caller's view and hands
	// WireGuard the wrong buffer. Reading packets[i] here hid exactly that.
	for i := 0; i < n; i++ {
		kept = append(kept, append([]byte(nil), orig[i][:sizes[i]]...))
		keptEps = append(keptEps, eps[i].DstToString())
	}
	return kept, keptEps
}

// Every interleaving of data and unwrapped-control packets in one batch must
// produce exactly the right payloads, with the right endpoints, and above all
// must never hand WireGuard a packet still carrying our magic prefix — that is
// what "Received message with unknown type" looks like from the far side.
func TestDemuxInjectionPreservesEveryPacket(t *testing.T) {
	data1 := wgPacket(1, 0x11)
	data2 := wgPacket(4, 0x22)
	// An unwrapped relay payload is itself a WireGuard packet.
	inner1 := wgPacket(2, 0x33)
	inner2 := wgPacket(3, 0x44)
	wrap := func(inner []byte) []byte {
		return append(append(Magic[:], byte(SubRelay)), inner...)
	}

	cases := []struct {
		name  string
		batch [][]byte
		want  [][]byte
	}{
		{"relay first", [][]byte{wrap(inner1), data1}, [][]byte{inner1, data1}},
		{"data first", [][]byte{data1, wrap(inner1)}, [][]byte{data1, inner1}},
		{"relay sandwiched", [][]byte{data1, wrap(inner1), data2}, [][]byte{data1, inner1, data2}},
		{"two relays", [][]byte{wrap(inner1), wrap(inner2)}, [][]byte{inner1, inner2}},
		{"relays around data", [][]byte{wrap(inner1), data1, wrap(inner2)}, [][]byte{inner1, data1, inner2}},
		{"data around relays", [][]byte{data1, wrap(inner1), wrap(inner2), data2}, [][]byte{data1, inner1, inner2, data2}},
		{"all relays", [][]byte{wrap(inner1), wrap(inner2), wrap(inner1)}, [][]byte{inner1, inner2, inner1}},
	}

	for _, c := range cases {
		kept, _ := runDemuxInject(t, c.batch)
		if len(kept) != len(c.want) {
			t.Errorf("%s: kept %d packets, want %d", c.name, len(kept), len(c.want))
			continue
		}
		for i := range c.want {
			if !bytes.Equal(kept[i], c.want[i]) {
				t.Errorf("%s: packet %d = % x, want % x", c.name, i, kept[i], c.want[i])
			}
			if isControl(kept[i]) {
				t.Errorf("%s: packet %d still carries the control magic — WireGuard will call this an unknown type", c.name, i)
			}
		}
	}
}

// An unwrapped packet must be attributed to the endpoint the handler chose (the
// relay endpoint), and a data packet to the address it actually came from.
// Getting this crossed sends replies to the wrong place.
func TestDemuxInjectionAttributesEndpoints(t *testing.T) {
	inner := wgPacket(2, 0x99)
	wrap := append(append(Magic[:], byte(SubRelay)), inner...)

	_, eps := runDemuxInject(t, [][]byte{wgPacket(1, 0x11), wrap, wgPacket(4, 0x22)})
	want := []string{"a", "relayed-b", "c"}
	if len(eps) != len(want) {
		t.Fatalf("got %d endpoints, want %d", len(eps), len(want))
	}
	for i := range want {
		if eps[i] != want[i] {
			t.Errorf("packet %d attributed to %q, want %q", i, eps[i], want[i])
		}
	}
}

// Packets WireGuard will reject must be counted and attributed. wireguard-go
// logs "Received message with unknown type" without a source, which is exactly
// why a real occurrence could not be traced.
func TestDemuxCountsUnknownPackets(t *testing.T) {
	b := &Bind{}
	b.SetControlHandler(func(_ Sub, _ []byte, _ conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		return nil, nil, false
	})

	batch := [][]byte{
		wgPacket(1, 0xaa),              // valid
		{0x6d, 0x76, 0x00, 0x00, 0x01}, // near-magic, not ours, not WireGuard
		ctrlPacket(0x01),               // ours, consumed
		wgPacket(4, 0xbb),              // valid
		{0x99, 0x00, 0x00, 0x00},       // bogus type
	}

	packets := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i], sizes[i] = buf, len(p)
		eps[i] = fakeEndpoint{id: string(rune('a' + i))}
	}

	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) { return len(batch), nil }
	if _, err := b.demux(inner)(packets, sizes, eps); err != nil {
		t.Fatalf("demux: %v", err)
	}

	n, last := b.Unknown()
	if n != 2 {
		t.Errorf("counted %d unknown packets, want 2", n)
	}
	// The most recent one, with enough to identify it.
	if !strings.Contains(last, "e") || !strings.Contains(last, "99") {
		t.Errorf("last unknown = %q, want the source and leading bytes of the bogus packet", last)
	}
}

// A healthy stream must not be reported as unknown, or the signal is worthless.
func TestDemuxCountsNoUnknownOnCleanTraffic(t *testing.T) {
	b := &Bind{}
	b.SetControlHandler(func(_ Sub, _ []byte, _ conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		return nil, nil, false
	})
	batch := [][]byte{wgPacket(1), wgPacket(2), wgPacket(3), wgPacket(4), ctrlPacket(0x01)}

	packets := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i], sizes[i] = buf, len(p)
		eps[i] = fakeEndpoint{id: "x"}
	}
	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) { return len(batch), nil }
	if _, err := b.demux(inner)(packets, sizes, eps); err != nil {
		t.Fatalf("demux: %v", err)
	}
	if n, _ := b.Unknown(); n != 0 {
		t.Errorf("counted %d unknown packets on clean traffic, want 0", n)
	}
}

// The message type is a little-endian uint32, not a byte. Checking only the
// first byte passed packets wireguard-go rejects, which is why the counter read
// zero while sixty were being dropped.
func TestLooksLikeWireGuardChecksFullType(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		want bool
	}{
		{"initiation", []byte{1, 0, 0, 0}, true},
		{"response", []byte{2, 0, 0, 0}, true},
		{"cookie", []byte{3, 0, 0, 0}, true},
		{"data", []byte{4, 0, 0, 0}, true},
		{"type 0", []byte{0, 0, 0, 0}, false},
		{"type 5", []byte{5, 0, 0, 0}, false},
		// Valid first byte, rubbish above it: the case the byte-only check missed.
		{"high bytes set", []byte{1, 0, 1, 0}, false},
		{"high byte set", []byte{2, 0, 0, 8}, false},
		{"too short", []byte{1, 0}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := looksLikeWireGuard(c.pkt); got != c.want {
			t.Errorf("%s: looksLikeWireGuard(% x) = %v, want %v", c.name, c.pkt, got, c.want)
		}
	}
}

// Regression: compaction must move BYTES, not slice headers.
//
// wireguard-go hands us `bufs` but reads the packet back from `bufsArrs`
// (device/receive.go: `recv(bufs, ...)` then `packet := bufsArrs[i][:size]`).
// The two alias on entry, so swapping slice headers rearranges only our view —
// WireGuard goes on reading bufsArrs[kept], which holds the control packet we
// just consumed, with the surviving packet's length.
//
// The result is a packet whose first four bytes are our magic, which WireGuard
// reports as "Received message with unknown type" while the real packet is
// lost. It cost six minutes per reconnect, and it only bites when a control
// packet precedes a data packet in the same batch — which is most batches,
// since disco and WireGuard share one socket.
func TestDemuxCompactionMovesBytesNotHeaders(t *testing.T) {
	// Control first, so the data packet must be compacted down to index 0.
	batch := [][]byte{ctrlPacket(0xde, 0xad), wgPacket(1, 0xbe, 0xef)}

	packets := make([][]byte, len(batch))
	orig := make([][]byte, len(batch))
	sizes := make([]int, len(batch))
	eps := make([]conn.Endpoint, len(batch))
	for i, p := range batch {
		buf := make([]byte, 1500)
		copy(buf, p)
		packets[i], orig[i], sizes[i] = buf, buf, len(p)
		eps[i] = fakeEndpoint{id: string(rune('a' + i))}
	}

	b := &Bind{}
	b.SetControlHandler(func(_ Sub, _ []byte, _ conn.Endpoint) ([]byte, conn.Endpoint, bool) {
		return nil, nil, false
	})
	inner := func(_ [][]byte, _ []int, _ []conn.Endpoint) (int, error) { return len(batch), nil }

	n, err := b.demux(inner)(packets, sizes, eps)
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	if n != 1 {
		t.Fatalf("kept %d packets, want 1", n)
	}

	// Read exactly as wireguard-go does.
	got := orig[0][:sizes[0]]
	if isControl(got) {
		t.Fatalf("WireGuard would read a control packet: % x — this is the "+
			"\"unknown type\" failure", got)
	}
	if !bytes.Equal(got, wgPacket(1, 0xbe, 0xef)) {
		t.Errorf("WireGuard would read % x, want % x", got, wgPacket(1, 0xbe, 0xef))
	}
}
