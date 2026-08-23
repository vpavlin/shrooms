package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in     string
		want   Spec
		wantNo bool
	}{
		{in: "immich:2283", want: Spec{Name: "immich", Port: 2283, Target: "127.0.0.1:2283"}},
		{in: "  jellyfin : 8096 ", want: Spec{Name: "jellyfin", Port: 8096, Target: "127.0.0.1:8096"}},
		// A bare port after -> is loopback, which is the only thing this is for.
		{in: "grafana:443->3000", want: Spec{Name: "grafana", Port: 443, Target: "127.0.0.1:3000"}},
		{in: "nas:80->192.168.1.5:8080", want: Spec{Name: "nas", Port: 80, Target: "192.168.1.5:8080"}},
		// Names are sanitised exactly as device names are, so what is written
		// in the config is what resolves.
		{in: "Photo_Prism:2342", want: Spec{Name: "photo-prism", Port: 2342, Target: "127.0.0.1:2342"}},

		{in: "immich", wantNo: true},        // no port
		{in: "immich:0", wantNo: true},      // port 0 binds "anything"
		{in: "immich:99999", wantNo: true},  // out of range
		{in: ":2283", wantNo: true},         // no name
		{in: "!!:2283", wantNo: true},       // name sanitises to nothing
		{in: "a:1->", wantNo: true},         // nothing after ->
		{in: "a:1->nonsense", wantNo: true}, // target is not host:port
		{in: "", wantNo: true},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.in)
		if c.wantNo {
			if err == nil {
				t.Errorf("ParseSpec(%q) = %+v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSpec(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// Duplicates are refused rather than half-applied: with two services on one
// port, whichever binds second silently does not exist.
func TestParseSpecsRejectsDuplicates(t *testing.T) {
	if _, err := ParseSpecs([]string{"immich:2283", "immich:9000"}); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := ParseSpecs([]string{"immich:2283", "photos:2283"}); err == nil {
		t.Error("duplicate port accepted")
	}
	// Different names on different ports are the ordinary case.
	if _, err := ParseSpecs([]string{"immich:2283", "jellyfin:8096"}); err != nil {
		t.Errorf("valid list rejected: %v", err)
	}
}

func TestSpecString(t *testing.T) {
	for _, in := range []string{"immich:2283", "nas:80->192.168.1.5:8080"} {
		s, err := ParseSpec(in)
		if err != nil {
			t.Fatal(err)
		}
		if s.String() != in {
			t.Errorf("round trip: %q -> %q", in, s.String())
		}
	}
}

// The point of the whole package: a connection to the overlay address reaches
// an application listening only on IPv4 loopback.
func TestForwardsToLoopback(t *testing.T) {
	// The application, bound to 127.0.0.1 and nothing else — the case that
	// makes plain DNS insufficient.
	app, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go echo(app)

	// ::1 stands in for the overlay address: this test must not depend on a
	// mesh interface existing.
	ln, port := freePort(t)
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pub := Publish(ctx, netip.MustParseAddr("::1"), "test.mesh", []Spec{{
		Name: "app", Port: port, Target: app.Addr().String(),
	}}, nil)
	defer pub.Close()

	c, err := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial the published service: %v", err)
	}
	defer c.Close()

	if _, err := io.WriteString(c, "hello"); err != nil {
		t.Fatal(err)
	}
	// Half-close, which is what tells the echo server the request is over.
	// Getting a reply after it is the reason forwarding closes one direction
	// at a time rather than tearing down both.
	c.(*net.TCPConn).CloseWrite()

	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}

	st := pub.Status()
	if len(st) != 1 || !st[0].Listening || st[0].Conns != 1 {
		t.Errorf("status = %+v, want one listening service with one connection", st)
	}
}

// A service whose application is not running must fail as a refused connection,
// not as a hang: the daemon accepted, so the client is already waiting.
func TestApplicationDownClosesConnection(t *testing.T) {
	dead, port := freePort(t)
	dead.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Target a port nothing listens on.
	other, otherPort := freePort(t)
	other.Close()

	pub := Publish(ctx, netip.MustParseAddr("::1"), "test.mesh", []Spec{{
		Name: "gone", Port: port, Target: fmt.Sprintf("127.0.0.1:%d", otherPort),
	}}, nil)
	defer pub.Close()

	c, err := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := io.ReadAll(c); err != nil && !errors.Is(err, io.EOF) {
		// A reset is fine; a timeout is not.
		if strings.Contains(err.Error(), "timeout") {
			t.Errorf("connection hung instead of closing: %v", err)
		}
	}
}

// Something else already holding the port is reported as reachable-directly
// rather than as an error: an application that binds :: needs no forwarding,
// and taking the port from it would break it.
func TestPortAlreadyHeldIsNotAnError(t *testing.T) {
	ln, port := freePort(t)
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pub := Publish(ctx, netip.MustParseAddr("::1"), "test.mesh", []Spec{{
		Name: "already", Port: port, Target: "127.0.0.1:1",
	}}, nil)
	defer pub.Close()

	st := pub.Status()
	if len(st) != 1 {
		t.Fatalf("status = %+v", st)
	}
	if !st[0].Direct || st[0].Err != "" || st[0].Listening {
		t.Errorf("status = %+v, want Direct with no error", st[0])
	}
}

// Cancelling the context must release the ports, or a daemon restart cannot
// rebind them.
func TestCloseReleasesPorts(t *testing.T) {
	ln, port := freePort(t)
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	pub := Publish(ctx, netip.MustParseAddr("::1"), "test.mesh", []Spec{{
		Name: "x", Port: port, Target: "127.0.0.1:1",
	}}, nil)
	if !pub.Status()[0].Listening {
		t.Fatal("did not bind")
	}
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
		if err == nil {
			l.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("port still held after the context was cancelled")
}

// freePort returns a listener on ::1 and its port. The caller closes it and
// reuses the port; briefly racy, and the alternative is a hardcoded port that
// is racy against every other test on the machine.
func freePort(t *testing.T) (net.Listener, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	return ln, uint16(ln.Addr().(*net.TCPAddr).Port)
}

func echo(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			io.Copy(c, c)
		}()
	}
}

// A service may forward to another machine entirely, which turns this device
// into a gateway for something that is not on the mesh at all — a Home
// Assistant box, a printer, a NAS web UI.
//
// Same code path as loopback (a dialled address is a dialled address), but it
// is the case people will actually reach for and it deserves a test that says
// so, because "127.0.0.1 only" is the assumption the package name suggests.
func TestForwardsToAnotherHost(t *testing.T) {
	// Bound to a real interface address rather than loopback, standing in for
	// a device elsewhere on the LAN.
	lan, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skip("no usable address")
	}
	defer lan.Close()
	go echo(lan)

	free, port := freePort(t)
	free.Close() // release it before Publish binds the same port
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(lan.Addr().(*net.TCPAddr).Port))

	spec, err := ParseSpec("ha:" + strconv.Itoa(int(port)) + "->" + target)
	if err != nil {
		t.Fatalf("a LAN target was rejected: %v", err)
	}
	if spec.Target != target {
		t.Fatalf("target = %q, want %q", spec.Target, target)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pub := Publish(ctx, netip.MustParseAddr("::1"), "jimmy-crib.mesh", []Spec{spec}, nil)
	defer pub.Close()

	c, err := net.DialTimeout("tcp6", fmt.Sprintf("[::1]:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	io.WriteString(c, "reaches the other host")
	c.(*net.TCPConn).CloseWrite()

	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "reaches the other host" {
		t.Errorf("got %q", got)
	}
}

// "ha->192.168.0.116:80" — no published port, because for a device that is not
// on the mesh the port is not a choice: it is whatever that device already
// serves on, and repeating it either side of the arrow is noise.
func TestPortDefaultsToTheTargetPort(t *testing.T) {
	got, err := ParseSpec("ha->192.168.0.116:80")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	want := Spec{Name: "ha", Port: 80, Target: "192.168.0.116:80"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// A bare name with no target is still an error: there is nothing to infer.
	if _, err := ParseSpec("ha"); err == nil {
		t.Error("a name with neither a port nor a target was accepted")
	}
	// And a target without a port cannot supply one.
	if _, err := ParseSpec("ha->192.168.0.116"); err == nil {
		t.Error("a target with no port was accepted")
	}
}

// A browser tries HTTPS first. If the router listens on 443 and forwards a
// ClientHello to a plain HTTP server, the browser gets a broken TLS
// conversation instead of a closed port and hangs — so 443 is served only for
// services that say they speak it.
func TestTLSIsOptIn(t *testing.T) {
	plain, err := ParseSpec("jellyfin:8096")
	if err != nil {
		t.Fatal(err)
	}
	if plain.TLS {
		t.Error("a plain service claimed TLS")
	}

	secure, err := ParseSpec("vault:8200/tls")
	if err != nil {
		t.Fatal(err)
	}
	if !secure.TLS || secure.Name != "vault" || secure.Port != 8200 {
		t.Errorf("parsed as %+v", secure)
	}
	// And with a target, which is the form that redirects elsewhere.
	withTarget, err := ParseSpec("ha->192.168.0.116:443/tls")
	if err != nil {
		t.Fatal(err)
	}
	if !withTarget.TLS || withTarget.Target != "192.168.0.116:443" {
		t.Errorf("parsed as %+v", withTarget)
	}
	// The string form round-trips, or a config rewritten by the daemon would
	// quietly lose the flag.
	back, err := ParseSpec(secure.String())
	if err != nil || !back.TLS {
		t.Errorf("%q did not round-trip: %+v (%v)", secure.String(), back, err)
	}
}

// A type is what a service IS; a name is what it is called here. Both flags
// have to survive in either order, and the old parser only stripped /tls when
// it was the very last thing.
func TestSpecCarriesATypeAlongsideTLS(t *testing.T) {
	for _, c := range []struct {
		in     string
		name   string
		port   uint16
		tls    bool
		typ    string
		target string
	}{
		{"storage:8080/type=logos-storage", "storage", 8080, false, "logos-storage", "127.0.0.1:8080"},
		{"immich:2283/tls", "immich", 2283, true, "", "127.0.0.1:2283"},
		{"immich:443/tls/type=immich", "immich", 443, true, "immich", "127.0.0.1:443"},
		{"immich:443/type=immich/tls", "immich", 443, true, "immich", "127.0.0.1:443"},
		// The nickname and the type are free to differ, which is the point.
		{"backup->127.0.0.1:8080/type=logos-storage", "backup", 8080, false, "logos-storage", "127.0.0.1:8080"},
		// No type unless declared. Defaulting it to the name would make every
		// existing nickname claim a type.
		{"jellyfin:8096", "jellyfin", 8096, false, "", "127.0.0.1:8096"},
	} {
		got, err := ParseSpec(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Name != c.name || got.Port != c.port || got.TLS != c.tls ||
			got.Type != c.typ || got.Target != c.target {
			t.Errorf("%q parsed as %+v", c.in, got)
		}
	}
}

// A type only means anything if everybody spells it the same way, and the
// registry that makes that true has rules. A type this accepts must be one that
// could actually be registered.
func TestTypesFollowTheIANARules(t *testing.T) {
	for _, good := range []string{"logos-storage", "http", "imap", "x1", "a"} {
		if err := ValidType(good); err != nil {
			t.Errorf("rejected %q: %v", good, err)
		}
	}
	for _, bad := range []struct{ in, why string }{
		{"", "empty"},
		{"this-name-is-far-too-long", "over 15 characters"},
		{"-leading", "leading hyphen"},
		{"trailing-", "trailing hyphen"},
		{"double--hyphen", "doubled hyphen"},
		{"1234", "no letter"},
		{"has_underscore", "underscore"},
		{"has.dot", "dot"},
		{"has space", "space"},
	} {
		if err := ValidType(bad.in); err == nil {
			t.Errorf("accepted %q (%s)", bad.in, bad.why)
		}
	}
	// And a bad type must fail the whole declaration rather than being dropped.
	if _, err := ParseSpec("storage:8080/type=has_underscore"); err == nil {
		t.Error("accepted a declaration with an unregisterable type")
	}
}

// Anything that builds a declaration must build one ParseSpec accepts, or
// `services add` writes config the daemon then refuses to load.
func TestSpecRoundTrips(t *testing.T) {
	for _, in := range []string{
		"immich:2283",
		"jellyfin:8096->127.0.0.1:8920",
		"ha->192.168.0.116:80",
		"immich:443/tls",
		"backup:8080/type=logos-storage",
		"backup:8080->127.0.0.1:9090/tls/type=logos-storage",
	} {
		spec, err := ParseSpec(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		again, err := ParseSpec(spec.String())
		if err != nil {
			t.Errorf("%q rendered as %q, which does not parse: %v", in, spec.String(), err)
			continue
		}
		if again != spec {
			t.Errorf("%q -> %q -> %+v, want %+v", in, spec.String(), again, spec)
		}
	}
}
