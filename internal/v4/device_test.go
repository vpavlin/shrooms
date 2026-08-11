package v4

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
)

// fakeTun stands in for the kernel: Read hands out queued packets, Write keeps
// what it is given.
type fakeTun struct {
	tun.Device
	in      [][]byte
	written [][]byte
}

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(f.in) == 0 {
		return 0, nil
	}
	n := 0
	for n < len(bufs) && len(f.in) > 0 {
		p := f.in[0]
		f.in = f.in[1:]
		sizes[n] = copy(bufs[n][offset:], p)
		n++
	}
	return n, nil
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		f.written = append(f.written, append([]byte(nil), b[offset:]...))
	}
	return len(bufs), nil
}

func testTable(t *testing.T) (*Table, netip.Addr) {
	t.Helper()
	selfPub, _, _ := ed25519.GenerateKey(nil)
	peerPub, _, _ := ed25519.GenerateKey(nil)
	tbl := NewTable("testnetid",
		Entry{Overlay: self6, DevicePub: selfPub},
		[]Entry{{Overlay: peer6, DevicePub: peerPub}},
	)
	alias, ok := tbl.Alias(peer6)
	if !ok {
		t.Fatal("peer got no alias")
	}
	return tbl, alias
}

func read(t *testing.T, d *Device) [][]byte {
	t.Helper()
	const offset = 16
	bufs := make([][]byte, 4)
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	sizes := make([]int, 4)
	n, err := d.Read(bufs, sizes, offset)
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		out[i] = append([]byte(nil), bufs[i][offset:offset+sizes[i]]...)
	}
	return out
}

// The whole point, end to end: a browser opens a connection to a peer's alias,
// the peer answers over IPv6, and the browser sees IPv4 both ways.
func TestRoundTripThroughTheDevice(t *testing.T) {
	tbl, alias := testTable(t)
	ft := &fakeTun{}
	d := NewDevice(ft, tbl, 1360)

	ft.in = [][]byte{tcp4(t, tbl.Self(), alias, []byte("hello"), false, 1460)}
	got := read(t, d)
	if len(got) != 1 {
		t.Fatalf("read %d packets, want 1", len(got))
	}
	if got[0][0]>>4 != 6 {
		t.Fatal("packet was not translated on the way out")
	}
	verify6(t, got[0])
	if !bytes.Equal(got[0][24:40], addr16(peer6)) {
		t.Error("did not go to the peer's overlay address")
	}

	// The peer answers: same ports, the other way round.
	reply := make([]byte, len(got[0]))
	copy(reply, got[0])
	copy(reply[8:24], addr16(peer6))
	copy(reply[24:40], addr16(self6))
	body := reply[v6HeaderLen:]
	sport := binary.BigEndian.Uint16(body[0:2])
	dport := binary.BigEndian.Uint16(body[2:4])
	binary.BigEndian.PutUint16(body[0:2], dport)
	binary.BigEndian.PutUint16(body[2:4], sport)
	putChecksum(body, 16, tcpUDPChecksum(reply[8:24], reply[24:40], protoTCP, body))

	framed := make([]byte, 16+len(reply))
	copy(framed[16:], reply)
	if _, err := d.Write([][]byte{framed}, 16); err != nil {
		t.Fatal(err)
	}
	if len(ft.written) != 1 {
		t.Fatalf("wrote %d packets", len(ft.written))
	}
	back := ft.written[0]
	if back[0]>>4 != 4 {
		t.Fatal("the reply was not translated back to IPv4")
	}
	verify4(t, back)
	if !bytes.Equal(back[12:16], addr4(alias)) || !bytes.Equal(back[16:20], addr4(tbl.Self())) {
		t.Error("the reply came from the wrong address")
	}
}

// The thing that must not break: IPv6 traffic a peer sends on its own account
// is not part of any translated flow, and must reach the operating system
// untouched. `http://[fd93:…]/` is how the mesh is used today.
func TestNativeIPv6IsUntouched(t *testing.T) {
	tbl, _ := testTable(t)
	ft := &fakeTun{}
	d := NewDevice(ft, tbl, 1360)

	// A packet from the peer for a connection nobody made over IPv4.
	native := make([]byte, v6HeaderLen+20)
	native[0] = 6 << 4
	binary.BigEndian.PutUint16(native[4:6], 20)
	native[6] = protoTCP
	native[7] = 64
	copy(native[8:24], addr16(peer6))
	copy(native[24:40], addr16(self6))
	binary.BigEndian.PutUint16(native[v6HeaderLen:], 443)
	binary.BigEndian.PutUint16(native[v6HeaderLen+2:], 51000)

	framed := make([]byte, 16+len(native))
	copy(framed[16:], native)
	if _, err := d.Write([][]byte{framed}, 16); err != nil {
		t.Fatal(err)
	}
	if len(ft.written) != 1 {
		t.Fatalf("wrote %d packets", len(ft.written))
	}
	if ft.written[0][0]>>4 != 6 {
		t.Error("translated IPv6 traffic that belonged to no v4 flow")
	}
	if !bytes.Equal(ft.written[0], native) {
		t.Error("altered a packet it should have passed through")
	}
	if _, in, _ := d.Stats(); in != 0 {
		t.Errorf("counted %d inbound translations, want 0", in)
	}
}

// An IPv4 packet for an address that is not a peer has nowhere to go. It must
// be dropped rather than handed to WireGuard, which would put IPv4 on an
// IPv6-only overlay.
func TestUnmappedIPv4IsDropped(t *testing.T) {
	tbl, _ := testTable(t)
	ft := &fakeTun{}
	d := NewDevice(ft, tbl, 1360)

	stray := netip.MustParseAddr("198.19.200.200")
	if !Prefix.Contains(stray) {
		t.Fatal("the stray address is outside the range entirely")
	}
	if _, mapped := tbl.Overlay(stray); mapped {
		t.Skip("the random alias happened to be this address")
	}
	ft.in = [][]byte{tcp4(t, tbl.Self(), stray, []byte("x"), false, 1460)}
	if got := read(t, d); len(got) != 0 {
		t.Errorf("passed on %d unmapped IPv4 packets", len(got))
	}
	if _, _, dropped := d.Stats(); dropped != 1 {
		t.Errorf("dropped counter is %d, want 1", dropped)
	}
}

// IPv6 packets from the operating system must pass through the outbound path
// unchanged, and keep their place in the batch.
func TestOutboundIPv6IsUntouched(t *testing.T) {
	tbl, alias := testTable(t)
	ft := &fakeTun{}
	d := NewDevice(ft, tbl, 1360)

	six := make([]byte, v6HeaderLen+8)
	six[0] = 6 << 4
	binary.BigEndian.PutUint16(six[4:6], 8)
	six[6] = protoUDP
	copy(six[8:24], addr16(self6))
	copy(six[24:40], addr16(peer6))

	ft.in = [][]byte{six, tcp4(t, tbl.Self(), alias, nil, false, 1460)}
	got := read(t, d)
	if len(got) != 2 {
		t.Fatalf("read %d packets, want 2", len(got))
	}
	if !bytes.Equal(got[0], six) {
		t.Error("altered an IPv6 packet on the way out")
	}
	if got[1][0]>>4 != 6 {
		t.Error("did not translate the IPv4 packet behind it")
	}
}
