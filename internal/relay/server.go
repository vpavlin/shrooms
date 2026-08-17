package relay

import (
	"net/netip"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
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

// MaxRegistrations bounds the table.
//
// The keys are chosen by whoever registers, so without a cap a single member
// could invent millions of them and the map would grow until the relay died —
// and every entry stayed for a full TTL. A personal mesh has tens of devices; a
// relay carrying several has hundreds. Anything past this is not a mesh.
const MaxRegistrations = 512

// Server is the relay's forwarding table.
//
// Pure soft state: it is rebuilt from registrations within one refresh interval,
// so a restarted relay costs a brief outage and nothing else. There is
// deliberately no persistence.
type Server struct {
	key Key

	mu    sync.RWMutex
	peers map[identity.WGKey]registration

	// byAddr is the reverse of peers, so filling in a forward's source key is a
	// lookup rather than a scan of the whole table on every packet.
	//
	// It also makes an address hold one registration at a time. That does not
	// close the hijack in ADR terms — a member can still register a key that is
	// not theirs, which needs a signature over the key to fix properly — but it
	// does mean one source cannot accumulate entries, which is what turned an
	// unbounded map into a way to exhaust the relay.
	byAddr map[netip.AddrPort]identity.WGKey

	// stats
	registered uint64
	forwarded  uint64
	dropped    uint64
	refused    uint64
}

// NewServer returns an empty relay.
func NewServer(key Key) *Server {
	return &Server{
		key:    key,
		peers:  make(map[identity.WGKey]registration),
		byAddr: make(map[netip.AddrPort]identity.WGKey),
	}
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
		s.registerLocked(f.Key, from, now)
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

// registerLocked records a mapping, keeping both directions consistent.
//
// One address holds one key. A source that registers a second key releases the
// first, so a member cannot accumulate entries from one socket — which is what
// made the table unbounded. Re-registering the same key from a new address
// (a NAT rebinding, or a phone that moved) drops the stale reverse entry, which
// the old code leaked because there was no reverse map to leak from.
func (s *Server) registerLocked(key identity.WGKey, from netip.AddrPort, now time.Time) {
	prev, existed := s.peers[key]
	if !existed && len(s.peers) >= MaxRegistrations {
		// Full, and this is a new key. Refusing the new one rather than
		// evicting an old one: eviction is what lets a flood displace the
		// devices the relay exists to carry.
		s.refused++
		return
	}
	if existed && prev.addr != from {
		delete(s.byAddr, prev.addr)
	}
	// This address was holding some other key; that registration ends here.
	if old, held := s.byAddr[from]; held && old != key {
		delete(s.peers, old)
	}
	if !existed {
		s.registered++
	}
	s.peers[key] = registration{addr: from, seen: now}
	s.byAddr[from] = key
}

// lookupByAddr finds which key registered from an address.
func (s *Server) lookupByAddr(addr netip.AddrPort, now time.Time) (identity.WGKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byAddr[addr]
	if !ok {
		return identity.WGKey{}, false
	}
	if r, live := s.peers[k]; !live || now.Sub(r.seen) > RegistrationTTL {
		return identity.WGKey{}, false
	}
	return k, true
}

func (s *Server) expireLocked(now time.Time) {
	for k, r := range s.peers {
		if now.Sub(r.seen) > RegistrationTTL {
			// Both directions, or byAddr keeps a key that no longer exists and
			// a later forward resolves a source that is gone.
			if held, ok := s.byAddr[r.addr]; ok && held == k {
				delete(s.byAddr, r.addr)
			}
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
