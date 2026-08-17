package cred

import (
	"encoding/hex"
	"sync"
	"time"
)

// List is the set of revocations a node knows about.
//
// Every node keeps its own and checks it itself, which is what makes revocation
// mean something on a bus with no authority: a compromised node cannot
// un-revoke a device by staying quiet, because its peers already hold the
// statement and verified the admin signature themselves.
//
// Entries are NOT dropped, despite what this comment used to say and what Prune
// below implements. Nothing calls Prune, so a revocation is kept for as long as
// the process — and now the state dir — holds it.
//
// That is the safe direction, and it is worth being explicit about why the tidy
// version is not obviously right. Prune drops an entry once `until` passes, and
// Add sets `until` to now+DefaultLife because a revocation does not say what it
// withdraws. Wire it up as written and a revocation for a credential issued
// with a longer --life would be forgotten while that credential still verifies,
// and the device would walk back onto the mesh. Growth, meanwhile, is bounded
// by what an admin has signed: an attacker cannot add entries here at all, only
// the holder of the admin key can.
//
// Fixing it properly means the revocation carrying the withdrawn credential's
// NotAfter, which changes a signed wire format — see
// docs/audit-open-questions.md.
type List struct {
	mu sync.RWMutex
	// by device key, keeping the highest serial seen. A revocation withdraws
	// its serial and everything below, so the highest is all that matters.
	seen map[string]*entry
}

type entry struct {
	serial uint64
	// until is when this can be forgotten: the latest expiry of any credential
	// it could still be withdrawing.
	until time.Time
	raw   []byte
}

func NewList() *List { return &List{seen: make(map[string]*entry)} }

// A nil list means "nothing has been revoked", so every method tolerates one.
// A mesh with no authority never builds a list, and a caller should not have to
// know that to ask the question.

// Add records a revocation that has already been verified against the mesh's
// authority. Returns true when it told us something new, which is what decides
// whether to pass it on.
func (l *List) Add(r *Revocation, raw []byte, keepUntil time.Time) bool {
	if l == nil || r == nil {
		return false
	}
	k := hex.EncodeToString(r.DevicePub)

	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.seen[k]; ok && prev.serial >= r.Serial {
		if keepUntil.After(prev.until) {
			prev.until = keepUntil
		}
		return false
	}
	l.seen[k] = &entry{serial: r.Serial, until: keepUntil, raw: append([]byte(nil), raw...)}
	return true
}

// Revoked reports whether a credential has been withdrawn.
func (l *List) Revoked(c *Credential) bool {
	if l == nil || c == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.seen[hex.EncodeToString(c.DevicePub)]
	return ok && e.serial >= c.Serial
}

// All returns the revocations worth repeating, so a node that has just joined
// can be told what it missed.
func (l *List) All() [][]byte {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([][]byte, 0, len(l.seen))
	for _, e := range l.seen {
		out = append(out, e.raw)
	}
	return out
}

// Prune drops entries whose credentials would have expired anyway.
//
// Deliberately not called. See the note on List: `until` is a guess
// (now+DefaultLife), not the withdrawn credential's real expiry, so pruning on
// it can forget a revocation that still matters. Kept rather than deleted
// because the tests below pin the behaviour, and because deleting it would
// leave the next person to re-derive the same trap from scratch.
func (l *List) Prune(now time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, e := range l.seen {
		if now.After(e.until) {
			delete(l.seen, k)
			n++
		}
	}
	return n
}

// Len reports how many devices are currently withdrawn.
func (l *List) Len() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.seen)
}
