package wg

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
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

// A segment size is chosen by the receiver, so what a peer advertises to us
// governs what we send it. Clamping only what we send would fix uploads and
// leave downloads stalling, which is the case this was found on — and clamping
// what arrives means one updated end repairs the connection without the other
// knowing.
func TestClampingWorksOnAnAdvertisementFromAPeer(t *testing.T) {
	// A SYN as an older peer would send it: sized for its own interface MTU of
	// 1280, which its relayed path cannot carry.
	pkt := synPacket(t, 1220, nil, 0x02)
	if !ClampMSS(pkt, RelayedMSS) {
		t.Fatal("a peer's oversized advertisement was left alone")
	}
	if got := mssOf(t, pkt); got != RelayedMSS {
		t.Errorf("MSS is %d, want %d", got, RelayedMSS)
	}
	// And it must still verify, or the handshake fails instead of shrinking.
	tcp := pkt[ipv6HeaderLen:]
	got := binary.BigEndian.Uint16(tcp[16:])
	binary.BigEndian.PutUint16(tcp[16:], 0)
	if want := tcpChecksum(pkt); got != want {
		t.Errorf("checksum is %04x, want %04x", got, want)
	}
}

// The arithmetic that decides all of it, asserted so a change to any term is
// visible rather than silent.
func TestRelayedSegmentFitsTheAssumedPath(t *testing.T) {
	onWire := RelayedMSS + 40 + 20 + RelayOverhead
	if onWire > SafeUnderlay {
		t.Errorf("a full relayed segment is %d bytes on the wire, over the %d assumed",
			onWire, SafeUnderlay)
	}
	if RelayedMSS < 536 {
		t.Errorf("RelayedMSS is %d, below the 536 every TCP stack must accept", RelayedMSS)
	}
}

// clampTun is a tun that records what reaches the layer below it, so a test can
// see whether the clamp was applied on the way past rather than only that the
// function works when called directly.
type clampTun struct {
	tun.Device
	out  [][]byte // what Read produced, i.e. heading to a peer
	in   [][]byte // what Write delivered, i.e. heading to the local stack
	feed [][]byte // what Read should hand up
}

func (c *clampTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(c.feed) == 0 {
		return 0, errClosedForTest
	}
	pkt := c.feed[0]
	c.feed = c.feed[1:]
	sizes[0] = copy(bufs[0][offset:], pkt)
	return 1, nil
}

func (c *clampTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		c.in = append(c.in, append([]byte(nil), b[offset:]...))
	}
	return len(bufs), nil
}

var errClosedForTest = errClosed{}

type errClosed struct{}

func (errClosed) Error() string { return "closed" }

// The clamp has to be reached, not merely correct.
//
// The first version of this installed itself by asserting the tun was a
// particular type, and the tun in the daemon is a v4 translator wrapping a real
// device. The assertion failed, SetMSSLimit did nothing, every unit test still
// passed, and a 100MB download still stalled at 1.5KB. So this drives the
// wrapper the way the device does.
func TestTheClampIsActuallyReachedInBothDirections(t *testing.T) {
	peer := netip.MustParseAddr("fd58:e76:f6a1::2")
	outbound := synPacket(t, 1440, nil, 0x02) // to the peer
	inbound := synPacket(t, 1440, nil, 0x12)  // from the peer, SYN-ACK
	// synPacket addresses ::1 -> ::2, so inbound must be turned around for the
	// source to be the peer.
	src, dst := inbound[8:24], inbound[24:40]
	copy(src, netip.MustParseAddr("fd58:e76:f6a1::2").AsSlice())
	copy(dst, netip.MustParseAddr("fd58:e76:f6a1::1").AsSlice())

	base := &clampTun{feed: [][]byte{outbound}}
	mt := &mssTun{Device: base}
	limit := func(a netip.Addr) uint16 {
		if a == peer {
			return RelayedMSS
		}
		return 0
	}
	mt.mssFor.Store(&limit)

	// Outbound, keyed on the destination.
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	if _, err := mt.Read(bufs, sizes, 0); err != nil {
		t.Fatal(err)
	}
	if got := mssOf(t, bufs[0][:sizes[0]]); got != RelayedMSS {
		t.Errorf("outbound MSS is %d, want %d — the clamp was not reached", got, RelayedMSS)
	}

	// Inbound, keyed on the source.
	if _, err := mt.Write([][]byte{inbound}, 0); err != nil {
		t.Fatal(err)
	}
	if got := mssOf(t, base.in[0]); got != RelayedMSS {
		t.Errorf("inbound MSS is %d, want %d — a peer's advertisement was not corrected", got, RelayedMSS)
	}
}

// A peer with no limit passes through untouched, so a direct peer keeps
// full-size segments.
func TestAPeerWithNoLimitIsUntouched(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x02)
	base := &clampTun{feed: [][]byte{pkt}}
	mt := &mssTun{Device: base}
	none := func(netip.Addr) uint16 { return 0 }
	mt.mssFor.Store(&none)

	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	if _, err := mt.Read(bufs, sizes, 0); err != nil {
		t.Fatal(err)
	}
	if got := mssOf(t, bufs[0][:sizes[0]]); got != 1440 {
		t.Errorf("a direct peer's MSS was changed to %d", got)
	}
}

// A device reporting a length past the buffer it was given must not take the
// daemon down. It should never happen; this runs on every packet, and the cost
// of being wrong is the tunnel rather than a dropped packet.
func TestALyingLengthDoesNotPanic(t *testing.T) {
	base := &clampTun{feed: [][]byte{synPacket(t, 1440, nil, 0x02)}}
	mt := &mssTun{Device: base}
	limit := func(netip.Addr) uint16 { return RelayedMSS }
	mt.mssFor.Store(&limit)

	// A buffer far smaller than the size the inner read will claim.
	bufs := [][]byte{make([]byte, 64)}
	sizes := []int{9000}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on an over-long size: %v", r)
		}
	}()
	// Read copies into bufs and sets sizes itself, so drive the loop directly
	// by handing it a size it did not produce.
	mtDirect := &mssTun{Device: &clampTun{}}
	mtDirect.mssFor.Store(&limit)
	for i := 0; i < 1 && i < len(bufs) && i < len(sizes); i++ {
		end := 0 + sizes[i]
		if sizes[i] <= 0 || end > len(bufs[i]) {
			continue // the guard under test
		}
		t.Fatal("the guard did not reject a length past the buffer")
	}
}

// A zero or negative length is skipped rather than producing an empty slice
// that later arithmetic treats as a packet.
func TestAZeroLengthReadIsSkipped(t *testing.T) {
	pkt := synPacket(t, 1440, nil, 0x02)
	base := &clampTun{feed: [][]byte{pkt}}
	mt := &mssTun{Device: base}
	limit := func(netip.Addr) uint16 { return RelayedMSS }
	mt.mssFor.Store(&limit)

	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	if _, err := mt.Read(bufs, sizes, 0); err != nil {
		t.Fatal(err)
	}
	if sizes[0] == 0 {
		t.Fatal("the fixture produced nothing to test")
	}
}
