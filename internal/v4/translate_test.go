package v4

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

var (
	self6 = netip.MustParseAddr("fd93:ce1e:8c57:20bc:cb09:eb53:2f8b:22a8")
	peer6 = netip.MustParseAddr("fd93:ce1e:8c57:a332:855c:1059:8060:59d7")
	self4 = netip.MustParseAddr("198.18.0.10")
	peer4 = netip.MustParseAddr("198.19.3.4")
)

// tcp4 builds a plausible IPv4 TCP packet with a correct checksum.
func tcp4(t *testing.T, src, dst netip.Addr, payload []byte, syn bool, mss uint16) []byte {
	t.Helper()
	tcp := make([]byte, 24+len(payload)) // 20 + one MSS option, padded to 4
	binary.BigEndian.PutUint16(tcp[0:2], 51000)
	binary.BigEndian.PutUint16(tcp[2:4], 443)
	tcp[12] = 6 << 4 // data offset: 24 bytes
	if syn {
		tcp[13] = tcpFlagSyn
	}
	binary.BigEndian.PutUint16(tcp[14:16], 65535)
	tcp[20], tcp[21] = tcpOptionMSS, tcpOptionMSSLen
	binary.BigEndian.PutUint16(tcp[22:24], mss)
	copy(tcp[24:], payload)

	pkt := make([]byte, v4HeaderLen+len(tcp))
	pkt[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = protoTCP
	s, d := src.As4(), dst.As4()
	copy(pkt[12:16], s[:])
	copy(pkt[16:20], d[:])
	putChecksum(pkt[:v4HeaderLen], 10, checksum(pkt[:v4HeaderLen]))
	copy(pkt[v4HeaderLen:], tcp)
	putChecksum(pkt[v4HeaderLen:], 16, tcpUDPChecksum4(s[:], d[:], protoTCP, pkt[v4HeaderLen:]))
	return pkt
}

// verify6 checks a translated IPv6 packet the way a receiver would.
func verify6(t *testing.T, pkt []byte) {
	t.Helper()
	if pkt[0]>>4 != 6 {
		t.Fatalf("not IPv6: version %d", pkt[0]>>4)
	}
	length := int(binary.BigEndian.Uint16(pkt[4:6]))
	if v6HeaderLen+length != len(pkt) {
		t.Fatalf("payload length %d does not match packet %d", length, len(pkt))
	}
	// On a copy: tcpUDPChecksum zeroes the field it computes, so verifying in
	// place would quietly destroy the packet for whatever the test does next.
	body := append([]byte(nil), pkt[v6HeaderLen:]...)
	proto := pkt[6]
	at := 16
	switch proto {
	case protoUDP:
		at = 6
	case protoICMPv6:
		at = 2
	}
	got := binary.BigEndian.Uint16(body[at : at+2])
	want := tcpUDPChecksum(pkt[8:24], pkt[24:40], proto, body)
	if got != want {
		t.Errorf("transport checksum is %#04x, a receiver computes %#04x", got, want)
	}
}

// verify4 checks a translated IPv4 packet the way a receiver would.
func verify4(t *testing.T, pkt []byte) {
	t.Helper()
	if pkt[0]>>4 != 4 {
		t.Fatalf("not IPv4: version %d", pkt[0]>>4)
	}
	hdr := append([]byte(nil), pkt[:v4HeaderLen]...)
	got := binary.BigEndian.Uint16(hdr[10:12])
	putChecksum(hdr, 10, 0)
	if want := checksum(hdr); got != want {
		t.Errorf("header checksum is %#04x, want %#04x", got, want)
	}
	if int(binary.BigEndian.Uint16(pkt[2:4])) != len(pkt) {
		t.Errorf("total length %d, packet %d", binary.BigEndian.Uint16(pkt[2:4]), len(pkt))
	}

	body := append([]byte(nil), pkt[v4HeaderLen:]...)
	proto := pkt[9]
	if proto == protoICMP {
		sum := binary.BigEndian.Uint16(body[2:4])
		cp := append([]byte(nil), body...)
		putChecksum(cp, 2, 0)
		if want := checksum(cp); sum != want {
			t.Errorf("icmp checksum is %#04x, want %#04x", sum, want)
		}
		return
	}
	at := 16
	if proto == protoUDP {
		at = 6
	}
	got = binary.BigEndian.Uint16(body[at : at+2])
	want := tcpUDPChecksum4(pkt[12:16], pkt[16:20], proto, body)
	if got != want {
		t.Errorf("transport checksum is %#04x, a receiver computes %#04x", got, want)
	}
}

// The round trip is what matters: a browser's packet must come out the far end
// byte-identical, or this is a very expensive way to corrupt traffic.
func TestTCPRoundTrip(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: ha.jimmy-crib.mesh\r\n\r\n")
	orig := tcp4(t, self4, peer4, payload, false, 1460)

	six := To6(orig, self6, peer6, 0)
	if six == nil {
		t.Fatal("refused a plain TCP packet")
	}
	verify6(t, six)
	if !bytes.Equal(six[8:24], addr16(self6)) || !bytes.Equal(six[24:40], addr16(peer6)) {
		t.Error("addresses were not rewritten")
	}

	back := To4(six, self4, peer4)
	if back == nil {
		t.Fatal("refused its own output")
	}
	verify4(t, back)
	if !bytes.Equal(back[v4HeaderLen+24:], payload) {
		t.Error("payload did not survive")
	}
	if !bytes.Equal(back[12:16], addr4(self4)) || !bytes.Equal(back[16:20], addr4(peer4)) {
		t.Error("addresses were not rewritten back")
	}
}

// The v6 header is 20 bytes bigger, so a segment sized for the v4 MTU no longer
// fits. Left alone, that is a connection that opens and then hangs on the first
// full-size response — the worst kind of bug to find later.
func TestMSSIsClamped(t *testing.T) {
	syn := tcp4(t, self4, peer4, nil, true, 1460)
	six := To6(syn, self6, peer6, 1360)
	if six == nil {
		t.Fatal("refused a SYN")
	}
	verify6(t, six)
	if got := binary.BigEndian.Uint16(six[v6HeaderLen+22 : v6HeaderLen+24]); got != 1360 {
		t.Errorf("MSS is %d, want it clamped to 1360", got)
	}

	// A SYN already below the limit is left alone.
	small := tcp4(t, self4, peer4, nil, true, 800)
	six = To6(small, self6, peer6, 1360)
	if got := binary.BigEndian.Uint16(six[v6HeaderLen+22 : v6HeaderLen+24]); got != 800 {
		t.Errorf("MSS is %d, want 800 left alone", got)
	}

	// And a packet that is not a SYN carries no MSS to clamp.
	data := tcp4(t, self4, peer4, []byte("x"), false, 1460)
	six = To6(data, self6, peer6, 1360)
	if got := binary.BigEndian.Uint16(six[v6HeaderLen+22 : v6HeaderLen+24]); got != 1460 {
		t.Errorf("clamped a non-SYN: %d", got)
	}
}

func TestUDPRoundTrip(t *testing.T) {
	udp := make([]byte, 8+5)
	binary.BigEndian.PutUint16(udp[0:2], 40000)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], "hello")

	pkt := make([]byte, v4HeaderLen+len(udp))
	pkt[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = protoUDP
	s, d := self4.As4(), peer4.As4()
	copy(pkt[12:16], s[:])
	copy(pkt[16:20], d[:])
	putChecksum(pkt[:v4HeaderLen], 10, checksum(pkt[:v4HeaderLen]))
	copy(pkt[v4HeaderLen:], udp)
	putChecksum(pkt[v4HeaderLen:], 6, tcpUDPChecksum4(s[:], d[:], protoUDP, pkt[v4HeaderLen:]))

	six := To6(pkt, self6, peer6, 0)
	if six == nil {
		t.Fatal("refused UDP")
	}
	verify6(t, six)
	// UDP over IPv6 may not carry a zero checksum.
	if binary.BigEndian.Uint16(six[v6HeaderLen+6:v6HeaderLen+8]) == 0 {
		t.Error("wrote a zero UDP checksum, which is illegal over IPv6")
	}

	back := To4(six, self4, peer4)
	if back == nil {
		t.Fatal("refused its own output")
	}
	verify4(t, back)
	if !bytes.Equal(back[v4HeaderLen+8:], []byte("hello")) {
		t.Error("payload did not survive")
	}
}

// ping is the first thing anyone tries when a network looks broken.
func TestICMPEcho(t *testing.T) {
	icmp := make([]byte, 8)
	icmp[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(icmp[4:6], 1234) // id
	putChecksum(icmp, 2, checksum(icmp))

	pkt := make([]byte, v4HeaderLen+len(icmp))
	pkt[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = protoICMP
	s, d := self4.As4(), peer4.As4()
	copy(pkt[12:16], s[:])
	copy(pkt[16:20], d[:])
	putChecksum(pkt[:v4HeaderLen], 10, checksum(pkt[:v4HeaderLen]))
	copy(pkt[v4HeaderLen:], icmp)

	six := To6(pkt, self6, peer6, 0)
	if six == nil {
		t.Fatal("refused an echo request")
	}
	if six[v6HeaderLen] != icmp6EchoRequest {
		t.Errorf("type is %d, want %d", six[v6HeaderLen], icmp6EchoRequest)
	}
	verify6(t, six)

	// And the reply on the way back.
	six[v6HeaderLen] = icmp6EchoReply
	putChecksum(six[v6HeaderLen:], 2, tcpUDPChecksum(six[8:24], six[24:40], protoICMPv6, six[v6HeaderLen:]))
	back := To4(six, peer4, self4)
	if back == nil {
		t.Fatal("refused an echo reply")
	}
	if back[v4HeaderLen] != icmpEchoReply {
		t.Errorf("type is %d, want %d", back[v4HeaderLen], icmpEchoReply)
	}
	verify4(t, back)
}

// Anything we cannot translate correctly must be refused, not passed through:
// an IPv4 packet on an IPv6-only overlay is worse than a dropped one.
func TestRefuses(t *testing.T) {
	frag := tcp4(t, self4, peer4, []byte("x"), false, 1460)
	binary.BigEndian.PutUint16(frag[6:8], 0x2000) // more fragments
	if To6(frag, self6, peer6, 0) != nil {
		t.Error("translated a fragment")
	}

	odd := tcp4(t, self4, peer4, nil, false, 1460)
	odd[9] = 47 // GRE
	if To6(odd, self6, peer6, 0) != nil {
		t.Error("translated a protocol with a checksum we do not fix")
	}

	for _, short := range [][]byte{nil, make([]byte, 10), make([]byte, v4HeaderLen)} {
		if To6(short, self6, peer6, 0) != nil {
			t.Errorf("translated %d bytes of nothing", len(short))
		}
		if To4(short, self4, peer4) != nil {
			t.Errorf("translated %d bytes of nothing back", len(short))
		}
	}
}

func addr16(a netip.Addr) []byte { b := a.As16(); return b[:] }
func addr4(a netip.Addr) []byte  { b := a.As4(); return b[:] }
