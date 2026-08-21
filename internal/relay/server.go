package relay

import (
	"crypto/subtle"
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

	// devicePub is who claimed this handle first, on a blind relay.
	//
	// The roster answers "may this device use that key" on a relay that has
	// one. A blind relay has none, so it answers a weaker question instead —
	// "is this the same device as last time" — and that is what this field is
	// for. See ownerLocked.
	devicePub []byte

	// out limits what this registration may be sent, when the operator has set
	// a per-peer ceiling.
	out *bucket
}

// pending is a registration waiting for its routability challenge to come back.
//
// Deliberately tiny: a nonce, an expiry, and what it would install. An attacker
// who floods registrations makes the relay hold a few dozen bytes per address
// for a few seconds and nothing else.
type pending struct {
	nonce     [NonceLen]byte
	key       identity.WGKey
	devicePub []byte
	until     time.Time
}

// ChallengeTTL is how long a routability challenge stays answerable.
//
// One round trip to the registrant. Generous enough for a phone on mobile data
// waking a radio, short enough that pending state cannot accumulate.
const ChallengeTTL = 10 * time.Second

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
	key  Key
	opts Options

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

	// waiting holds routability challenges, keyed by the address the challenge
	// was sent to — which is the address that has to answer.
	waiting map[netip.AddrPort]pending

	// perSource counts registrations per source IP, so one host cannot take
	// the table on a relay open to strangers.
	perSource map[netip.Addr]int

	// total limits everything forwarded, when the operator has set a ceiling.
	total *bucket

	// stats
	registered uint64
	forwarded  uint64
	dropped    uint64
	refused    uint64
	throttled  uint64
	challenged uint64
}

// NewServer returns an empty relay.
//
// owns may be nil, in which case registrations are accepted from any member —
// the behaviour before ADR-029, kept for a relay with no roster to ask.
func NewServer(key Key, owns func(devicePub []byte, wg identity.WGKey) bool) *Server {
	return NewServerWith(key, owns, Options{})
}

// NewServerWith is NewServer with the operator's limits and, for a relay that
// is not a member of the meshes it carries, blind mode (docs/blind-relays.md).
func NewServerWith(key Key, owns func(devicePub []byte, wg identity.WGKey) bool, opts Options) *Server {
	return &Server{
		key:       key,
		opts:      opts,
		owns:      owns,
		peers:     make(map[identity.WGKey]registration),
		byAddr:    make(map[netip.AddrPort]identity.WGKey),
		waiting:   make(map[netip.AddrPort]pending),
		perSource: make(map[netip.Addr]int),
		total:     newBucket(float64(opts.BytesPerSecond), opts.burst()),
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
		// First claim wins, and never moves while the entry lives. On a relay
		// with a roster this is redundant; on a blind one it is the only thing
		// standing between a device and somebody taking its handle, so it is
		// checked whenever the relay has an owner recorded.
		if !s.ownerLocked(f.Key, f.DevicePub) {
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}

		// An unchanged mapping needs no challenge. Registrations refresh every
		// few tens of seconds and a round trip each time would triple the cost
		// of holding one open — the question a challenge answers is whether
		// this address receives, and an address that answered before and is
		// registering the same handle has already answered it.
		if !s.opts.Blind || s.confirmedLocked(f.Key, from) {
			s.registerLocked(f.Key, from, now, f.DevicePub)
			s.expireLocked(now)
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}

		// New binding on a blind relay: prove you receive where you claim
		// before anything is installed. An attacker registering somebody
		// else's address never sees the nonce, which is what stops an open
		// relay being pointed at a third party.
		if s.perSource[from.Addr()] >= s.opts.MaxPerSource && s.opts.MaxPerSource > 0 {
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
		nonce, err := NewNonce()
		if err != nil {
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
		s.expirePendingLocked(now)
		s.waiting[from] = pending{
			nonce:     nonce,
			key:       f.Key,
			devicePub: append([]byte(nil), f.DevicePub...),
			until:     now.Add(ChallengeTTL),
		}
		s.challenged++
		s.mu.Unlock()
		return EncodeChallenge(s.key, nonce), from, true

	case TypeConfirm:
		if skew := now.Sub(time.Unix(f.At, 0)); skew > RegisterSkew || skew < -RegisterSkew {
			s.mu.Lock()
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
		s.mu.Lock()
		p, ok := s.waiting[from]
		// Everything has to match, and the nonce comparison is the whole
		// point: it proves this address received what was sent to it.
		//
		// The handle and the device key are re-checked from the frame rather
		// than taken from the pending entry, so a confirm cannot install
		// something the register did not ask for.
		if !ok || now.After(p.until) ||
			subtle.ConstantTimeCompare(p.nonce[:], f.Nonce[:]) != 1 ||
			p.key != f.Key ||
			subtle.ConstantTimeCompare(p.devicePub, f.DevicePub) != 1 {
			s.refused++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
		delete(s.waiting, from)
		s.registerLocked(f.Key, from, now, f.DevicePub)
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
		// The operator's ceilings, checked together so a packet that the total
		// allows but the per-peer bucket refuses does not spend the total.
		//
		// A refusal here presents to the user as packet loss, which is roughly
		// the right behaviour — TCP inside the tunnel backs off without needing
		// to be told anything — and is counted separately from a drop so that a
		// throttled relay does not look like a broken one.
		if !s.allowLocked(len(frame), dst, now) {
			s.throttled++
			s.mu.Unlock()
			return nil, netip.AddrPort{}, false
		}
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
func (s *Server) registerLocked(key identity.WGKey, from netip.AddrPort, now time.Time, devicePub []byte) {
	prev, existed := s.peers[key]
	if !existed && len(s.peers) >= s.opts.maxRegistrations() {
		// Full, and this is a new key. Refusing the new one rather than
		// evicting an old one: eviction is what lets a flood displace the
		// devices the relay exists to carry.
		s.refused++
		return
	}
	if existed && prev.addr != from {
		delete(s.byAddr, prev.addr)
		s.releaseSourceLocked(prev.addr)
	}
	// This address was holding some other key; that registration ends here.
	if old, held := s.byAddr[from]; held && old != key {
		if r, ok := s.peers[old]; ok {
			s.releaseSourceLocked(r.addr)
		}
		delete(s.peers, old)
	}
	if !existed {
		s.registered++
	}
	// Carried across a refresh rather than rebuilt, or a device that
	// re-registers every thirty seconds gets a fresh full bucket each time and
	// the ceiling means nothing.
	out := newBucket(float64(s.opts.PerPeerBytesPerSecond), s.opts.burst())
	if existed && prev.out != nil {
		out = prev.out
	}
	owner := devicePub
	if existed && len(prev.devicePub) > 0 {
		owner = prev.devicePub // first claim, kept
	}
	if !existed || prev.addr != from {
		s.perSource[from.Addr()]++
	}
	s.peers[key] = registration{addr: from, seen: now, devicePub: owner, out: out}
	s.byAddr[from] = key
}

// ownerLocked reports whether this device may claim this handle.
//
// First claim wins and never moves while the entry lives — the rule SSH uses
// when it shows a fingerprint once and shouts if it ever changes. It is weaker
// than a roster: an attacker who registers a handle before its real owner ever
// does keeps it. It is stronger than nothing: once a device has registered,
// nobody can take the entry from it, because nobody can forge its signature.
//
// On a blind relay the attacker who matters is a stranger, and a stranger
// cannot compute the tag in the first place.
func (s *Server) ownerLocked(key identity.WGKey, devicePub []byte) bool {
	r, ok := s.peers[key]
	if !ok || len(r.devicePub) == 0 {
		return true
	}
	return subtle.ConstantTimeCompare(r.devicePub, devicePub) == 1
}

// confirmedLocked reports whether this address has already proved it receives
// for this handle, which is true exactly when the mapping is unchanged.
func (s *Server) confirmedLocked(key identity.WGKey, from netip.AddrPort) bool {
	r, ok := s.peers[key]
	return ok && r.addr == from
}

// allowLocked spends the operator's budget for one forwarded frame.
func (s *Server) allowLocked(n int, dst registration, now time.Time) bool {
	// The per-peer bucket first: it is the narrower one, and checking it first
	// means a packet it refuses has not already spent from the total.
	if !dst.out.allow(n, now) {
		return false
	}
	return s.total.allow(n, now)
}

func (s *Server) releaseSourceLocked(addr netip.AddrPort) {
	ip := addr.Addr()
	if n := s.perSource[ip]; n > 1 {
		s.perSource[ip] = n - 1
	} else {
		delete(s.perSource, ip)
	}
}

func (s *Server) expirePendingLocked(now time.Time) {
	for a, p := range s.waiting {
		if now.After(p.until) {
			delete(s.waiting, a)
		}
	}
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
			s.releaseSourceLocked(r.addr)
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

	// Throttled is a packet the operator's own ceiling refused.
	//
	// Separate from Dropped because they mean opposite things to whoever is
	// looking: a drop is a destination that is not there, and a throttle is
	// this relay working exactly as configured. Reporting the second as the
	// first makes a relay at its limit look like a broken one.
	Throttled uint64

	// Challenged is a routability check issued but not yet answered, counted
	// since start. On a blind relay a rising count with a flat Registered is
	// what "something is registering that cannot receive" looks like.
	Challenged uint64
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
		Throttled:  s.throttled,
		Challenged: s.challenged,
	}
}
