package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HTTPPort and TLSPort are where the name router listens, so that a service can
// be reached as `http://immich.home-server.mesh` with no port at all.
//
// Every service on a device shares that device's one overlay address, so port
// 80 cannot simply belong to one of them. What distinguishes them is the name
// the client asked for, and both protocols say it on the wire before anything
// else: HTTP in the Host header, TLS in the SNI extension. So the router reads
// that first and forwards accordingly — one port, many names, which is what a
// reverse proxy has always been.
//
// The limits are worth being plain about. It works for HTTP and TLS and nothing
// else, because nothing else announces the name it dialled; ssh, syncthing and
// databases still need their port. Giving every service its own address is the
// general answer and needs a change to how addresses are derived (ADR-019).
const (
	HTTPPort = 80
	TLSPort  = 443
)

// headTimeout bounds how long a connection may take to say which name it wants.
// A client that connects and sends nothing is holding a slot; TLS and HTTP both
// send their first bytes immediately.
const headTimeout = 10 * time.Second

// maxHead is how much is read while looking for the name. Enough for any
// realistic request line plus headers, and small enough that a client sending
// junk cannot make us buffer without limit.
const maxHead = 16 << 10

// router forwards by name on a shared port.
type router struct {
	device string // this device's DNS name, e.g. "home-server.mesh"
	routes map[string]Spec
	log    func(msg string, args ...any)
}

// RouterStatus is what the name router is doing, for status output.
type RouterStatus struct {
	Port      uint16
	Listening bool
	Direct    bool
	Err       string
}

// serveNames starts the name router on the shared ports.
//
// Failure to bind is reported and not fatal, exactly as for a single service:
// the explicit `name.device.mesh:port` form keeps working regardless, so what
// is lost is the convenience and not the service.
func (p *Publisher) serveNames(ctx context.Context, addr netip.Addr, device string, specs []Spec) {
	r := &router{device: strings.Trim(device, "."), routes: make(map[string]Spec), log: p.log}
	for _, s := range specs {
		r.routes[s.Name] = s
	}

	for _, port := range []uint16{HTTPPort, TLSPort} {
		st := RouterStatus{Port: port}
		ap := netip.AddrPortFrom(addr, port)
		ln, err := net.Listen("tcp6", ap.String())
		if err != nil {
			switch {
			case errors.Is(err, syscall.EADDRINUSE):
				st.Direct = true
				// Most often one of our own services declared this port. That
				// service still works and still answers its own name, but no
				// OTHER service on this device can be reached without a port —
				// which is invisible unless it is said out loud.
				owner := ""
				for _, spec := range specs {
					if spec.Port == port {
						owner = spec.Name
					}
				}
				if owner != "" && len(specs) > 1 {
					p.log("name routing not started",
						"port", port, "taken-by-service", owner,
						"note", "other services on this device need their port in the URL")
				} else {
					p.log("name routing not started", "port", port,
						"note", "another process holds this port")
				}
			case errors.Is(err, syscall.EACCES):
				// Ports below 1024 need the capability the daemon already
				// needs for DNS. Named rather than left as "permission denied",
				// which sends people to file permissions.
				st.Err = "needs CAP_NET_BIND_SERVICE"
				p.log("name routing unavailable", "port", port,
					"hint", "port "+fmt.Sprint(port)+" needs CAP_NET_BIND_SERVICE")
			default:
				st.Err = err.Error()
				p.log("name routing unavailable", "port", port, "err", err)
			}
			p.mu.Lock()
			p.router = append(p.router, st)
			p.mu.Unlock()
			continue
		}

		st.Listening = true
		p.mu.Lock()
		p.lns = append(p.lns, ln)
		p.router = append(p.router, st)
		p.mu.Unlock()

		tls := port == TLSPort
		p.log("name routing up", "port", port, "names", len(r.routes))
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					if ctx.Err() == nil {
						p.log("name routing stopped", "err", err)
					}
					return
				}
				go r.handle(ctx, c, tls)
			}
		}(ln)
	}
}

func (r *router) handle(ctx context.Context, in net.Conn, isTLS bool) {
	defer in.Close()

	in.SetReadDeadline(time.Now().Add(headTimeout))
	head, name, err := readName(in, isTLS)
	if err != nil {
		r.log("could not read the requested name", "err", err, "tls", isTLS)
		return
	}
	in.SetReadDeadline(time.Time{})

	spec, ok := r.match(name)
	if !ok {
		// Answering is much better than dropping: a browser shown "connection
		// reset" says nothing, and this is exactly the moment someone needs to
		// know which names exist.
		if !isTLS {
			r.explain(in, name)
		}
		return
	}

	d := net.Dialer{Timeout: dialTimeout}
	out, err := d.DialContext(ctx, "tcp", spec.Target)
	if err != nil {
		r.log("service unreachable locally",
			"service", spec.Name, "target", spec.Target, "err", err)
		if !isTLS {
			writeStatus(in, "502 Bad Gateway",
				fmt.Sprintf("%s is published here but is not answering on %s.\n",
					spec.Name, spec.Target))
		}
		return
	}
	defer out.Close()

	// Replay what was read to find the name. The application must see its
	// request whole; nothing was consumed on its behalf.
	if _, err := out.Write(head); err != nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go copyThenCloseWrite(&wg, out, in)
	go copyThenCloseWrite(&wg, in, out)
	wg.Wait()
}

// match finds the service a requested name asks for.
//
// The name is `<service>.<device>.<suffix>`, and only the leftmost label is
// used: the rest is how the client reached this machine, and this machine is
// already the one answering.
func (r *router) match(name string) (Spec, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	label, _, _ := strings.Cut(name, ".")
	s, ok := r.routes[label]
	return s, ok
}

// explain answers a request for a name this device does not publish.
func (r *router) explain(w io.Writer, asked string) {
	var b strings.Builder
	if asked == "" {
		b.WriteString("This is a logos-vpn device.\n\n")
	} else {
		fmt.Fprintf(&b, "%q is not published on this device.\n\n", asked)
	}
	if len(r.routes) == 0 {
		b.WriteString("Nothing is published here.\n")
	} else {
		b.WriteString("Published here:\n")
		for _, s := range sortedNames(r.routes) {
			fmt.Fprintf(&b, "  http://%s.%s\n", s, r.device)
		}
	}
	writeStatus(w, "404 Not Found", b.String())
}

func sortedNames(m map[string]Spec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: a handful of names, and it avoids importing sort here.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func writeStatus(w io.Writer, status, body string) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n\r\n%s", status, len(body), body)
}

// readName reads the beginning of a connection and returns both the bytes read
// and the name the client asked for. The bytes are returned because they must
// be replayed to the application, which has to see its request whole.
func readName(c net.Conn, isTLS bool) (head []byte, name string, err error) {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for len(buf) < maxHead {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)

		if isTLS {
			sni, done := parseSNI(buf)
			if done {
				return buf, sni, nil
			}
		} else if i := bytes.Index(buf, []byte("\r\n\r\n")); i >= 0 {
			return buf, hostHeader(buf[:i]), nil
		}

		if err != nil {
			return buf, "", err
		}
		if n == 0 {
			return buf, "", errors.New("no data")
		}
	}
	return buf, "", errors.New("no name in the first " + fmt.Sprint(maxHead) + " bytes")
}

// hostHeader pulls Host out of a request head. Case-insensitive, because the
// header name is.
func hostHeader(head []byte) string {
	for _, line := range strings.Split(string(head), "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseSNI extracts the server name from a TLS ClientHello.
//
// Nothing is decrypted or terminated: the ClientHello is cleartext by
// construction, the name is read out of it, and the bytes are then forwarded
// untouched. The application still does its own TLS with its own certificate,
// and this code never holds a private key.
//
// Returns done=false when the hello is incomplete and more bytes should be
// read; done=true with an empty name when there is no SNI to find.
func parseSNI(b []byte) (name string, done bool) {
	// TLS record: type(1) version(2) length(2)
	if len(b) < 5 {
		return "", false
	}
	if b[0] != 0x16 { // not a handshake record; not TLS
		return "", true
	}
	recLen := int(b[3])<<8 | int(b[4])
	if len(b) < 5+recLen {
		return "", false
	}
	h := b[5 : 5+recLen]

	// Handshake: type(1) length(3) version(2) random(32)
	r := reader{h}
	if t, ok := r.u8(); !ok || t != 0x01 {
		return "", true // not a ClientHello
	}
	if _, ok := r.skip(3 + 2 + 32); !ok {
		return "", true
	}
	if !r.skipVec(1) { // session id
		return "", true
	}
	if !r.skipVec(2) { // cipher suites
		return "", true
	}
	if !r.skipVec(1) { // compression methods
		return "", true
	}

	extLen, ok := r.u16()
	if !ok {
		return "", true // no extensions: no SNI
	}
	exts, ok := r.take(extLen)
	if !ok {
		return "", true
	}

	e := reader{exts}
	for {
		typ, ok := e.u16()
		if !ok {
			return "", true
		}
		l, ok := e.u16()
		if !ok {
			return "", true
		}
		body, ok := e.take(l)
		if !ok {
			return "", true
		}
		if typ != 0x0000 { // server_name
			continue
		}
		// ServerNameList: length(2), then entries of type(1) length(2) name.
		s := reader{body}
		listLen, ok := s.u16()
		if !ok {
			return "", true
		}
		list, ok := s.take(listLen)
		if !ok {
			return "", true
		}
		n := reader{list}
		for {
			nameType, ok := n.u8()
			if !ok {
				return "", true
			}
			nl, ok := n.u16()
			if !ok {
				return "", true
			}
			v, ok := n.take(nl)
			if !ok {
				return "", true
			}
			if nameType == 0 { // host_name
				return strings.ToLower(string(v)), true
			}
		}
	}
}

// reader is a bounds-checked cursor. Every read returns ok=false rather than
// panicking, because this parses the first bytes any peer chooses to send.
type reader struct{ b []byte }

func (r *reader) u8() (byte, bool) {
	if len(r.b) < 1 {
		return 0, false
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v, true
}

func (r *reader) u16() (int, bool) {
	if len(r.b) < 2 {
		return 0, false
	}
	v := int(r.b[0])<<8 | int(r.b[1])
	r.b = r.b[2:]
	return v, true
}

func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || len(r.b) < n {
		return nil, false
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v, true
}

func (r *reader) skip(n int) ([]byte, bool) { return r.take(n) }

// skipVec skips a length-prefixed vector whose length field is lenBytes wide.
func (r *reader) skipVec(lenBytes int) bool {
	var n int
	switch lenBytes {
	case 1:
		v, ok := r.u8()
		if !ok {
			return false
		}
		n = int(v)
	case 2:
		v, ok := r.u16()
		if !ok {
			return false
		}
		n = v
	}
	_, ok := r.take(n)
	return ok
}
