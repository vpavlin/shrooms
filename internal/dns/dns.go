// Package dns serves names for the mesh and nothing else.
//
// It replaces the /etc/hosts block (ADR-013), which is static, needs root, and
// does not exist on Android — the platform this is ultimately for, and the one
// where domain-scoped resolution is a first-class API rather than a fight.
//
// The rule that matters is what it REFUSES to do. It is authoritative for one
// suffix and answers nothing else: no forwarding, no recursion, no upstream. A
// VPN that quietly becomes the system resolver is a surprise nobody asked for
// and a privacy leak besides, since every query you make would traverse it.
package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Port is the only port a resolver can be told to use on most platforms.
// Android's VpnService.Builder.addDnsServer takes an address and no port.
const Port = 53

// TTL is deliberately short. Names map to derived addresses that never change,
// but which peer answers to a name can, and a stale cache outlives the mesh
// state that produced it.
const TTL = 30

// ServiceAddr is the address the resolver answers on: the mesh prefix with a
// host part of ::53.
//
// Deliberately NOT this device's own address, which was the mistake. A packet
// addressed to an address assigned to the interface is delivered locally by the
// kernel and never traverses the tun — so neither a socket nor the read-path
// intercept ever sees it. Tailscale uses 100.100.100.100 for the same reason.
//
// This address is inside the routed prefix but assigned to nothing, so packets
// for it go into the tun, where the intercept is waiting. Collision with a
// peer is not a concern: host parts are 80-bit hashes of device keys.
func ServiceAddr(prefix netip.Prefix) netip.Addr {
	a := prefix.Addr().As16()
	// Clear the host part, then set ::53.
	for i := 6; i < 16; i++ {
		a[i] = 0
	}
	a[15] = 0x53
	return netip.AddrFrom16(a)
}

// Lookup resolves a single label — "laptop" — to an overlay address.
// Returning false means the name is not in this mesh.
type Lookup func(host string) (netip.Addr, bool)

// Server answers queries for one suffix.
type Server struct {
	// Suffix is the domain served, without dots: "mesh".
	Suffix string
	// Lookup resolves a host label within the suffix.
	Lookup Lookup
	// Log receives one line per interesting event; may be nil.
	Log func(msg string, args ...any)

	// Upstream forwards a query we are not authoritative for, returning the
	// raw response.
	//
	// Required on Android and unwanted anywhere else. Android has no split-DNS:
	// a VpnService that sets a DNS server receives EVERY query, so refusing
	// non-mesh names takes the device's name resolution away entirely — no
	// browsing, no app updates, nothing. Forwarding is the only way to hold the
	// mesh names without holding the rest hostage.
	//
	// Nil means refuse, which is right for the Linux daemon: there the resolver
	// is reached only for the domain it is configured for, so a query outside
	// it is a misconfiguration rather than ordinary traffic.
	//
	// Queries passing through here are proxied verbatim and never logged. It is
	// a pipe, not an observation point.
	Upstream func(query []byte) ([]byte, error)

	mu          sync.Mutex
	queries     uint64
	answers     uint64
	refused     uint64
	forwarded   uint64
	forwardFail uint64
}

// Stats reports what the server has seen, for `status`.
func (s *Server) Stats() (queries, answers, refused, forwarded, forwardFail uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries, s.answers, s.refused, s.forwarded, s.forwardFail
}

// Serve reads queries until the context is cancelled.
//
// UDP only. A mesh answer is one small record, so it never approaches the 512
// byte limit that makes TCP fallback necessary, and a TCP listener is another
// thing to get wrong.
func (s *Server) Serve(ctx context.Context, pc net.PacketConn) error {
	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	buf := make([]byte, 1232) // the EDNS0 payload size that survives most paths
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("dns read: %w", err)
		}
		reply, err := s.answer(buf[:n])
		if err != nil {
			// A malformed query is not worth a log line per packet; anything on
			// this port that is not a query is someone else's problem.
			continue
		}
		if _, err := pc.WriteTo(reply, addr); err != nil && ctx.Err() == nil {
			if s.Log != nil {
				s.Log("dns write failed", "to", addr, "err", err)
			}
		}
	}
}

// Answer builds a response for a raw query.
//
// Exported for the tun intercept, which has a query and needs a response
// without a socket between them.
func (s *Server) Answer(query []byte) ([]byte, error) { return s.answer(query) }

// answer builds a response for a query.
func (s *Server) answer(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.queries++
	s.mu.Unlock()

	reply := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:            header.ID,
		Response:      true,
		Authoritative: true,
		// Not RecursionAvailable: we are authoritative for one suffix and
		// resolve nothing else, and saying otherwise invites clients to ask.
		RCode: dnsmessage.RCodeSuccess,
	})
	reply.EnableCompression()
	if err := reply.StartQuestions(); err != nil {
		return nil, err
	}
	if err := reply.Question(q); err != nil {
		return nil, err
	}

	name := strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")
	host, ok := s.hostWithinSuffix(name)
	if !ok {
		if s.Upstream != nil {
			// Not ours: hand it on unchanged. Failing to reach upstream is
			// SERVFAIL, which tells the client to try another resolver, rather
			// than REFUSED, which many clients treat as final.
			resp, err := s.Upstream(query)
			if err != nil {
				s.mu.Lock()
				s.forwardFail++
				s.mu.Unlock()
				return s.rcode(header, q, dnsmessage.RCodeServerFailure)
			}
			s.mu.Lock()
			s.forwarded++
			s.mu.Unlock()
			return resp, nil
		}
		// Outside our suffix. REFUSED rather than NXDOMAIN: NXDOMAIN asserts
		// the name does not exist anywhere, which we are in no position to say.
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		return s.rcode(header, q, dnsmessage.RCodeRefused)
	}

	addr, found := netip.Addr{}, false
	if s.Lookup != nil {
		addr, found = s.Lookup(host)
	}
	if !found {
		return s.rcode(header, q, dnsmessage.RCodeNameError) // NXDOMAIN
	}

	// The overlay is IPv6-only. An A query for a name that exists must be
	// NOERROR with no records, not NXDOMAIN: NXDOMAIN means the name does not
	// exist at all, and a resolver that believes it will not then try AAAA.
	if q.Type == dnsmessage.TypeA {
		if err := reply.StartAnswers(); err != nil {
			return nil, err
		}
		return reply.Finish()
	}
	if q.Type != dnsmessage.TypeAAAA {
		if err := reply.StartAnswers(); err != nil {
			return nil, err
		}
		return reply.Finish()
	}

	if err := reply.StartAnswers(); err != nil {
		return nil, err
	}
	if err := reply.AAAAResource(
		dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: q.Class, TTL: TTL},
		dnsmessage.AAAAResource{AAAA: addr.As16()},
	); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.answers++
	s.mu.Unlock()
	return reply.Finish()
}

func (s *Server) rcode(header dnsmessage.Header, q dnsmessage.Question, code dnsmessage.RCode) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: header.ID, Response: true, Authoritative: true, RCode: code,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	return b.Finish()
}

// hostWithinSuffix returns the label to look up, for names we serve.
//
// Accepts both "laptop.mesh" and "laptop.home.mesh": the qualified form is what
// survives two meshes holding the same device name (ADR-015), and the short one
// is what people type.
func (s *Server) hostWithinSuffix(name string) (string, bool) {
	suffix := strings.ToLower(strings.Trim(s.Suffix, "."))
	if suffix == "" || name == "" {
		return "", false
	}
	if !strings.HasSuffix(name, "."+suffix) {
		return "", false
	}
	rest := strings.TrimSuffix(name, "."+suffix)
	if rest == "" {
		return "", false
	}
	// "laptop.home" -> "laptop"; the mesh label is not resolved here because a
	// node knows only its own meshes and they are local names anyway.
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		return rest[:i], true
	}
	return rest, true
}

// Listen binds the resolver to an address.
//
// Port 53 needs CAP_NET_BIND_SERVICE on Linux. On Android the app has no such
// capability, and whether the bind succeeds depends on the device's
// ip_unprivileged_port_start — so the error is returned plainly rather than
// swallowed, because a resolver that silently is not listening looks exactly
// like a mesh that cannot resolve names.
func Listen(addr netip.Addr) (net.PacketConn, error) {
	ap := netip.AddrPortFrom(addr, Port)
	pc, err := net.ListenPacket("udp", ap.String())
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", ap, err)
	}
	return pc, nil
}

// Deadline is how long a query may take before it is abandoned. Nothing here
// blocks, so this only bounds a pathological client.
const Deadline = 2 * time.Second
