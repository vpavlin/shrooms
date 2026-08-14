package v4

import (
	"net/netip"
	"testing"
)

// A device on two meshes holds one alias per mesh, and nothing obliges the
// operating system to send from the one belonging to the mesh it is addressing.
// When it does not, the reply used to be addressed to our own alias anyway — so
// it arrived at an address the socket was not bound to, the kernel dropped it,
// and the connection hung until it timed out. Everything else looked healthy:
// the name resolved, the packet was translated, the peer answered.
func TestReplyGoesBackToTheAddressItCameFrom(t *testing.T) {
	peer := netip.MustParseAddr("fd7b:15fb:5ec1:f228:4593:972d:ae56:9952")
	self := netip.MustParseAddr("fd7b:15fb:5ec1:c1c8:8395:b471:36d9:d655")

	tbl := NewTableIn(netip.MustParsePrefix("198.18.32.0/19"),
		Entry{Overlay: self, DevicePub: []byte("self-device-key")}, nil)
	tbl.Update([]Entry{{Overlay: peer, DevicePub: []byte("peer-device-key")}})

	alias, ok := tbl.Alias(peer)
	if !ok {
		t.Fatal("the peer has no alias")
	}
	d := &Device{table: tbl, flows: make(map[flowKey]flow)}

	// The OS sends from the OTHER mesh's alias, which is not tbl.Self().
	other := netip.MustParseAddr("198.19.225.254")
	if other == tbl.Self() {
		t.Fatal("the test needs a source that is not this mesh's own alias")
	}

	out := d.toOverlay(synTCP(other, alias, 44001, 80))
	if out == nil {
		t.Fatal("the outbound packet was not translated")
	}

	back := d.toAlias(synAckTCP(peer, self, 80, 44001))
	if back == nil {
		t.Fatal("the reply was not translated back")
	}
	dst, _ := netip.AddrFromSlice(back[16:20])
	if dst != other {
		t.Errorf("reply addressed to %v; the connection was opened from %v", dst, other)
	}
}

// A flow opened from our own alias — the ordinary single-mesh case — still comes
// back to it.
func TestOwnAliasStillWorks(t *testing.T) {
	peer := netip.MustParseAddr("fd7b:15fb:5ec1:f228:4593:972d:ae56:9952")
	self := netip.MustParseAddr("fd7b:15fb:5ec1:c1c8:8395:b471:36d9:d655")

	tbl := NewTableIn(netip.MustParsePrefix("198.18.32.0/19"),
		Entry{Overlay: self, DevicePub: []byte("self-device-key")}, nil)
	tbl.Update([]Entry{{Overlay: peer, DevicePub: []byte("peer-device-key")}})
	alias, _ := tbl.Alias(peer)
	d := &Device{table: tbl, flows: make(map[flowKey]flow)}

	if d.toOverlay(synTCP(tbl.Self(), alias, 44002, 80)) == nil {
		t.Fatal("the outbound packet was not translated")
	}
	back := d.toAlias(synAckTCP(peer, self, 80, 44002))
	if back == nil {
		t.Fatal("the reply was not translated back")
	}
	dst, _ := netip.AddrFromSlice(back[16:20])
	if dst != tbl.Self() {
		t.Errorf("reply addressed to %v, want %v", dst, tbl.Self())
	}
}

// synTCP builds a minimal IPv4 TCP packet.
func synTCP(src, dst netip.Addr, sport, dport uint16) []byte {
	pkt := make([]byte, v4HeaderLen+20)
	pkt[0] = 4<<4 | 5
	pkt[2], pkt[3] = byte(len(pkt)>>8), byte(len(pkt))
	pkt[8] = 64
	pkt[9] = protoTCP
	copy(pkt[12:16], src.AsSlice())
	copy(pkt[16:20], dst.AsSlice())
	tcp := pkt[v4HeaderLen:]
	tcp[0], tcp[1] = byte(sport>>8), byte(sport)
	tcp[2], tcp[3] = byte(dport>>8), byte(dport)
	tcp[12] = 5 << 4
	return pkt
}

// synAckTCP builds a minimal IPv6 TCP packet coming the other way.
func synAckTCP(src, dst netip.Addr, sport, dport uint16) []byte {
	pkt := make([]byte, v6HeaderLen+20)
	pkt[0] = 6 << 4
	pkt[4], pkt[5] = 0, 20
	pkt[6] = protoTCP
	pkt[7] = 64
	copy(pkt[8:24], src.AsSlice())
	copy(pkt[24:40], dst.AsSlice())
	tcp := pkt[v6HeaderLen:]
	tcp[0], tcp[1] = byte(sport>>8), byte(sport)
	tcp[2], tcp[3] = byte(dport>>8), byte(dport)
	tcp[12] = 5 << 4
	return pkt
}
