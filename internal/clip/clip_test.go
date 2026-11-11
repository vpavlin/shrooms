package clip

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	laptop = netip.MustParseAddr("fd00::1")
	phone  = netip.MustParseAddr("fd00::2")
	vps    = netip.MustParseAddr("fd00::3")
	work   = netip.MustParseAddr("fd01::2")
)

// From the laptop: home has the phone and the VPS, office has the phone and
// somebody else's desktop.
func laptopView() ([]Mesh, []Peer) {
	return []Mesh{{"home", "laptop", laptop}, {"office", "laptop", netip.MustParseAddr("fd01::1")}},
		[]Peer{
			{"home", "phone", phone, true, true},
			{"home", "vps", vps, false, true},
			{"office", "phone", work, true, true},
			{"office", "colleague", netip.MustParseAddr("fd01::9"), true, true},
		}
}

func TestResolve(t *testing.T) {
	meshes, peers := laptopView()
	for _, c := range []struct {
		req   Request
		mesh  string
		names string
		err   string
	}{
		{req: Request{To: []string{"phone"}}, mesh: "home", names: "phone"},
		{req: Request{To: []string{"phone.office"}}, mesh: "office", names: "phone"},
		{req: Request{To: []string{"colleague"}}, mesh: "office", names: "colleague"},
		{req: Request{To: []string{"phone", "vps"}}, mesh: "home", names: "phone,vps"},
		{req: Request{To: []string{"vps", "colleague"}}, err: "no one mesh"},
		{req: Request{To: []string{"vps"}, Mesh: "office"}, err: "no device called vps on office"},
		{req: Request{All: true, Mesh: "home"}, mesh: "home", names: "phone,vps"},
		// "All" never spans meshes, nor picks one silently.
		{req: Request{All: true}, err: "all of which mesh"},
		{req: Request{}, err: "say who"},
		{req: Request{To: []string{"phone.home"}, Mesh: "office"}, err: "one clip goes on one mesh"},
	} {
		mesh, names, err := resolve(c.req, meshes, peers)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: err %v, want %q", c.req, err, c.err)
			}
			continue
		}
		if err != nil || mesh != c.mesh || strings.Join(names, ",") != c.names {
			t.Errorf("%+v: %q %v %v, want %q %q", c.req, mesh, names, err, c.mesh, c.names)
		}
	}
}

// A clip goes from the laptop to the phone, the phone knows it came from the
// laptop by its address, and the VPS, offline, still waits for it.
func TestSendAndReceive(t *testing.T) {
	phoneView := func() ([]Mesh, []Peer) {
		return []Mesh{{"home", "phone", phone}}, []Peer{{"home", "laptop", netip.MustParseAddr("127.0.0.1"), true, true}}
	}
	got := make(chan Clip, 1)
	recv := New("", phoneView, nil)
	recv.Arrived = func(c Clip) { got <- c }
	srv := httptest.NewServer(recv.Handler("home"))
	defer srv.Close()

	send := New(t.TempDir()+"/clips.json", laptopView, nil)
	send.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !strings.HasPrefix(addr, "[fd00::2]:") {
			t.Errorf("dialled %s, only the phone is online", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	c, err := send.Send(Request{Text: "https://example.org/x", To: []string{"phone", "vps"}})
	if err != nil {
		t.Fatal(err)
	}
	send.flush(context.Background())
	select {
	case r := <-got:
		if r.From != "laptop" || r.Text != "https://example.org/x" || r.ID != c.ID || r.Mesh != "home" {
			t.Fatalf("received %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}
	_, sent := send.List()
	if len(sent) != 1 || sent[0].To[0].Sent.IsZero() || !sent[0].To[1].Sent.IsZero() {
		t.Fatalf("sent %+v: the phone has it, the VPS waits", sent)
	}
	// Sent again after a lost answer: still one clip.
	send.mark(c.ID, "phone", nil)
	recv.receive("home", Peer{Name: "laptop", Signed: true}, wire{ID: c.ID, Text: "x", Expires: time.Now().Add(time.Hour).Unix()})
	if r, _ := recv.List(); len(r) != 1 {
		t.Fatalf("%d clips after a retry", len(r))
	}
}

// Only a member of the mesh a clip arrives on may send one, and the name is the
// one its address has, whatever it says.
func TestStrangerRefused(t *testing.T) {
	recv := New("", laptopView, nil)
	h := recv.Handler("home")
	post := func(from string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/clip",
			strings.NewReader(`{"id":"a","text":"hi","from":"phone","expires":`+itoa(time.Now().Add(time.Hour).Unix())+`}`))
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	// The colleague is on office, not home.
	if c := post("[fd01::9]:4000"); c != http.StatusForbidden {
		t.Fatalf("office member on home: %d", c)
	}
	if c := post("[fd00::3]:4000"); c != http.StatusNoContent {
		t.Fatalf("vps: %d", c)
	}
	r, _ := recv.List()
	if len(r) != 1 || r[0].From != "vps" {
		t.Fatalf("%+v", r)
	}
}

// A secret is never written down, and goes soon after it is copied.
func TestSecret(t *testing.T) {
	path := t.TempDir() + "/clips.json"
	s := New(path, laptopView, nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.receive("home", Peer{Name: "phone", Signed: true}, wire{ID: "k", Text: "sk-123", Secret: true, Expires: now.Add(time.Hour).Unix()})
	s.receive("home", Peer{Name: "phone", Signed: true}, wire{ID: "u", Text: "https://x", Expires: now.Add(time.Hour).Unix()})
	if again := New(path, laptopView, nil); func() bool { r, _ := again.List(); return len(r) != 1 || r[0].ID != "u" }() {
		t.Fatal("a secret was kept on disk")
	}
	if _, err := s.Take("k"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(takenSecret + time.Second)
	if r, _ := s.List(); len(r) != 1 || r[0].ID != "u" {
		t.Fatalf("after copying and waiting: %+v", r)
	}
}

// A recipient keeps a clip no longer than it allows, whatever the sender asks.
func TestExpiryCapped(t *testing.T) {
	s := New("", laptopView, nil)
	s.receive("home", Peer{Name: "phone", Signed: true}, wire{ID: "a", Text: "x", Expires: time.Now().Add(365 * 24 * time.Hour).Unix()})
	r, _ := s.List()
	if len(r) != 1 || r[0].Expires.After(time.Now().Add(MaxTTL+time.Minute)) {
		t.Fatalf("%+v", r)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A recipient on an older shrooms refuses: the sender says so in words, and
// tries it again later rather than every 15 seconds.
func TestRefusedBacksOff(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens: refused
	dials := 0
	s := New("", laptopView, nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials++
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	if _, err := s.Send(Request{Text: "x", To: []string{"phone"}}); err != nil {
		t.Fatal(err)
	}
	s.flush(context.Background())
	_, sent := s.List()
	if e := sent[0].To[0].Error; !strings.Contains(e, "older shrooms") {
		t.Fatalf("error %q", e)
	}
	s.flush(context.Background())
	if dials != 1 {
		t.Fatalf("%d tries within the backoff", dials)
	}
	now = now.Add(31 * time.Second)
	s.flush(context.Background())
	if dials != 2 {
		t.Fatalf("%d tries after it", dials)
	}
}

// A sender may not date a clip ahead, nor send a stream of them; and a name
// its admin did not sign is said to be only announced.
func TestWhatASenderMayNotDo(t *testing.T) {
	s := New("", laptopView, nil)
	future := time.Now().Add(time.Hour)
	s.receive("home", Peer{Name: "vps"}, wire{ID: "f", Text: "x", Created: future.Unix(), Expires: future.Unix()})
	r, _ := s.List()
	if len(r) != 1 || r[0].Created.After(time.Now().Add(time.Second)) || !r[0].Unsigned {
		t.Fatalf("%+v", r)
	}
	h := s.Handler("home")
	codes := map[int]int{}
	for i := 0; i < perMinute+3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/clip",
			strings.NewReader(`{"id":"`+strconv.Itoa(i)+`","text":"hi","expires":`+itoa(future.Unix())+`}`))
		req.RemoteAddr = "[fd00::3]:4000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		codes[w.Code]++
	}
	if codes[http.StatusTooManyRequests] != 3 {
		t.Fatalf("%v", codes)
	}
}
