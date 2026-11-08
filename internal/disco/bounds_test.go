package disco

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func aps(s ...string) []netip.AddrPort {
	out := make([]netip.AddrPort, len(s))
	for i, x := range s {
		out[i] = netip.MustParseAddrPort(x)
	}
	return out
}

// What a member announces is probed only where a datagram could reach it.
func TestCandidatesDropUnusableTargets(t *testing.T) {
	got := Candidates(aps("127.0.0.1:51820", "0.0.0.0:51820", "224.0.0.1:51820", "255.255.255.255:51820",
		"169.254.1.1:51820", "[fe80::1]:51820", "[::1]:51820", "203.0.113.5:0",
		"192.168.10.5:51820", "100.69.1.2:51820", "203.0.113.5:51820", "192.168.10.5:51820"))
	want := aps("203.0.113.5:51820", "192.168.10.5:51820", "100.69.1.2:51820")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v: public first, LAN and CGNAT kept, the rest and the duplicate dropped", got, want)
	}
}

// A peer announcing a long list gets MaxCandidates probed, and a public
// address at the end of it is among them.
func TestCandidatesCapKeepsThePublicAddress(t *testing.T) {
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("172.17.0.%d:51820", i+1))
	}
	many = append(many, "198.51.100.7:51820")
	got := Candidates(aps(many...))
	if len(got) != MaxCandidates || got[0] != netip.MustParseAddrPort("198.51.100.7:51820") {
		t.Fatalf("got %d candidates starting %v", len(got), got[0])
	}
}

// Announces that change many times a second do not make as many probe rounds.
func TestProbeFloorPerPeer(t *testing.T) {
	sent := 0
	p := NewProber(testKey(t), newDeviceKey(t), func([]byte, netip.AddrPort) error { sent++; return nil })
	start := time.Now()
	for i := 0; i < 50; i++ {
		p.Probe(unprobed, aps(fmt.Sprintf("198.51.100.%d:51820", i+1)), start.Add(time.Duration(i)*10*time.Millisecond))
	}
	if sent != 1 {
		t.Fatalf("50 rounds in half a second sent %d probes, want 1", sent)
	}
	p.Probe(unprobed, aps("198.51.100.99:51820"), start.Add(ProbeFloor))
	if sent != 2 {
		t.Fatalf("a round after ProbeFloor was not sent (%d)", sent)
	}
	// Another peer has a floor of its own.
	p.Probe("other", aps("198.51.100.98:51820"), start.Add(ProbeFloor))
	if sent != 3 {
		t.Fatalf("another peer's round was held back (%d)", sent)
	}
}

// Probe itself refuses unusable targets and over-long lists, whatever the
// caller passes.
func TestProbeFiltersAndCaps(t *testing.T) {
	var to []netip.AddrPort
	p := NewProber(testKey(t), newDeviceKey(t), func(_ []byte, a netip.AddrPort) error { to = append(to, a); return nil })
	var many []netip.AddrPort
	for i := 0; i < 30; i++ {
		many = append(many, netip.MustParseAddrPort(fmt.Sprintf("198.51.100.%d:51820", i+1)))
	}
	many = append(aps("127.0.0.1:1", "224.0.0.1:1"), many...)
	p.Probe(unprobed, many, time.Now())
	if len(to) != MaxCandidates+1 {
		t.Fatalf("probed %d addresses, want %d", len(to), MaxCandidates+1)
	}
	for _, a := range to {
		if !usableTarget(a) {
			t.Fatalf("probed %v", a)
		}
	}
}

// Pings replayed from one (spoofed) source get a bounded number of pongs.
func TestPongsPerSourceAreBounded(t *testing.T) {
	key := testKey(t)
	sent := 0
	p := NewProber(key, newDeviceKey(t), func([]byte, netip.AddrPort) error { sent++; return nil })
	sender := NewProber(key, newDeviceKey(t), nil)
	tx, _ := NewTxID()
	pkt, err := EncodePing(key, sender.selfPriv, tx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(key, pkt)
	if err != nil {
		t.Fatal(err)
	}
	victim := netip.MustParseAddrPort("192.0.2.1:9")
	for i := 0; i < 1000; i++ {
		p.HandlePing(m, victim)
	}
	if sent > PongBurst+2 {
		t.Fatalf("1000 replays from one source got %d pongs", sent)
	}
	// Another source is answered.
	before := sent
	p.HandlePing(m, netip.MustParseAddrPort("192.0.2.2:9"))
	if sent != before+1 {
		t.Fatal("a different source was not answered")
	}
}

// The bucket refills: an honest peer pinging every few seconds is always
// answered.
func TestPongBucketRefills(t *testing.T) {
	p := NewProber(testKey(t), newDeviceKey(t), nil)
	from := netip.MustParseAddrPort("192.0.2.1:9")
	now := time.Now()
	for i := 0; i < PongBurst; i++ {
		if !p.allowPong(from, now) {
			t.Fatalf("pong %d of the burst refused", i)
		}
	}
	if p.allowPong(from, now) {
		t.Fatal("past the burst, still answered")
	}
	if !p.allowPong(from, now.Add(PongEvery)) {
		t.Fatal("not refilled after PongEvery")
	}
	for i := 0; i < 100; i++ {
		now = now.Add(ProbeInterval)
		for j := 0; j < 4; j++ {
			if !p.allowPong(from, now) {
				t.Fatal("an honest rate was refused")
			}
		}
	}
}
