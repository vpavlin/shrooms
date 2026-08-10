package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newRouter builds a router over a fake application and returns a dial function
// that speaks to it as a client would.
//
// The listeners are ordinary loopback sockets, not ports 80 and 443: binding
// those needs a capability tests do not have, and what is under test is which
// name goes where, not which port it arrived on.
func newRouter(t *testing.T, app string, specs ...Spec) *router {
	t.Helper()
	r := &router{device: "home-server.mesh", routes: map[string]Spec{}, log: func(string, ...any) {}}
	for _, s := range specs {
		if s.Target == "" {
			s.Target = app
		}
		r.routes[s.Name] = s
	}
	return r
}

// dial runs one connection through the router and returns the client side.
func dial(t *testing.T, r *router, isTLS bool) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go r.handle(context.Background(), server, isTLS)
	t.Cleanup(func() { client.Close() })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	return client
}

// The ask this exists for: no port in the URL.
func TestRoutesByHostHeader(t *testing.T) {
	app := serveText(t, "immich here")
	other := serveText(t, "jellyfin here")

	r := newRouter(t, "",
		Spec{Name: "immich", Port: 2283, Target: app},
		Spec{Name: "jellyfin", Port: 8096, Target: other},
	)

	for _, c := range []struct{ host, want string }{
		{"immich.home-server.mesh", "immich here"},
		{"jellyfin.home-server.mesh", "jellyfin here"},
		// A port in the Host header is the client telling us where it
		// connected, not part of the name.
		{"immich.home-server.mesh:80", "immich here"},
		// Host headers are case-insensitive.
		{"IMMICH.Home-Server.Mesh", "immich here"},
	} {
		conn := dial(t, r, false)
		io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+c.host+"\r\n\r\n")
		body := readBody(t, conn)
		if body != c.want {
			t.Errorf("Host %q got %q, want %q", c.host, body, c.want)
		}
		conn.Close()
	}
}

// A name this device does not publish must say so, and say what it does
// publish. A dropped connection at this moment tells the user nothing.
func TestUnknownNameExplains(t *testing.T) {
	r := newRouter(t, "127.0.0.1:1",
		Spec{Name: "immich", Port: 2283},
		Spec{Name: "jellyfin", Port: 8096},
	)

	conn := dial(t, r, false)
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: nothing.home-server.mesh\r\n\r\n")

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	// Both names, and in a form that can be pasted into a browser.
	for _, want := range []string{"http://immich.home-server.mesh", "http://jellyfin.home-server.mesh"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not offer %q:\n%s", want, body)
		}
	}
}

// A published service whose application is down is a 502, not silence: the
// distinction between "no such name" and "the app is not running" is the whole
// of the debugging.
func TestPublishedButDownIs502(t *testing.T) {
	dead, port := freePort(t)
	dead.Close()

	r := newRouter(t, "", Spec{Name: "immich", Port: 2283, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))})

	conn := dial(t, r, false)
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: immich.home-server.mesh\r\n\r\n")

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// TLS routes on SNI, and the bytes must arrive at the application untouched:
// the router reads the name out of a cleartext ClientHello and terminates
// nothing, so the application's own certificate is what the client sees.
func TestRoutesBySNIWithoutTouchingTheBytes(t *testing.T) {
	hello := clientHello(t, "immich.home-server.mesh")

	got := make(chan []byte, 1)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, len(hello))
		io.ReadFull(c, buf)
		got <- buf
	}()

	r := newRouter(t, "", Spec{Name: "immich", Port: 443, Target: ln.Addr().String()})
	conn := dial(t, r, true)
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}

	select {
	case b := <-got:
		if !bytes.Equal(b, hello) {
			t.Error("the ClientHello was modified in transit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ClientHello never reached the application")
	}
}

func TestParseSNI(t *testing.T) {
	hello := clientHello(t, "immich.home-server.mesh")

	name, done := parseSNI(hello)
	if !done || name != "immich.home-server.mesh" {
		t.Errorf("parseSNI = %q, %v", name, done)
	}

	// A partial hello must ask for more bytes rather than give up: TCP does not
	// promise the whole record in one read.
	for _, n := range []int{1, 4, 5, 20, len(hello) - 1} {
		if _, done := parseSNI(hello[:n]); done {
			t.Errorf("parseSNI gave up on the first %d bytes", n)
		}
	}

	// Anything that is not a TLS handshake is done immediately with no name,
	// rather than being read forever.
	if _, done := parseSNI([]byte("GET / HTTP/1.1\r\n\r\n")); !done {
		t.Error("parseSNI kept reading a non-TLS connection")
	}
}

// The parser runs on the first bytes any mesh member chooses to send, so it
// must return rather than panic on anything.
func TestParseSNISurvivesGarbage(t *testing.T) {
	hello := clientHello(t, "x.mesh")
	for i := range hello {
		b := append([]byte(nil), hello...)
		b[i] ^= 0xff
		parseSNI(b) // must not panic
	}
	for _, junk := range [][]byte{
		{0x16},
		{0x16, 0x03, 0x01, 0xff, 0xff},
		{0x16, 0x03, 0x01, 0x00, 0x02, 0x01, 0x00},
		bytes.Repeat([]byte{0x16}, 4096),
	} {
		parseSNI(junk)
	}
}

// clientHello returns a real TLS ClientHello for a name, produced by the TLS
// stack rather than hand-built, so the parser is tested against what clients
// actually send.
func clientHello(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	c := &captureConn{w: &buf}
	// The handshake cannot complete against a sink; the ClientHello is written
	// before that matters, which is all this needs.
	tls.Client(c, &tls.Config{ServerName: name}).Handshake()
	if buf.Len() == 0 {
		t.Fatal("no ClientHello was written")
	}
	return buf.Bytes()
}

type captureConn struct {
	net.Conn
	w *bytes.Buffer
}

func (c *captureConn) Write(b []byte) (int, error) { return c.w.Write(b) }
func (c *captureConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *captureConn) Close() error                { return nil }
func (c *captureConn) SetDeadline(time.Time) error { return nil }

// serveText starts an application that answers every request with one string,
// and returns its address.
func serveText(t *testing.T, body string) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func readBody(t *testing.T, c net.Conn) string {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
