// Package ustack is a userspace TCP stack holding this device's own overlay
// addresses, for a process that cannot use the kernel's: the Android app,
// which is outside its own tunnel (ADR-083).
//
// It is not a second interface. It sees only the packets it is handed — the
// tunnel's, for the ports it serves and the replies to the ones it dialled
// from — and what it sends goes into WireGuard from the same address the
// kernel would use, because a peer accepts nothing else from this device.
// Its own connections take ports above the kernel's ephemeral range (PortsAbove),
// so a reply meant for one of them is told from another app's by its port
// alone.
package ustack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// PortLow is the first port the stack dials from when the kernel's range is
// Linux's default, 32768-60999.
const PortLow = 61000

// lowest is as high as the band may start and still leave it ports to use.
const lowest = 65000

// PortsAbove is the first port the stack may dial from, given the kernel's
// ip_local_port_range as written in /proc: one above its top, and never below
// PortLow. A phone whose maker widened the range up to the top leaves no room
// that cannot collide; false says so, and the band is then the last 535.
func PortsAbove(procRange string) (uint16, bool) {
	var lo, hi int
	if _, err := fmt.Sscan(procRange, &lo, &hi); err != nil || hi <= 0 {
		return PortLow, true
	}
	switch {
	case hi < PortLow:
		return PortLow, true
	case hi < lowest:
		return uint16(hi + 1), true
	}
	return lowest, false
}

const nic = 1

// Stack is the userspace stack.
type Stack struct {
	s    *stack.Stack
	ep   *channel.Endpoint
	send func([]byte)
	low  uint16 // the first port it dials from

	mu     sync.Mutex
	addrs  []netip.Addr
	closed bool
	done   chan struct{}
}

// New makes a stack that dials from low up and hands each packet it sends to
// send, which owns it.
func New(mtu int, low uint16, send func([]byte)) (*Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	sack := tcpip.TCPSACKEnabled(true)
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return nil, fmt.Errorf("tcp sack: %v", err)
	}
	if err := s.SetPortRange(low, 65535); err != nil {
		return nil, fmt.Errorf("port range: %v", err)
	}
	ep := channel.New(256, uint32(mtu), "")
	if err := s.CreateNIC(nic, ep); err != nil {
		return nil, fmt.Errorf("nic: %v", err)
	}
	s.AddRoute(tcpip.Route{Destination: header.IPv6EmptySubnet, NIC: nic})
	u := &Stack{s: s, ep: ep, send: send, low: low, done: make(chan struct{})}
	go u.pump()
	return u, nil
}

// pump hands out what the stack sends.
func (u *Stack) pump() {
	defer close(u.done)
	for {
		pkt := u.ep.ReadContext(context.Background())
		if pkt == nil {
			return
		}
		v := pkt.ToView()
		pkt.DecRef()
		u.send(v.AsSlice())
	}
}

// Add gives the stack one of this device's addresses.
func (u *Stack) Add(a netip.Addr) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.addrs {
		if x == a {
			return nil
		}
	}
	err := u.s.AddProtocolAddress(nic, tcpip.ProtocolAddress{
		Protocol:          ipv6.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFromSlice(a.AsSlice()).WithPrefix(),
	}, stack.AddressProperties{})
	if err != nil {
		return fmt.Errorf("address %s: %v", a, err)
	}
	u.addrs = append(u.addrs, a)
	return nil
}

// Deliver hands the stack a packet from the tunnel. It is copied: the
// caller's buffer is WireGuard's, and reused.
func (u *Stack) Deliver(pkt []byte) {
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()
	if closed || len(pkt) == 0 || pkt[0]>>4 != 6 {
		return
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), pkt...))})
	u.ep.InjectInbound(header.IPv6ProtocolNumber, pb)
	pb.DecRef()
}

// Listen takes connections to one of the stack's addresses.
func (u *Stack) Listen(a netip.AddrPort) (net.Listener, error) {
	return gonet.ListenTCP(u.s, fullAddr(a), ipv6.ProtocolNumber)
}

// DialContext reaches a peer, from the stack's address in the peer's mesh: a
// peer drops anything from an address it does not know this device by.
func (u *Stack) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return nil, err
	}
	from, ok := u.source(ap.Addr())
	if !ok {
		return nil, errors.New("no address of this device is in " + ap.Addr().String() + "'s mesh")
	}
	local := tcpip.FullAddress{NIC: nic, Addr: tcpip.AddrFromSlice(from.AsSlice())}
	return gonet.DialTCPWithBind(ctx, u.s, local, fullAddr(ap), ipv6.ProtocolNumber)
}

// source is the stack's address sharing the most of to's prefix: each mesh
// has its own /48.
func (u *Stack) source(to netip.Addr) (netip.Addr, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, a := range u.addrs {
		if p, _ := a.Prefix(48); p.Contains(to) && a != to {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// Close stops the stack; its connections fail.
func (u *Stack) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	u.mu.Unlock()
	u.s.Close()
	u.ep.Close()
	<-u.done
}

func fullAddr(a netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: nic, Addr: tcpip.AddrFromSlice(a.Addr().AsSlice()), Port: a.Port()}
}

// Wants is whether a packet from the tunnel is the stack's: TCP to one of
// the ports it serves, or a reply to a connection it made. IPv6 with no
// extension headers only; ForSelf has already said it is for this device.
func (u *Stack) Wants(pkt []byte, serves uint16) bool {
	if len(pkt) < 40+4 || pkt[0]>>4 != 6 || pkt[6] != 6 { // TCP
		return false
	}
	src := uint16(pkt[40])<<8 | uint16(pkt[41])
	dst := uint16(pkt[42])<<8 | uint16(pkt[43])
	return dst == serves || (src == serves && dst >= u.low)
}
