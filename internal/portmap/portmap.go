// Package portmap asks the local router to open an inbound UDP port, so that a
// node behind a home NAT can be dialled directly instead of paying for a relay.
//
// This is the one hole-punching technique that involves no third party at all.
// Reflexive discovery (see internal/disco) learns our public address only
// because peers tell us what they saw, and hole punching only works if the NAT
// cooperates. Asking the router directly is strictly better when it answers:
// the mapping is explicit, it is stable for the lifetime the router promises,
// and it works even under endpoint-dependent (symmetric) NAT, which is the case
// punching cannot solve. Consumer routers that answer are common — UPnP-IGD is
// the third dialect and is deliberately not implemented here, because it is
// SOAP over HTTP over SSDP and would dwarf this file.
//
// Two protocols, one port. PCP (RFC 6887) is the successor to NAT-PMP
// (RFC 6886) and both live on UDP 5351 on the gateway. PCP is tried first
// because it is what a modern router speaks and because its response carries
// the external address and port together; NAT-PMP needs two exchanges to learn
// the same thing. A PCP server is required to also answer NAT-PMP, but the
// converse is false, so the fallback direction is the useful one.
//
// Nothing here retransmits. Both RFCs specify exponential retransmission
// starting at 250ms, which exists for the case where portmap is the only thing
// keeping a mapping alive. Here it is not: the caller re-maps on a schedule
// well inside the mapping lifetime anyway, so a lost packet costs one cycle
// rather than the connection, and a router that does not answer at all is the
// overwhelmingly common case that retransmission only makes slower to detect.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// port is where both protocols listen on the gateway.
const port = 5351

// DefaultTimeout bounds a single request/response exchange.
//
// A router that speaks neither protocol usually says nothing rather than
// refusing, so this timeout is what a total failure costs, twice over (PCP then
// NAT-PMP). Kept short for that reason: on the LAN a router that does answer
// answers in single-digit milliseconds, so waiting longer only ever delays the
// negative result.
const DefaultTimeout = 2 * time.Second

// DefaultLifetime is requested when the caller asks for no particular lifetime.
// RFC 6887 recommends this value for a mapping the client intends to renew.
const DefaultLifetime = 2 * time.Hour

// Mapping is one external address obtained from the router.
type Mapping struct {
	External netip.AddrPort // the public address other nodes can dial
	Lifetime time.Duration  // how long the router promises to keep it
	Proto    string         // "pcp" or "natpmp", for logging
}

// Client asks the local router for a mapping.
//
// It holds no state between calls: a mapping is soft state on the router, and
// the only way to keep it alive is to ask again, which is the caller's job.
type Client struct {
	// Gateway is the router to ask. Zero value means "discover it".
	Gateway netip.Addr
	// Timeout bounds one attempt. Zero means a sensible default.
	Timeout time.Duration
}

// Map requests an inbound UDP mapping for localPort, returning the external
// address. PCP is tried first, then NAT-PMP.
//
// The returned external address is whatever the router says it assigned, which
// is not necessarily globally routable: behind carrier-grade NAT the home
// router happily maps a port on its own RFC 1918 WAN address, and the result
// looks like a success. Callers should treat the address as a candidate to be
// confirmed by a peer actually reaching it, not as proof of reachability.
func (c *Client) Map(ctx context.Context, localPort uint16, lifetime time.Duration) (Mapping, error) {
	return c.MapTo(ctx, localPort, localPort, lifetime)
}

// MapTo is Map with a specific external port to ask for.
//
// Needed because a router can hand out the same external port twice. Observed
// on 2026-08-22: two machines behind one NAT, one asked to map 51820 and was
// given 51821, which the other already held from its own mapping. Both then
// believed they were reachable there and only one was.
//
// Suggesting a different port is the way out, and it is what the protocol
// field is for. The router remains free to ignore the suggestion — the mapping
// it returns is the answer, and a caller must read it rather than assume it got
// what it asked for.
func (c *Client) MapTo(ctx context.Context, localPort, wantExternal uint16, lifetime time.Duration) (Mapping, error) {
	if localPort == 0 {
		return Mapping{}, errors.New("portmap: cannot map port 0")
	}
	if wantExternal == 0 {
		wantExternal = localPort
	}
	// A zero lifetime means "delete this mapping" in both protocols, so it must
	// never be passed through by accident — a caller that forgot to set one
	// would silently tear down the mapping it meant to create.
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}

	gw := c.Gateway
	if !gw.IsValid() {
		var err error
		gw, err = DiscoverGateway()
		if err != nil {
			return Mapping{}, fmt.Errorf("portmap: no gateway to ask: %w", err)
		}
	}
	// Both protocols are IPv4-only by construction: the whole point is to get
	// through a NAT, and a v6 gateway address here means the caller passed
	// something we cannot ask.
	gw = gw.Unmap()
	if !gw.Is4() {
		return Mapping{}, fmt.Errorf("portmap: gateway %s is not IPv4", gw)
	}

	m, pcpErr := c.mapPCP(ctx, gw, localPort, wantExternal, lifetime)
	if pcpErr == nil {
		return m, nil
	}
	// Do not fall through to NAT-PMP once the context is done; the failure was
	// the deadline, not the protocol, and a second attempt would only return
	// the same error more slowly.
	if ctx.Err() != nil {
		return Mapping{}, fmt.Errorf("portmap: %w", pcpErr)
	}

	m, npErr := c.mapNATPMP(ctx, gw, localPort, wantExternal, lifetime)
	if npErr == nil {
		return m, nil
	}
	return Mapping{}, fmt.Errorf("portmap: gateway %s answered neither protocol: pcp: %w; natpmp: %v", gw, pcpErr, npErr)
}

// timeout is the effective per-exchange bound.
func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

// session is a connected UDP socket to the gateway's portmap port.
//
// Connected rather than merely bound, for two reasons. The kernel picks the
// source address for us, which is exactly the internal address PCP requires in
// its request body and which we would otherwise have to guess from the
// interface list. And on Linux a connected UDP socket surfaces the ICMP port
// unreachable that a router without a portmap daemon sends back, turning the
// common "not supported" case from a full timeout into an immediate error.
type session struct {
	conn  *net.UDPConn
	peer  netip.AddrPort
	local netip.Addr
}

// dial opens a session to the gateway.
func (c *Client) dial(ctx context.Context, gw netip.Addr) (*session, error) {
	peer := netip.AddrPortFrom(gw, port)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", peer.String())
	if err != nil {
		return nil, fmt.Errorf("dialing gateway %s: %w", peer, err)
	}
	uc, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("dialing gateway %s: not a UDP socket", peer)
	}
	local, ok := netip.AddrFromSlice(uc.LocalAddr().(*net.UDPAddr).IP)
	if !ok {
		uc.Close()
		return nil, fmt.Errorf("dialing gateway %s: unusable local address %s", peer, uc.LocalAddr())
	}
	return &session{conn: uc, peer: peer, local: local.Unmap()}, nil
}

func (s *session) Close() error { return s.conn.Close() }

// roundTrip sends one request and returns the first reply that came from the
// gateway itself.
//
// Replies from anywhere else are dropped rather than parsed. The socket is
// connected so the kernel should already filter them, but the check is cheap
// and this is a packet from the local network arriving on an ephemeral port —
// exactly the shape of thing that should not be trusted because it turned up.
func (s *session) roundTrip(ctx context.Context, req []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// A deadline alone does not honour cancellation, only expiry. Unblocking
	// the read by expiring the deadline immediately is the only way to make a
	// blocked socket read observe ctx.
	stop := context.AfterFunc(ctx, func() { s.conn.SetReadDeadline(time.Now()) })
	defer stop()

	if _, err := s.conn.Write(req); err != nil {
		return nil, fmt.Errorf("sending to %s: %w", s.peer, err)
	}

	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("awaiting reply from %s: %w", s.peer, err)
		}
		if from.Addr().Unmap() != s.peer.Addr() || from.Port() != s.peer.Port() {
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out, nil
	}
}
