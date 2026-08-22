package portmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

const (
	natpmpVersion = 0

	natpmpOpExternal = 0 // "what is your external address"
	natpmpOpMapUDP   = 1 // opcode 2 is the TCP equivalent, which we never want

	// A response echoes the request's opcode with the top bit set.
	natpmpResponseBit = 128

	natpmpExternalRespLen = 12
	natpmpMapRespLen      = 16
)

// mapNATPMP performs the two exchanges NAT-PMP needs to produce a full external
// address: the mapping itself carries only a port, so the address has to be
// asked for separately.
//
// The address is asked for first because it is the cheaper failure: a router
// that does not speak NAT-PMP is discovered before we have created any state on
// it, and there is no window in which a mapping exists that we failed to learn
// the address of and therefore cannot use or tear down.
func (c *Client) mapNATPMP(ctx context.Context, gw netip.Addr, localPort, wantExternal uint16, lifetime time.Duration) (Mapping, error) {
	s, err := c.dial(ctx, gw)
	if err != nil {
		return Mapping{}, err
	}
	defer s.Close()

	resp, err := s.roundTrip(ctx, []byte{natpmpVersion, natpmpOpExternal}, c.timeout())
	if err != nil {
		return Mapping{}, err
	}
	external, err := parseNATPMPExternal(resp)
	if err != nil {
		return Mapping{}, err
	}

	req := make([]byte, 12)
	req[0] = natpmpVersion
	req[1] = natpmpOpMapUDP
	// Bytes 2-3 are reserved and must be zero.
	binary.BigEndian.PutUint16(req[4:6], localPort)
	// The external port to ask for. Normally the same as the local one, since
	// routers usually honour it and a peer that has only ever seen us there
	// needs no new information — but a caller that has discovered its mapping
	// collides with somebody else's asks for a different one.
	binary.BigEndian.PutUint16(req[6:8], wantExternal)
	binary.BigEndian.PutUint32(req[8:12], lifetimeSeconds(lifetime))

	resp, err = s.roundTrip(ctx, req, c.timeout())
	if err != nil {
		return Mapping{}, err
	}
	extPort, granted, err := parseNATPMPMap(resp, localPort)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{
		External: netip.AddrPortFrom(external, extPort),
		Lifetime: granted,
		Proto:    "natpmp",
	}, nil
}

// parseNATPMPExternal reads an opcode 0 response and returns the router's
// external IPv4 address.
func parseNATPMPExternal(pkt []byte) (netip.Addr, error) {
	if len(pkt) < natpmpExternalRespLen {
		return netip.Addr{}, fmt.Errorf("natpmp external address response is %d bytes, want at least %d", len(pkt), natpmpExternalRespLen)
	}
	if pkt[0] != natpmpVersion {
		return netip.Addr{}, fmt.Errorf("natpmp external address response has version %d, want %d", pkt[0], natpmpVersion)
	}
	if pkt[1] != natpmpResponseBit+natpmpOpExternal {
		return netip.Addr{}, fmt.Errorf("natpmp external address response has opcode %d, want %d", pkt[1], natpmpResponseBit+natpmpOpExternal)
	}
	if code := binary.BigEndian.Uint16(pkt[2:4]); code != 0 {
		return netip.Addr{}, fmt.Errorf("natpmp external address refused: result code %d (%s)", code, natpmpResultName(code))
	}
	// Bytes 4-8 are seconds since the router's epoch. It exists so a client can
	// notice a reboot and re-map early; we ignore it because the caller re-maps
	// on a fixed schedule regardless, and acting on it would mean holding state
	// across calls that this client deliberately does not have.
	addr := netip.AddrFrom4([4]byte(pkt[8:12]))
	if !addr.IsValid() || addr.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("natpmp reported external address %s", addr)
	}
	return addr, nil
}

// parseNATPMPMap reads an opcode 1 response and returns the external port the
// router assigned along with the lifetime it granted.
//
// wantInternal is checked against the echoed internal port so that a reply to
// some other request — a delayed one from a previous run, since the mapping
// protocol has no transaction id of any kind — cannot be mistaken for the
// answer to this one.
func parseNATPMPMap(pkt []byte, wantInternal uint16) (uint16, time.Duration, error) {
	if len(pkt) < natpmpMapRespLen {
		return 0, 0, fmt.Errorf("natpmp map response is %d bytes, want at least %d", len(pkt), natpmpMapRespLen)
	}
	if pkt[0] != natpmpVersion {
		return 0, 0, fmt.Errorf("natpmp map response has version %d, want %d", pkt[0], natpmpVersion)
	}
	if pkt[1] != natpmpResponseBit+natpmpOpMapUDP {
		return 0, 0, fmt.Errorf("natpmp map response has opcode %d, want %d", pkt[1], natpmpResponseBit+natpmpOpMapUDP)
	}
	if code := binary.BigEndian.Uint16(pkt[2:4]); code != 0 {
		return 0, 0, fmt.Errorf("natpmp map refused: result code %d (%s)", code, natpmpResultName(code))
	}
	if got := binary.BigEndian.Uint16(pkt[8:10]); got != wantInternal {
		return 0, 0, fmt.Errorf("natpmp map response is for internal port %d, want %d", got, wantInternal)
	}
	extPort := binary.BigEndian.Uint16(pkt[10:12])
	if extPort == 0 {
		return 0, 0, fmt.Errorf("natpmp map assigned external port 0 for internal port %d", wantInternal)
	}
	granted := time.Duration(binary.BigEndian.Uint32(pkt[12:16])) * time.Second
	if granted <= 0 {
		return 0, 0, fmt.Errorf("natpmp map assigned external port %d with a zero lifetime", extPort)
	}
	return extPort, granted, nil
}

// natpmpResultName names a result code so a log line is readable without the
// RFC open next to it. Unknown codes are reported by number alone, since a
// router inventing one is more likely than the RFC having grown.
func natpmpResultName(code uint16) string {
	switch code {
	case 0:
		return "success"
	case 1:
		return "unsupported version"
	case 2:
		return "not authorized, port mapping is probably disabled on the router"
	case 3:
		return "network failure, the router has no external address yet"
	case 4:
		return "out of resources"
	case 5:
		return "unsupported opcode"
	default:
		return "unknown"
	}
}

// lifetimeSeconds converts a duration to the wire's uint32 seconds, clamping
// rather than wrapping. A wrapped lifetime is the worst possible failure here:
// it turns a long-lived request into a short one, or into the zero that means
// "delete the mapping".
func lifetimeSeconds(d time.Duration) uint32 {
	secs := int64(d / time.Second)
	if secs < 1 {
		return 1
	}
	const maxUint32 = int64(^uint32(0))
	if secs > maxUint32 {
		return uint32(maxUint32)
	}
	return uint32(secs)
}
