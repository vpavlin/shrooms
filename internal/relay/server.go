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
// RegisterSkew bounds how stale a registration may be.
//
// A captured register frame is replayable for exactly this long, so it wants to
// be short — and it must comfortably exceed the clock difference between two
// machines that are otherwise fine, or a relay starts refusing honest peers for
// a reason nobody will guess.
const RegisterSkew = 2 * time.Minute

type Server struct {
	key Key

	// owns answers "does this device hold that tunnel key", which is the
	// question a registration has to pass and the one a relay cannot answer by
	// itself. The mesh supplies it from the roster, where the pairing is
	// carried by each device's own signed announce and, on a mesh with an
	// authority, checked against the credential that names both keys.
	//
	// Nil means unchecked, which is what a relay had before this existed. Kept
	// possible rather than impossible because a relay for a mesh with no
	// roster — a test, a standalone forwarder — should still work.
	owns func(devicePub []byte, wg identity.WGKey) bool

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
//
// owns may be nil, in which case registrations are accepted from any member —
// the behaviour before ADR-029, kept for a relay with no roster to ask.
func NewServer(key Key, owns func(devicePub []byte, wg identity.WGKey) bool) *Server {
	return &Server{
		key:    key,
		owns:   owns,
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
		// Recent, and by the device that owns the key.
		//
		// The frame's own signature is checked in Decode, so by here we know
		// the named device asked for this. What is left is whether that device
		// may claim this tunnel key, and whether the claim is fresh.
		if skew := now.Sub(time.Unix(f.At, 0)); skew > RegisterSkew || skew < -RegisterSkew {
			s.mu.Lock()
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
		if s.owns != nil && !s.owns(f.DevicePub, f.Key) {
			// A member registering somebody else's key: the hijack this exists
			// to stop. Counted rather than logged — the server has no logger,
			// and a counter is what makes it visible in status.
			s.mu.Lock()
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
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

// Stat is what a relay can say about itself.
type Stat struct {
	// Peers currently registered, i.e. devices this relay can forward to.
	Peers int
	// Registered, Forwarded and Dropped are cumulative since start.
	Registered, Forwarded, Dropped uint64
	// Refused is a registration this relay would not accept: stale, or from a
	// device the roster does not agree owns that tunnel key (ADR-029).
	//
	// Counted since the ownership check existed and reported nowhere, which
	// made "is the relay refusing my device?" a question with no answer — the
	// exact question asked of a relay that looks up and carries nothing.
	Refused uint64
}

// Stats reports counters for diagnostics.
func (s *Server) Stats() Stat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stat{
		Peers:      len(s.peers),
		Registered: s.registered,
		Forwarded:  s.forwarded,
		Dropped:    s.dropped,
		Refused:    s.refused,
	}
}
