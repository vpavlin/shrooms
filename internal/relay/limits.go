package relay

import "time"

// What an operator needs before they can leave a relay open to strangers
// (docs/blind-relays.md).
//
// Every one of these is a ceiling on somebody else's use of a machine the
// operator pays for. None of them is a security boundary: a blind relay's
// safety comes from the registrant's signature and the routability check, and
// these exist purely so that "open" does not mean "unbounded".

// Options configures a relay. The zero value is the mesh-member relay that
// existed before blind relays, with the historical constants as its defaults.
type Options struct {
	// Blind means this relay is not a member of any mesh it carries.
	//
	// It cannot consult a roster, so it substitutes two things: the first
	// device key to claim a tag keeps it, and a registration is only installed
	// once the registrant has proved it receives at the address it gave.
	Blind bool

	// Open means no token is required. Frames authenticate under OpenKey,
	// which everybody has — see the note there about why that is honest rather
	// than broken.
	Open bool

	// MaxRegistrations bounds the whole table. Zero means MaxRegistrations.
	MaxRegistrations int

	// MaxPerSource bounds how many registrations one source address may hold.
	//
	// The table cap alone is not enough once a relay is open: one host with a
	// range of ports can fill it, and every entry it takes is a device the
	// relay exists to carry and now cannot. Counted per IP rather than per
	// address, since ports are free.
	//
	// **Set this generously, and understand what it counts.** A source address
	// is not a user. Every device behind one home router shares one, and every
	// phone behind carrier-grade NAT shares one with thousands of strangers —
	// and a phone on mobile data is the case a relay exists for, because it is
	// the one that cannot be dialled directly at all. A cap low enough to be a
	// meaningful defence against one abusive host is also low enough to lock
	// out a carrier.
	//
	// Found the obvious way: a loop of probes from a single laptop exhausted a
	// cap of 8 after four runs and then looked exactly like an unreachable
	// relay for two minutes.
	//
	// So this is a backstop against one host taking the whole table, not a
	// fair-share mechanism. The real protection against a relay being abused is
	// the bandwidth ceiling, which is measured in the thing that actually costs
	// the operator money.
	//
	// Zero means unlimited, which is right for a mesh-member relay where every
	// registrant is already known.
	MaxPerSource int

	// BytesPerSecond caps everything this relay forwards. Zero means no cap.
	BytesPerSecond int64

	// PerPeerBytesPerSecond caps one registration. Zero means no cap.
	//
	// Worth having alongside the total: without it the first heavy user takes
	// the whole allowance and everybody else sees a relay that appears broken.
	PerPeerBytesPerSecond int64

	// BurstSeconds is how long a bucket may save up for. Zero means
	// defaultBurstSeconds.
	//
	// A tunnel is bursty — a file transfer starts, a video call rings — and a
	// bucket with no burst turns every one of those into loss at the exact
	// moment a user is watching. Loss on a WireGuard tunnel presents as a slow
	// network, so the kindest ceiling is one that absorbs a few seconds.
	BurstSeconds float64
}

const defaultBurstSeconds = 3

// controlShare is the fraction of the total ceiling that answers to registers
// and probes may use.
//
// They need a ceiling of their own, and did not have one: only forwarding went
// through the buckets, so a relay could be made to emit challenges and echoes
// without limit. Neither amplifies — a challenge is 33 bytes against a
// 153-byte register, an echo 27 against a probe of up to 65535 — so this is not
// about being used as a weapon against somebody else. It is about an operator's
// uplink, which is the thing they actually pay for.
//
// A twentieth, because control traffic is tiny beside relayed traffic and a
// relay whose control answers are crowding out the packets it exists to carry
// is misconfigured rather than busy.
const controlShare = 20

func (o Options) maxRegistrations() int {
	if o.MaxRegistrations > 0 {
		return o.MaxRegistrations
	}
	return MaxRegistrations
}

func (o Options) burst() float64 {
	if o.BurstSeconds > 0 {
		return o.BurstSeconds
	}
	return defaultBurstSeconds
}

// bucket is a token bucket in bytes.
//
// Refilled on read rather than on a timer, so an idle registration costs
// nothing and there is no goroutine per peer — which matters at the table size
// a relay is allowed to reach.
type bucket struct {
	rate   float64 // bytes per second
	cap    float64
	tokens float64
	last   time.Time
}

func newBucket(rate float64, burstSeconds float64) *bucket {
	if rate <= 0 {
		return nil // no limit: the nil bucket allows everything
	}
	c := rate * burstSeconds
	return &bucket{rate: rate, cap: c, tokens: c}
}

// allow reports whether n bytes may pass, and spends them if so.
//
// All-or-nothing per packet. Partial spending would let a stream of oversized
// packets drain a bucket that never refuses any of them.
func (b *bucket) allow(n int, now time.Time) bool {
	if b == nil {
		return true
	}
	if b.last.IsZero() {
		b.last = now
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.cap {
			b.tokens = b.cap
		}
		b.last = now
	}
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}
