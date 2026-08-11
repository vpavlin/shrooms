package portmap

import (
	"context"
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// These tests exercise the parsers against hand-built packets and never open a
// socket. A router that answers is exactly the thing that cannot be assumed in
// CI, and the parsers are where every field-order and endianness mistake in
// these two protocols actually lands.

// natpmpExternalResp builds an opcode 128 (external address) response.
func natpmpExternalResp(code uint16, addr netip.Addr) []byte {
	pkt := make([]byte, natpmpExternalRespLen)
	pkt[0] = natpmpVersion
	pkt[1] = natpmpResponseBit + natpmpOpExternal
	binary.BigEndian.PutUint16(pkt[2:4], code)
	binary.BigEndian.PutUint32(pkt[4:8], 12345) // epoch, deliberately ignored
	if addr.Is4() {
		b := addr.As4()
		copy(pkt[8:12], b[:])
	}
	return pkt
}

// natpmpMapResp builds an opcode 129 (map UDP) response.
func natpmpMapResp(code, internal, external uint16, lifetime uint32) []byte {
	pkt := make([]byte, natpmpMapRespLen)
	pkt[0] = natpmpVersion
	pkt[1] = natpmpResponseBit + natpmpOpMapUDP
	binary.BigEndian.PutUint16(pkt[2:4], code)
	binary.BigEndian.PutUint32(pkt[4:8], 12345)
	binary.BigEndian.PutUint16(pkt[8:10], internal)
	binary.BigEndian.PutUint16(pkt[10:12], external)
	binary.BigEndian.PutUint32(pkt[12:16], lifetime)
	return pkt
}

// pcpMapResp builds a MAP response carrying the given nonce.
func pcpMapResp(code uint8, nonce [pcpNonceLen]byte, internal, external uint16, addr netip.Addr, lifetime uint32) []byte {
	pkt := make([]byte, pcpMsgLen)
	pkt[0] = pcpVersion
	pkt[1] = pcpResponseBit | pcpOpMap
	pkt[3] = code
	binary.BigEndian.PutUint32(pkt[4:8], lifetime)
	binary.BigEndian.PutUint32(pkt[8:12], 12345) // epoch, deliberately ignored
	copy(pkt[24:36], nonce[:])
	pkt[36] = pcpProtoUDP
	binary.BigEndian.PutUint16(pkt[40:42], internal)
	binary.BigEndian.PutUint16(pkt[42:44], external)
	copy(pkt[44:60], mapped16(addr))
	return pkt
}

func TestParseNATPMPExternal(t *testing.T) {
	want := netip.MustParseAddr("203.0.113.7")
	got, err := parseNATPMPExternal(natpmpExternalResp(0, want))
	if err != nil {
		t.Fatalf("parseNATPMPExternal: %v", err)
	}
	if got != want {
		t.Errorf("external address = %s, want %s", got, want)
	}
}

func TestParseNATPMPMap(t *testing.T) {
	pkt := natpmpMapResp(0, 51820, 40001, 3600)
	extPort, lifetime, err := parseNATPMPMap(pkt, 51820)
	if err != nil {
		t.Fatalf("parseNATPMPMap: %v", err)
	}
	if extPort != 40001 {
		t.Errorf("external port = %d, want 40001", extPort)
	}
	if lifetime != time.Hour {
		t.Errorf("lifetime = %s, want 1h", lifetime)
	}
}

// A response echoing a different internal port belongs to some other request.
// NAT-PMP has no transaction id, so this check is the only thing standing
// between us and adopting a stale reply as our own mapping.
func TestParseNATPMPMapWrongInternalPort(t *testing.T) {
	pkt := natpmpMapResp(0, 500, 40001, 3600)
	if _, _, err := parseNATPMPMap(pkt, 51820); err == nil {
		t.Fatal("parseNATPMPMap accepted a response for a different internal port")
	}
}

func TestParseNATPMPMapResultCode(t *testing.T) {
	pkt := natpmpMapResp(2, 51820, 40001, 3600)
	_, _, err := parseNATPMPMap(pkt, 51820)
	if err == nil {
		t.Fatal("parseNATPMPMap accepted a non-zero result code")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not mention result code 2", err)
	}
}

func TestParseNATPMPExternalResultCode(t *testing.T) {
	pkt := natpmpExternalResp(3, netip.MustParseAddr("203.0.113.7"))
	_, err := parseNATPMPExternal(pkt)
	if err == nil {
		t.Fatal("parseNATPMPExternal accepted a non-zero result code")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error %q does not mention result code 3", err)
	}
}

func TestParseNATPMPMapShort(t *testing.T) {
	if _, _, err := parseNATPMPMap(natpmpMapResp(0, 51820, 40001, 3600)[:12], 51820); err == nil {
		t.Fatal("parseNATPMPMap accepted a truncated response")
	}
}

func TestParsePCPMap(t *testing.T) {
	nonce := [pcpNonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	want := netip.MustParseAddrPort("203.0.113.7:40001")
	pkt := pcpMapResp(0, nonce, 51820, want.Port(), want.Addr(), 7200)

	got, lifetime, err := parsePCPMap(pkt, nonce, 51820)
	if err != nil {
		t.Fatalf("parsePCPMap: %v", err)
	}
	if got != want {
		t.Errorf("external = %s, want %s", got, want)
	}
	// The address must come back as IPv4, not as the ::ffff: form it travels
	// in: everything downstream compares it against addresses learned from
	// disco, and a mapped address compares unequal to the plain one.
	if !got.Addr().Is4() {
		t.Errorf("external address %s is not unmapped IPv4", got.Addr())
	}
	if lifetime != 2*time.Hour {
		t.Errorf("lifetime = %s, want 2h", lifetime)
	}
}

// A MAP response whose nonce is not ours is somebody else's answer, or an
// off-path forgery. Either way it must not become our mapping.
func TestParsePCPMapNonceMismatch(t *testing.T) {
	sent := [pcpNonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	other := sent
	other[11] ^= 0xff

	pkt := pcpMapResp(0, other, 51820, 40001, netip.MustParseAddr("203.0.113.7"), 7200)
	_, _, err := parsePCPMap(pkt, sent, 51820)
	if err == nil {
		t.Fatal("parsePCPMap accepted a response with a mismatched nonce")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("error %q does not name the nonce as the reason", err)
	}
}

// A failure reported against a nonce that is not ours says nothing about our
// request, so the nonce check has to come first.
func TestParsePCPMapNonceCheckedBeforeResultCode(t *testing.T) {
	sent := [pcpNonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	other := sent
	other[0] ^= 0xff

	pkt := pcpMapResp(8, other, 51820, 40001, netip.MustParseAddr("203.0.113.7"), 7200)
	_, _, err := parsePCPMap(pkt, sent, 51820)
	if err == nil {
		t.Fatal("parsePCPMap accepted a response with a mismatched nonce")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("error %q blames the result code rather than the nonce", err)
	}
}

func TestParsePCPMapResultCode(t *testing.T) {
	nonce := [pcpNonceLen]byte{9, 9, 9}
	pkt := pcpMapResp(12, nonce, 51820, 40001, netip.MustParseAddr("203.0.113.7"), 7200)
	_, _, err := parsePCPMap(pkt, nonce, 51820)
	if err == nil {
		t.Fatal("parsePCPMap accepted a non-zero result code")
	}
	if !strings.Contains(err.Error(), "12") {
		t.Errorf("error %q does not mention result code 12", err)
	}
}

func TestParsePCPMapWrongInternalPort(t *testing.T) {
	nonce := [pcpNonceLen]byte{4}
	pkt := pcpMapResp(0, nonce, 500, 40001, netip.MustParseAddr("203.0.113.7"), 7200)
	if _, _, err := parsePCPMap(pkt, nonce, 51820); err == nil {
		t.Fatal("parsePCPMap accepted a response for a different internal port")
	}
}

func TestParsePCPMapShort(t *testing.T) {
	nonce := [pcpNonceLen]byte{4}
	pkt := pcpMapResp(0, nonce, 51820, 40001, netip.MustParseAddr("203.0.113.7"), 7200)
	if _, _, err := parsePCPMap(pkt[:pcpHeaderLen], nonce, 51820); err == nil {
		t.Fatal("parsePCPMap accepted a response with no MAP data")
	}
}

// A NAT-PMP response is not a PCP response even though both arrive on the same
// socket from the same host, and a router that speaks only the old protocol
// will answer our PCP probe with one.
func TestParsePCPMapRejectsNATPMPResponse(t *testing.T) {
	nonce := [pcpNonceLen]byte{4}
	pkt := make([]byte, pcpMsgLen)
	copy(pkt, natpmpMapResp(0, 51820, 40001, 3600))
	if _, _, err := parsePCPMap(pkt, nonce, 51820); err == nil {
		t.Fatal("parsePCPMap accepted a NAT-PMP response")
	}
}

// The request encoding is asserted field by field because a misplaced offset
// here produces a MALFORMED_REQUEST from a real router and nothing else to go
// on.
func TestBuildPCPMap(t *testing.T) {
	nonce := [pcpNonceLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	local := netip.MustParseAddr("192.168.1.50")
	req := buildPCPMap(nonce, local, 51820, time.Hour)

	if len(req) != pcpMsgLen {
		t.Fatalf("request is %d bytes, want %d", len(req), pcpMsgLen)
	}
	if req[0] != pcpVersion || req[1] != pcpOpMap {
		t.Errorf("version/opcode = %d/%d, want %d/%d", req[0], req[1], pcpVersion, pcpOpMap)
	}
	if got := binary.BigEndian.Uint32(req[4:8]); got != 3600 {
		t.Errorf("lifetime = %d, want 3600", got)
	}
	if got := netip.AddrFrom16([16]byte(req[8:24])).Unmap(); got != local {
		t.Errorf("client address = %s, want %s", got, local)
	}
	if !netip.AddrFrom16([16]byte(req[8:24])).Is4In6() {
		t.Errorf("client address is not in the IPv4-mapped form PCP requires")
	}
	if [pcpNonceLen]byte(req[24:36]) != nonce {
		t.Errorf("nonce was not copied into the request")
	}
	if req[36] != pcpProtoUDP {
		t.Errorf("protocol = %d, want %d", req[36], pcpProtoUDP)
	}
	if got := binary.BigEndian.Uint16(req[40:42]); got != 51820 {
		t.Errorf("internal port = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint16(req[42:44]); got != 51820 {
		t.Errorf("suggested external port = %d, want 51820", got)
	}
	for _, b := range req[44:60] {
		if b != 0 {
			t.Errorf("suggested external address is set, want zero so the server chooses")
			break
		}
	}
}

// A zero lifetime is "delete the mapping" on the wire, so no conversion may
// ever produce one from a caller that asked for a mapping.
func TestLifetimeSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want uint32
	}{
		{time.Hour, 3600},
		{2 * time.Hour, 7200},
		{500 * time.Millisecond, 1},
		{0, 1},
		{-time.Hour, 1},
		{1 << 33 * time.Second, ^uint32(0)},
	} {
		if got := lifetimeSeconds(tc.in); got != tc.want {
			t.Errorf("lifetimeSeconds(%s) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseProcNetRoute(t *testing.T) {
	// Real output shape: a wifi default route, a container bridge with no
	// gateway, and a higher-metric default route that must lose.
	const table = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
wlan0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
eth0	00000000	01FEA8C0	0003	0	0	1000	00000000	0	0	0
wlan0	0001A8C0	00000000	0001	0	0	600	00FFFFFF	0	0	0
`
	got, err := parseProcNetRoute(strings.NewReader(table))
	if err != nil {
		t.Fatalf("parseProcNetRoute: %v", err)
	}
	want := netip.MustParseAddr("192.168.1.1")
	if got != want {
		t.Errorf("gateway = %s, want %s", got, want)
	}
}

// A default route through a point-to-point tunnel has no next hop, and taking
// it would mean sending PCP requests into the tunnel instead of at the router.
func TestParseProcNetRouteSkipsNextHoplessDefault(t *testing.T) {
	const table = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
tun0	00000000	00000000	0001	0	0	50	00000000	0	0	0
eth0	00000000	01FEA8C0	0003	0	0	1000	00000000	0	0	0
`
	got, err := parseProcNetRoute(strings.NewReader(table))
	if err != nil {
		t.Fatalf("parseProcNetRoute: %v", err)
	}
	want := netip.MustParseAddr("192.168.254.1")
	if got != want {
		t.Errorf("gateway = %s, want %s", got, want)
	}
}

func TestParseProcNetRouteNoDefault(t *testing.T) {
	const table = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`
	if _, err := parseProcNetRoute(strings.NewReader(table)); err == nil {
		t.Fatal("parseProcNetRoute invented a default route")
	}
}

// Port 0 cannot be mapped, and the check has to happen before anything touches
// the network so that a caller with an unconfigured listen port gets an
// immediate error rather than a two-second timeout.
func TestMapRejectsPortZero(t *testing.T) {
	c := &Client{Gateway: netip.MustParseAddr("192.168.1.1")}
	if _, err := c.Map(context.Background(), 0, time.Hour); err == nil {
		t.Fatal("Map accepted port 0")
	}
}

func TestMapRejectsIPv6Gateway(t *testing.T) {
	c := &Client{Gateway: netip.MustParseAddr("fe80::1")}
	if _, err := c.Map(context.Background(), 51820, time.Hour); err == nil {
		t.Fatal("Map accepted an IPv6 gateway")
	}
}
