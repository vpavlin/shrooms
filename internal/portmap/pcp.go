package portmap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

const (
	pcpVersion = 2

	pcpOpMap = 1
	// A response sets the R bit, so a MAP response arrives as 0x81 rather than
	// as the opcode alone.
	pcpResponseBit = 0x80

	pcpHeaderLen  = 24
	pcpMapDataLen = 36
	pcpMsgLen     = pcpHeaderLen + pcpMapDataLen

	pcpProtoUDP = 17 // IANA protocol number, the same one an IP header carries

	pcpNonceLen = 12
)

// mapPCP requests a MAP mapping and returns the external address the server
// assigned.
//
// One exchange, unlike NAT-PMP: the MAP response carries the assigned address
// and port together, so there is no window in which we hold half an answer.
func (c *Client) mapPCP(ctx context.Context, gw netip.Addr, localPort, wantExternal uint16, lifetime time.Duration) (Mapping, error) {
	s, err := c.dial(ctx, gw)
	if err != nil {
		return Mapping{}, err
	}
	defer s.Close()

	var nonce [pcpNonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Mapping{}, fmt.Errorf("generating pcp nonce: %w", err)
	}

	req := buildPCPMap(nonce, s.local, localPort, wantExternal, lifetime)
	resp, err := s.roundTrip(ctx, req, c.timeout())
	if err != nil {
		return Mapping{}, err
	}
	external, granted, err := parsePCPMap(resp, nonce, localPort)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{External: external, Lifetime: granted, Proto: "pcp"}, nil
}

// buildPCPMap encodes a MAP request.
//
// The client's own internal address goes in the header, and it must be the
// address the packet will actually be sent from: a PCP server compares the two
// and answers ADDRESS_MISMATCH if they differ, which is how it detects a client
// sitting behind a NAT it does not control. That is why the socket is connected
// before the request is built rather than after — the kernel's choice of source
// address is the only trustworthy answer on a multi-homed machine.
func buildPCPMap(nonce [pcpNonceLen]byte, local netip.Addr, localPort, wantExternal uint16, lifetime time.Duration) []byte {
	req := make([]byte, pcpMsgLen)
	req[0] = pcpVersion
	req[1] = pcpOpMap
	// Bytes 2-3 are reserved.
	binary.BigEndian.PutUint32(req[4:8], lifetimeSeconds(lifetime))
	// PCP addresses are always 16 bytes; IPv4 travels as IPv4-mapped IPv6.
	copy(req[8:24], mapped16(local))

	copy(req[24:36], nonce[:])
	req[36] = pcpProtoUDP
	// Bytes 37-39 are reserved.
	binary.BigEndian.PutUint16(req[40:42], localPort)
	// Suggest the same port externally; the server is free to ignore it.
	binary.BigEndian.PutUint16(req[42:44], wantExternal)
	// A zero suggested external address asks the server to choose, which is
	// what we want: we do not know the router's WAN address, and asking for a
	// specific one is how a request gets refused with CANNOT_PROVIDE_EXTERNAL.
	return req
}

// parsePCPMap reads a MAP response and returns the assigned external address
// and the lifetime the server granted.
//
// wantNonce and wantInternal are what tie the response to this request. The
// nonce is the real defence: PCP servers multicast unsolicited ANNOUNCE
// messages and may re-send a MAP response after a reboot, so a packet arriving
// on our socket is not necessarily the answer to our question, and accepting
// one blindly would install a mapping for someone else's port as if it were
// ours.
func parsePCPMap(pkt []byte, wantNonce [pcpNonceLen]byte, wantInternal uint16) (netip.AddrPort, time.Duration, error) {
	if len(pkt) < pcpMsgLen {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response is %d bytes, want at least %d", len(pkt), pcpMsgLen)
	}
	if pkt[0] != pcpVersion {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response has version %d, want %d", pkt[0], pcpVersion)
	}
	if pkt[1] != pcpResponseBit|pcpOpMap {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response has opcode byte 0x%02x, want 0x%02x", pkt[1], pcpResponseBit|pcpOpMap)
	}
	// The nonce is checked before the result code so that a failure reported
	// for somebody else's request is discarded rather than blamed on ours.
	if [pcpNonceLen]byte(pkt[24:36]) != wantNonce {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response nonce does not match the request")
	}
	if code := pkt[3]; code != 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map refused: result code %d (%s)", code, pcpResultName(code))
	}
	if pkt[36] != pcpProtoUDP {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response is for protocol %d, want %d", pkt[36], pcpProtoUDP)
	}
	if got := binary.BigEndian.Uint16(pkt[40:42]); got != wantInternal {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map response is for internal port %d, want %d", got, wantInternal)
	}

	extPort := binary.BigEndian.Uint16(pkt[42:44])
	if extPort == 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map assigned external port 0 for internal port %d", wantInternal)
	}
	external := netip.AddrFrom16([16]byte(pkt[44:60])).Unmap()
	if !external.IsValid() || external.IsUnspecified() {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map assigned external address %s", external)
	}

	// Bytes 4-8 are the granted lifetime, 8-12 the server's epoch. The epoch is
	// ignored for the same reason as in NAT-PMP: the caller re-maps on a
	// schedule, so detecting a router reboot buys nothing that costs state.
	granted := time.Duration(binary.BigEndian.Uint32(pkt[4:8])) * time.Second
	if granted <= 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp map assigned external port %d with a zero lifetime", extPort)
	}
	return netip.AddrPortFrom(external, extPort), granted, nil
}

// pcpResultName names a result code. The distinction that matters operationally
// is between "the router will never do this" and "try again later"; the text
// says which is which where it is not obvious.
func pcpResultName(code uint8) string {
	switch code {
	case 0:
		return "success"
	case 1:
		return "unsupported version"
	case 2:
		return "not authorized, port mapping is probably disabled on the router"
	case 3:
		return "malformed request"
	case 4:
		return "unsupported opcode"
	case 5:
		return "unsupported option"
	case 6:
		return "malformed option"
	case 7:
		return "network failure, retry later"
	case 8:
		return "no resources, retry later"
	case 9:
		return "unsupported protocol"
	case 10:
		return "user exceeded quota, retry later"
	case 11:
		return "cannot provide external address"
	case 12:
		return "address mismatch, the request did not come from the address it claimed"
	case 13:
		return "excessive remote peers"
	default:
		return "unknown"
	}
}

// mapped16 renders an address as the 16 bytes PCP always uses. As16 already
// produces the ::ffff:0:0/96 form for an IPv4 address, which is exactly what
// RFC 6887 requires, and all zeros for the invalid address, which is the
// "server, you choose" encoding.
func mapped16(a netip.Addr) []byte {
	b := a.As16()
	return b[:]
}
