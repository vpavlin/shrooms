package dns

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
)

var self = netip.MustParseAddr("fd3b:ffe9:f81:773f:a40:67a:5077:6ce9")
var client = netip.MustParseAddr("fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69")

// fakeTun feeds a fixed batch and records what is written back.
type fakeTun struct {
	tun.Device
	batch   [][]byte
	written [][]byte
	done    bool
}

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if f.done {
		return 0, errors.New("eof")
	}
	f.done = true
	for i, p := range f.batch {
		copy(bufs[i][offset:], p)
		sizes[i] = len(p)
	}
	return len(f.batch), nil
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		f.written = append(f.written, append([]byte(nil), b[offset:]...))
	}
	return len(bufs), nil
}

func (f *fakeTun) BatchSize() int { return len(f.batch) }

// udpPacket builds an IPv6/UDP datagram.
func udpPacket(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	udpLen := udpHeaderLen + len(payload)
	pkt := make([]byte, ipv6HeaderLen+udpLen)
	pkt[0] = 6 << 4
	binary.BigEndian.PutUint16(pkt[4:6], uint16(udpLen))
	pkt[6] = protoUDP
	pkt[7] = 64
	copy(pkt[8:24], src.AsSlice())
	copy(pkt[24:40], dst.AsSlice())
	udp := pkt[ipv6HeaderLen:]
	binary.BigEndian.PutUint16(udp[0:2], sport)
	binary.BigEndian.PutUint16(udp[2:4], dport)
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLen))
	copy(udp[udpHeaderLen:], payload)
	binary.BigEndian.PutUint16(udp[6:8], udpChecksum(pkt[8:24], pkt[24:40], udp))
	return pkt
}

func run(t *testing.T, batch [][]byte, answer func([]byte) ([]byte, error)) (*fakeTun, [][]byte) {
	t.Helper()
	const offset = 10

	ft := &fakeTun{batch: batch}
	ic := NewIntercept(ft, self, answer)

	bufs := make([][]byte, len(batch))
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	sizes := make([]int, len(batch))

	n, err := ic.Read(bufs, sizes, offset)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Read back the way wireguard-go does, from the buffers it handed us.
	var kept [][]byte
	for i := 0; i < n; i++ {
		kept = append(kept, append([]byte(nil), bufs[i][offset:offset+sizes[i]]...))
	}
	return ft, kept
}

func TestInterceptAnswersQueryForUs(t *testing.T) {
	query := []byte("QUERY-PAYLOAD")
	reply := []byte("REPLY")
	pkt := udpPacket(client, self, 40000, Port, query)

	ft, kept := run(t, [][]byte{pkt}, func(q []byte) ([]byte, error) {
		if !bytes.Equal(q, query) {
			t.Errorf("handler got %q, want %q", q, query)
		}
		return reply, nil
	})

	if len(kept) != 0 {
		t.Errorf("passed %d packets to WireGuard; the query must be consumed", len(kept))
	}
	if len(ft.written) != 1 {
		t.Fatalf("wrote %d replies, want 1", len(ft.written))
	}

	out := ft.written[0]
	// Addresses and ports must be swapped, or the reply goes nowhere.
	src, _ := netip.AddrFromSlice(out[8:24])
	dst, _ := netip.AddrFromSlice(out[24:40])
	if src != self || dst != client {
		t.Errorf("reply addressed %v -> %v, want %v -> %v", src, dst, self, client)
	}
	udp := out[ipv6HeaderLen:]
	if sport := binary.BigEndian.Uint16(udp[0:2]); sport != Port {
		t.Errorf("reply source port %d, want %d", sport, Port)
	}
	if dport := binary.BigEndian.Uint16(udp[2:4]); dport != 40000 {
		t.Errorf("reply dest port %d, want 40000", dport)
	}
	if got := udp[udpHeaderLen:]; !bytes.Equal(got, reply) {
		t.Errorf("payload %q, want %q", got, reply)
	}
}

// The checksum is mandatory in IPv6. A wrong one is dropped by the receiver
// with no diagnostic at all, so verify it the way a receiver would: the sum
// over the whole datagram including the checksum field must come to zero.
func TestReplyChecksumIsValid(t *testing.T) {
	pkt := udpPacket(client, self, 40000, Port, []byte("Q"))
	ft, _ := run(t, [][]byte{pkt}, func([]byte) ([]byte, error) { return []byte("REPLY-PAYLOAD"), nil })

	out := ft.written[0]
	udp := out[ipv6HeaderLen:]
	if binary.BigEndian.Uint16(udp[6:8]) == 0 {
		t.Fatal("checksum is zero, which is illegal in IPv6")
	}
	if got := udpChecksum(out[8:24], out[24:40], udp); got != 0xffff && got != 0 {
		t.Errorf("checksum does not verify: residual %#04x", got)
	}
}

// Everything not addressed to us must pass through untouched.
func TestInterceptPassesOtherTraffic(t *testing.T) {
	cases := map[string][]byte{
		"to a peer":        udpPacket(self, client, 40000, Port, []byte("Q")),
		"not port 53":      udpPacket(client, self, 40000, 443, []byte("X")),
		"not udp":          func() []byte { p := udpPacket(client, self, 1, Port, []byte("X")); p[6] = 6; return p }(),
		"ipv4-ish garbage": {0x45, 0, 0, 20},
	}
	for name, pkt := range cases {
		ft, kept := run(t, [][]byte{pkt}, func([]byte) ([]byte, error) {
			t.Errorf("%s: answered a packet that is not ours", name)
			return nil, nil
		})
		if len(kept) != 1 {
			t.Errorf("%s: dropped a packet WireGuard needed", name)
		}
		if len(ft.written) != 0 {
			t.Errorf("%s: wrote a reply", name)
		}
	}
}

// Compaction: ordering and contents.
//
// Note what this does NOT prove. Swapping slice headers instead of copying
// passes this test, because the hazard is wireguard-go's internal pairing of
// bufs[i] with elems[i].buffer, which is invisible from the Read contract.
// Verified by trying it. The implementation copies for that reason.
func TestInterceptCompactsByMovingBytes(t *testing.T) {
	q := udpPacket(client, self, 40000, Port, []byte("Q"))
	a := udpPacket(client, self, 40000, 443, []byte("FIRST"))
	b := udpPacket(client, self, 40000, 443, []byte("SECOND"))

	for _, c := range []struct {
		name  string
		batch [][]byte
		want  [][]byte
	}{
		{"query first", [][]byte{q, a, b}, [][]byte{a, b}},
		{"query middle", [][]byte{a, q, b}, [][]byte{a, b}},
		{"query last", [][]byte{a, b, q}, [][]byte{a, b}},
		{"two queries", [][]byte{q, a, q}, [][]byte{a}},
	} {
		_, kept := run(t, c.batch, func([]byte) ([]byte, error) { return []byte("R"), nil })
		if len(kept) != len(c.want) {
			t.Errorf("%s: kept %d packets, want %d", c.name, len(kept), len(c.want))
			continue
		}
		for i := range c.want {
			if !bytes.Equal(kept[i], c.want[i]) {
				t.Errorf("%s: packet %d is the wrong one", c.name, i)
			}
		}
	}
}

// A resolver failure must drop the query, never forward it to WireGuard, which
// would send a packet addressed to ourselves to a peer.
func TestFailedAnswerDropsTheQuery(t *testing.T) {
	pkt := udpPacket(client, self, 40000, Port, []byte("Q"))
	ft, kept := run(t, [][]byte{pkt}, func([]byte) ([]byte, error) {
		return nil, errors.New("resolver failed")
	})
	if len(kept) != 0 {
		t.Error("passed an unanswerable query to WireGuard")
	}
	if len(ft.written) != 0 {
		t.Error("wrote a reply for a failed lookup")
	}
}

func TestTruncatedPacketsAreNotAnswered(t *testing.T) {
	for _, pkt := range [][]byte{{}, {6 << 4}, make([]byte, ipv6HeaderLen)} {
		_, kept := run(t, [][]byte{pkt}, func([]byte) ([]byte, error) {
			t.Error("answered a truncated packet")
			return nil, nil
		})
		if len(kept) != 1 {
			t.Error("dropped a short packet instead of passing it on")
		}
	}
}
