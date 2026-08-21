package wg

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// synPacket builds an IPv6 TCP SYN carrying an MSS option, and returns it with
// a correct checksum — so a test that breaks the checksum is detectable.
func synPacket(t *testing.T, mss uint16, extraOpts []byte, flags byte) []byte {
	t.Helper()
	opts := append([]byte{optMSS, optMSSLen, 0, 0}, extraOpts...)
	binary.BigEndian.PutUint16(opts[2:], mss)
	return synPacketWithOptions(t, opts, flags)
}

// synPacketWithOptions builds one carrying exactly these options, so a test can
// control their order and the declared header length together.
func synPacketWithOptions(t *testing.T, opts []byte, flags byte) []byte {
	t.Helper()
	src := netip.MustParseAddr("fd58:e76:f6a1::1").As16()
	dst := netip.MustParseAddr("fd58:e76:f6a1::2").As16()

	opts = append([]byte(nil), opts...)
	for len(opts)%4 != 0 {
		opts = append(opts, optNop)
	}
	tcpLen := 20 + len(opts)

	pkt := make([]byte, ipv6HeaderLen+tcpLen)
	pkt[0] = 6 << 4
	binary.BigEndian.PutUint16(pkt[4:], uint16(tcpLen))
	pkt[6] = tcpProto
	pkt[7] = 64
	copy(pkt[8:], src[:])
	copy(pkt[24:], dst[:])

	tcp := pkt[ipv6HeaderLen:]
	binary.BigEndian.PutUint16(tcp[0:], 12345) // source port
	binary.BigEndian.PutUint16(tcp[2:], 80)    // destination port
	tcp[12] = byte(tcpLen/4) << 4
	tcp[13] = flags
	copy(tcp[20:], opts)

	binary.BigEndian.PutUint16(tcp[16:], tcpChecksum(pkt))
	return pkt
}

// tcpChecksum computes the whole thing the slow way, which is what the
// incremental update has to agree with.
func tcpChecksum(pkt []byte) uint16 {
	tcp := pkt[ipv6HeaderLen:]
	var s uint32
	for i := 8; i < 40; i += 2 { // pseudo-header: source and destination
		s += uint32(binary.BigEndian.Uint16(pkt[i:]))
	}
	s += uint32(len(tcp)) + tcpProto
	for i := 0; i+1 < len(tcp); i += 2 {
		if i == 16 {
			continue // the checksum field itself
		}
		s += uint32(binary.BigEndian.Uint16(tcp[i:]))
	}
	if len(tcp)%2 == 1 {
		s += uint32(tcp[len(tcp)-1]) << 8
	}
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	return ^uint16(s)
}

func mssOf(t *testing.T, pkt []byte) uint16 {
	t.Helper()
	tcp := pkt[ipv6HeaderLen:]
	dataOff := int(tcp[12]>>4) * 4
	for i := 20; i+1 < dataOff; {
		if tcp[i] == optNop {
			i++
			continue
		}
		if tcp[i] == optMSS {
			return binary.BigEndian.Uint16(tcp[i+2:])
		}
		i += int(tcp[i+1])
	}
	t.Fatal("no MSS option")
	return 0
}

// The whole point: an oversized advertisement is brought down to what the path
// can carry, and the checksum still verifies. A wrong checksum is worse than no
// clamp, because the far end drops the SYN and the connection never starts —
// silently, which is the failure this exists to remove.
func TestClampLowersMSSAndKeepsTheChecksumValid(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x02)
	if !ClampMSS(pkt, 1074) {
		t.Fatal("an oversized MSS was not clamped")
	}
	if got := mssOf(t, pkt); got != 1074 {
		t.Errorf("MSS is %d, want 1074", got)
	}
	tcp := pkt[ipv6HeaderLen:]
	got := binary.BigEndian.Uint16(tcp[16:])
	binary.BigEndian.PutUint16(tcp[16:], 0)
	if want := tcpChecksum(pkt); got != want {
		t.Errorf("checksum is %04x, want %04x — the far end would drop this SYN", got, want)
	}
}

// A value already small enough is left alone, so a peer that has done its own
// arithmetic is not second-guessed and the checksum is not touched.
func TestASmallEnoughMSSIsUntouched(t *testing.T) {
	for _, mss := range []uint16{536, 1073, 1074} {
		pkt := synPacket(t, mss, nil, 0x02)
		before := append([]byte(nil), pkt...)
		if ClampMSS(pkt, 1074) {
			t.Errorf("MSS %d was clamped against a limit of 1074", mss)
		}
		if string(pkt) != string(before) {
			t.Errorf("MSS %d: the packet was modified anyway", mss)
		}
	}
}

// The option is found past others, since a real SYN carries window scale,
// SACK-permitted and timestamps, and MSS is not always first.
func TestTheOptionIsFoundAmongOthers(t *testing.T) {
	// A real SYN carries window scale, SACK-permitted and timestamps, and MSS
	// is not always first. Built with the option block declared in the header
	// rather than written over a smaller one — the first attempt at this test
	// wrote ten bytes of options into a packet whose data offset said four, and
	// the parser was right to stop before reaching them.
	//
	// nop, window scale, SACK permitted, then MSS last.
	pkt := synPacketWithOptions(t,
		[]byte{optNop, 3, 3, 7, 4, 2, optMSS, optMSSLen, 0x05, 0xa0}, 0x02)

	if !ClampMSS(pkt, 1074) {
		t.Fatal("an MSS option after other options was not found")
	}
	if got := mssOf(t, pkt); got != 1074 {
		t.Errorf("MSS is %d, want 1074", got)
	}
}

// A SYN-ACK carries an MSS too, and it governs what the other end sends to us.
func TestSynAckIsClampedAsWell(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x12) // SYN|ACK
	if !ClampMSS(pkt, 1074) {
		t.Error("a SYN-ACK was not clamped, so the peer keeps sending full-size segments")
	}
}

// Anything that is not a SYN carries no MSS and must not be touched — most
// obviously the data packets, which are the hot path.
func TestNonSynPacketsAreIgnored(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x10) // ACK only
	if ClampMSS(pkt, 1074) {
		t.Error("a non-SYN packet was modified")
	}
}

// Malformed input must not panic or read past the buffer. This runs against
// whatever the network hands us, including things designed to be awkward.
func TestMalformedPacketsAreSurvived(t *testing.T) {
	good := synPacket(t, 1440, nil, 0x02)
	cases := [][]byte{
		nil,
		{},
		{6 << 4},
		good[:ipv6HeaderLen],
		good[:ipv6HeaderLen+10],
	}
	// A data offset claiming more header than exists.
	bad := append([]byte(nil), good...)
	bad[ipv6HeaderLen+12] = 0xf0
	cases = append(cases, bad)
	// An option length of zero, which would not advance the loop.
	zero := append([]byte(nil), good...)
	zero[ipv6HeaderLen+21] = 0
	cases = append(cases, zero)
	// An option length running past the header.
	over := append([]byte(nil), good...)
	over[ipv6HeaderLen+21] = 0xff
	cases = append(cases, over)
	// Not IPv6, and IPv6 carrying something other than TCP.
	notV6 := append([]byte(nil), good...)
	notV6[0] = 4 << 4
	cases = append(cases, notV6)
	notTCP := append([]byte(nil), good...)
	notTCP[6] = 17
	cases = append(cases, notTCP)

	for i, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d panicked: %v", i, r)
				}
			}()
			ClampMSS(c, 1074)
		}()
	}
}

// A limit of zero means no clamping, which is how a direct peer is expressed.
func TestZeroMeansNoClamp(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x02)
	before := append([]byte(nil), pkt...)
	if ClampMSS(pkt, 0) {
		t.Error("a zero limit clamped something")
	}
	if string(pkt) != string(before) {
		t.Error("a zero limit modified the packet")
	}
}
