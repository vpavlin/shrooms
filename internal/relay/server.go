package relay

import (
	"net/netip"
	"sync"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// RegistrationTTL is how long a mapping survives without a refresh. Clients
// re-register well inside this; the point of expiring at all is that a NAT
// rebinding silently invalidates the address and a stale entry would black-hole
// traffic rather than fail over.
const RegistrationTTL = 2 * time.Minute

type registration struct {
	addr netip.AddrPort
	seen time.Time
}

// Server is the relay's forwarding table.
//
// Pure soft state: it is rebuilt from registrations within one refresh interval,
// so a restarted relay costs a brief outage and nothing else. There is
// deliberately no persistence.
type Server struct {
	key Key

	mu    sync.RWMutex
	peers map[identity.WGKey]registration

	// stats
	registered uint64
	forwarded  uint64
	dropped    uint64
}

// NewServer returns an empty relay.
func NewServer(key Key) *Server {
	return &Server{key: key, peers: make(map[identity.WGKey]registration)}
}

// Handle processes one inbound frame and returns what to send, if anything.
//
// `from` is the address the frame arrived from, and it is what gets recorded —
// never an address the client claims. A client behind NAT does not know its own
// external address, and one that lied could redirect another peer's traffic.
func (s *Server) Handle(pkt []byte, from netip.AddrPort, now time.Time) (out []byte, to netip.AddrPort, ok bool) {
	f, err := Decode(s.key, pkt)
	if err != nil {
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return nil, netip.AddrPort{}, false
	}

	switch f.Type {
	case TypeRegister:
		s.mu.Lock()
		if _, existed := s.peers[f.Key]; !existed {
			s.registered++
		}
		s.peers[f.Key] = registration{addr: from, seen: now}
		s.expireLocked(now)
		s.mu.Unlock()
		return nil, netip.AddrPort{}, false

	case TypeForward:
		s.mu.RLock()
		dst, known := s.peers[f.Key]
		s.mu.RUnlock()

		if !known || now.Sub(dst.seen) > RegistrationTTL {
			// The destination is not registered, or its mapping went stale.
			// Dropping is correct: there is nowhere to send it, and the sender
			// will fall back or retry.
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}

		// Rewrite with the source key filled in. The sender's claimed source is
		// ignored; we use the key registered to the address it came from, so a
		// client cannot forge who a packet appears to be from.
		src, srcKnown := s.lookupByAddr(from, now)
		if !srcKnown {
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}

		frame, err := EncodeForward(s.key, f.Key, src, f.Payload)
		if err != nil {
			return nil, netip.AddrPort{}, false
		}
		s.mu.Lock()
		s.forwarded++
		s.mu.Unlock()
		return frame, dst.addr, true
	}
	return nil, netip.AddrPort{}, false
}

// lookupByAddr finds which key registered from an address.
//
// Linear over the peer set, which is fine for a personal mesh; a relay serving
// many meshes would want a reverse index.
func (s *Server) lookupByAddr(addr netip.AddrPort, now time.Time) (identity.WGKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, r := range s.peers {
		if r.addr == addr && now.Sub(r.seen) <= RegistrationTTL {
			return k, true
		}
	}
	return identity.WGKey{}, false
}

func (s *Server) expireLocked(now time.Time) {
	for k, r := range s.peers {
		if now.Sub(r.seen) > RegistrationTTL {
			delete(s.peers, k)
		}
	}
}

// Stats reports counters for diagnostics.
func (s *Server) Stats() (peers int, registered, forwarded, dropped uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers), s.registered, s.forwarded, s.dropped
}
