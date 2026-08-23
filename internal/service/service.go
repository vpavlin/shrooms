// Package service publishes local ports on the mesh under their own names.
//
// A device declares what it runs — `services = ["immich:2283"]` — and the
// service becomes reachable from every other device as
// `http://immich.<device>.mesh`, without the application knowing the mesh
// exists. The declared port still works too; the bare name works because of the
// shared-port name router in router.go.
//
// The reason this needs code at all, rather than being pure DNS, is that
// `immich.home-server.mesh` resolves to an IPv6 address and a great many
// self-hosted applications bind `0.0.0.0` and nothing else. Resolving the name
// would then hand out an address the application is not listening on, which
// presents as "DNS works but the site does not load" — the worst kind of
// half-working. So the daemon listens on the overlay address itself and
// forwards to loopback, where the application actually is.
//
// It is a forwarder, not a proxy in any protocol sense: bytes are copied and TLS
// is not terminated.
//
// One exception, in this package and worth knowing about: the name router reads
// the HTTP Host header or the TLS SNI of connections to 80 and 443 to decide
// which device they are for. It parses, it does not decrypt or terminate — but
// "nothing is inspected", which this said, was not true of the package.
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
	"sync"
	"syscall"
	"time"
)

// dialTimeout bounds how long a connection waits for the local application.
// Short: the target is loopback, so anything slow is something being down
// rather than something being far away.
const dialTimeout = 5 * time.Second

// Spec is one published service.
type Spec struct {
	// Name is the leftmost DNS label: "immich" in immich.home-server.mesh.
	Name string

	// Port is the port published on the overlay address.
	Port uint16

	// TLS says the service speaks TLS, so the name router may accept HTTPS for
	// it and route on SNI.
	//
	// Off unless asked for, because the alternative was worse than not serving
	// HTTPS at all: the router accepted 443, matched the name, and handed a
	// browser's ClientHello to a plain HTTP server, which answered with an
	// error the browser could make no sense of. Written as "name:port/tls".
	TLS bool

	// Target is where connections are forwarded, as host:port.
	// Defaults to 127.0.0.1:<Port>.
	Target string

	// Type is what this service IS, as opposed to what it is called here.
	//
	// Name is a nickname: chosen per install, meaningful because a human reads
	// it. Type is agreed in advance and means the same thing on every device,
	// which is what makes "does this mesh have a logos-storage?" a question
	// with an answer. Written as "name:port/type=logos-storage".
	//
	// Empty unless declared, deliberately. Defaulting it to Name would make
	// every existing nickname claim a type and destroy the only property that
	// makes a type worth having — that it was agreed with somebody.
	//
	// The namespace is IANA's service names (RFC 6335): at most 15 characters,
	// letters, digits and hyphens, which is the registry every DNS-SD service
	// already uses.
	Type string
}

// MaxTypeLen is RFC 6335's limit on a service name.
const MaxTypeLen = 15

// String renders a Spec in the config syntax that produced it.
//
// The one place that knows how to build a declaration, so that `services add`,
// a UI, or a future registration API cannot drift from what ParseSpec accepts.
// The round trip is tested — this method silently dropped Type the moment Type
// was added, which is exactly the drift the test exists to catch.
func (s Spec) String() string {
	suffix := ""
	if s.TLS {
		suffix += "/tls"
	}
	if s.Type != "" {
		suffix += "/type=" + s.Type
	}
	if s.Target == defaultTarget(s.Port) {
		return fmt.Sprintf("%s:%d%s", s.Name, s.Port, suffix)
	}
	return fmt.Sprintf("%s:%d->%s%s", s.Name, s.Port, s.Target, suffix)
}

func defaultTarget(port uint16) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
}

// ParseSpec reads one service declaration.
//
//	immich:2283                     published and forwarded on the same port
//	jellyfin:8096->127.0.0.1:8920   published on 8096, forwarded to 8920
//	ha->192.168.0.116:80            published on the target's port, 80
//
// The last form is for reaching something that is not on the mesh at all: the
// port is usually not a choice, it is whatever that device already serves on,
// and repeating it either side of the arrow is noise.
//
// Deliberately a flat string rather than a config table: the config parser is a
// TOML subset with no nesting (internal/state), and one line per service reads
// better than four anyway.
func ParseSpec(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, errors.New("empty service")
	}

	// Trailing "/" flags, in any order: "/tls" marks a service the name router
	// may serve over HTTPS, "/type=..." says what the service is.
	//
	// A loop rather than one CutSuffix, so that a declaration can carry both.
	// The old code stripped "/tls" only when it was the very last thing, which
	// its own comment described as "anywhere in the declaration".
	tlsFlag, svcType := false, ""
	for {
		s = strings.TrimSpace(s)
		cut := strings.LastIndex(s, "/")
		if cut < 0 {
			break
		}
		flag := strings.TrimSpace(s[cut+1:])
		switch {
		case flag == "tls":
			tlsFlag = true
		case strings.HasPrefix(flag, "type="):
			svcType = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(flag, "type=")))
			if err := ValidType(svcType); err != nil {
				return Spec{}, fmt.Errorf("%q: %w", s, err)
			}
		default:
			// Not a flag — part of a target like "->192.168.0.1/32" would not
			// parse anyway, but leaving it alone gives a better error below.
			cut = -1
		}
		if cut < 0 {
			break
		}
		s = s[:cut]
	}

	decl, target, hasTarget := strings.Cut(s, "->")
	decl = strings.TrimSpace(decl)
	target = strings.TrimSpace(target)

	name, portStr, hasPort := strings.Cut(decl, ":")
	name = sanitiseLabel(name)
	if name == "" {
		return Spec{}, fmt.Errorf("%q: service name is empty or has no usable characters", s)
	}
	if !hasPort && !hasTarget {
		return Spec{}, fmt.Errorf("%q: expected name:port, as in \"immich:2283\"", s)
	}

	var port uint64
	if hasPort {
		var err error
		port, err = strconv.ParseUint(strings.TrimSpace(portStr), 10, 16)
		if err != nil || port == 0 {
			return Spec{}, fmt.Errorf("%q: %q is not a port", s, portStr)
		}
	} else {
		// "ha->192.168.0.116:80": publish on whatever the target serves on.
		_, tp, err := net.SplitHostPort(target)
		if err != nil {
			return Spec{}, fmt.Errorf("%q: target %q is not host:port", s, target)
		}
		port, err = strconv.ParseUint(tp, 10, 16)
		if err != nil || port == 0 {
			return Spec{}, fmt.Errorf("%q: %q is not a port", s, tp)
		}
	}

	spec := Spec{Name: name, Port: uint16(port), TLS: tlsFlag, Type: svcType,
		Target: defaultTarget(uint16(port))}
	if hasTarget {
		if target == "" {
			return Spec{}, fmt.Errorf("%q: nothing after ->", s)
		}
		// A bare port after -> means loopback, since that is the only thing
		// this is for: "immich:443->2283".
		if p, err := strconv.ParseUint(target, 10, 16); err == nil && p > 0 {
			target = defaultTarget(uint16(p))
		}
		if _, _, err := net.SplitHostPort(target); err != nil {
			return Spec{}, fmt.Errorf("%q: target %q is not host:port", s, target)
		}
		spec.Target = target
	}
	return spec, nil
}

// ParseSpecs reads a list, rejecting duplicate names and ports.
//
// Both duplicates are silent failures otherwise: two services with one name
// means one is unreachable by name, and two on one port means whichever binds
// second is dropped. Better to refuse the config than to run three quarters of
// it.
func ParseSpecs(list []string) ([]Spec, error) {
	var out []Spec
	names := make(map[string]bool)
	ports := make(map[uint16]string)
	for _, s := range list {
		spec, err := ParseSpec(s)
		if err != nil {
			return nil, err
		}
		if names[spec.Name] {
			return nil, fmt.Errorf("service %q is declared twice", spec.Name)
		}
		if other, taken := ports[spec.Port]; taken {
			return nil, fmt.Errorf("services %q and %q both publish port %d",
				other, spec.Name, spec.Port)
		}
		names[spec.Name] = true
		ports[spec.Port] = spec.Name
		out = append(out, spec)
	}
	return out, nil
}

// sanitiseLabel mirrors mesh name sanitising, so a service name resolves as
// written rather than as something adjacent to it.
func sanitiseLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Status is what one published service is currently doing.
type Status struct {
	Spec

	// Listening is false when the daemon is not holding the port — either
	// because the bind failed, or because Direct is true.
	Listening bool

	// Direct means something else already holds the port on this address, so
	// the mesh reaches the application without help. Not an error: an
	// application that binds :: or the overlay address needs no forwarding, and
	// taking the port from it would break it.
	Direct bool

	// Err is why the service is unavailable, empty when it is fine.
	Err string

	// Conns is how many connections have been accepted.
	Conns uint64
}

// Publisher holds the listeners for a device's services.
type Publisher struct {
	log    func(msg string, args ...any)
	mu     sync.Mutex
	stats  map[string]*Status
	router []RouterStatus
	lns    []net.Listener
}

// Publish binds every service to addr and forwards to its target.
//
// A single service failing to bind does not fail the rest, and never fails the
// daemon: services are a convenience layered on the mesh, and losing one is not
// a reason to lose the tunnel. What is refused is silence — every outcome is
// logged and reported in status.
func Publish(ctx context.Context, addr netip.Addr, device string, specs []Spec, log func(msg string, args ...any)) *Publisher {
	if log == nil {
		log = func(string, ...any) {}
	}
	p := &Publisher{log: log, stats: make(map[string]*Status)}

	// Every Status exists before any accept loop runs, so a goroutine can never
	// observe a half-built map. The listeners are started in a second pass for
	// the same reason.
	for _, spec := range specs {
		p.stats[spec.Name] = &Status{Spec: spec}
	}

	for _, spec := range specs {
		st := p.stats[spec.Name]

		ap := netip.AddrPortFrom(addr, spec.Port)
		ln, err := net.Listen("tcp6", ap.String())
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				// Something already listens here, and nothing here identifies
				// what. This used to say it "can only be the application itself
				// binding ::", which is not true: any local process that bound
				// the port first wins, and for a service port above 1024 -
				// immich, jellyfin, the ordinary case - that process needs no
				// privilege. It then serves every member of the mesh under this
				// device's name, and status reported it as healthy.
				//
				// Still not forwarded, because taking the port is not ours to
				// do and the usual cause really is the application. But it is
				// reported as unverified rather than as the application, and
				// said once at warn level so it is visible if it was not.
				st.Direct = true
				log("service port already held by another process; not forwarding",
					"service", spec.Name, "port", spec.Port,
					"note", "shrooms cannot tell which process holds it — if this "+
						"is not the application, it is serving the mesh under this device's name")
				continue
			}
			st.Err = err.Error()
			log("service could not be published", "service", spec.Name, "err", err)
			continue
		}
		st.Listening = true
		p.mu.Lock()
		p.lns = append(p.lns, ln)
		p.mu.Unlock()
		log("service published", "service", spec.Name,
			"listen", ap.String(), "target", spec.Target)

		go p.accept(ctx, ln, spec, st)
	}

	// One more listener per shared port, so a service can be reached by name
	// alone. See router.go for why this cannot simply be DNS.
	if len(specs) > 0 {
		p.serveNames(ctx, addr, device, specs)
	}

	// Closing the listeners is what unblocks the accept loops.
	go func() {
		<-ctx.Done()
		p.Close()
	}()
	return p
}

func (p *Publisher) accept(ctx context.Context, ln net.Listener, spec Spec, st *Status) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				p.log("service stopped accepting", "service", spec.Name, "err", err)
			}
			return
		}
		p.mu.Lock()
		st.Conns++
		p.mu.Unlock()
		go p.forward(ctx, c, spec)
	}
}

func (p *Publisher) forward(ctx context.Context, in net.Conn, spec Spec) {
	defer in.Close()

	d := net.Dialer{Timeout: dialTimeout}
	out, err := d.DialContext(ctx, "tcp", spec.Target)
	if err != nil {
		// The mesh side worked and the application is not there. Worth a line:
		// from the client this is an immediate reset and looks like the mesh.
		p.log("service unreachable locally",
			"service", spec.Name, "target", spec.Target, "err", err)
		return
	}
	defer out.Close()

	// Half-close each direction as it ends, so a client that shuts down writes
	// and waits for a reply — HTTP/1.0, some database protocols — still gets it.
	var wg sync.WaitGroup
	wg.Add(2)
	go copyThenCloseWrite(&wg, out, in)
	go copyThenCloseWrite(&wg, in, out)
	wg.Wait()
}

func copyThenCloseWrite(wg *sync.WaitGroup, dst, src net.Conn) {
	defer wg.Done()
	_, _ = io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	dst.Close()
}

// Router reports what the shared-port name router is doing.
func (p *Publisher) Router() []RouterStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]RouterStatus(nil), p.router...)
}

// Status reports every declared service.
func (p *Publisher) Status() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Status, 0, len(p.stats))
	for _, st := range p.stats {
		out = append(out, *st)
	}
	return out
}

// Has reports whether a service name is published here.
func (p *Publisher) Has(name string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.stats[sanitiseLabel(name)]
	return ok
}

// Close releases every listener.
func (p *Publisher) Close() {
	p.mu.Lock()
	lns := p.lns
	p.lns = nil
	p.mu.Unlock()
	for _, ln := range lns {
		ln.Close()
	}
}

// ValidType checks a service type against IANA's rules for a service name
// (RFC 6335): at most 15 characters, letters, digits and hyphens, at least one
// letter, and no leading, trailing or doubled hyphen.
//
// Enforced rather than merely documented, because a type only means anything if
// everybody spells it the same way, and the registry that makes that true has
// rules. A type this rejects is one that could never be registered.
func ValidType(t string) error {
	if t == "" {
		return errors.New("service type is empty")
	}
	if len(t) > MaxTypeLen {
		return fmt.Errorf("service type %q is %d characters, over the %d that "+
			"IANA allows for a service name (RFC 6335)", t, len(t), MaxTypeLen)
	}
	letter := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z':
			letter = true
		case c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(t)-1 || t[i-1] == '-' {
				return fmt.Errorf("service type %q: a hyphen may not lead, "+
					"trail or double", t)
			}
		default:
			return fmt.Errorf("service type %q: only letters, digits and "+
				"hyphens are allowed", t)
		}
	}
	if !letter {
		return fmt.Errorf("service type %q must contain a letter", t)
	}
	return nil
}
