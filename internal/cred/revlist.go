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
// An entry is dropped only when the revocation itself says when it stops
// mattering, and that statement is signed by the admin.
//
// The history is the argument for that rule. Add used to set `until` to
// now+DefaultLife, because a version 1 revocation did not say what it withdrew,
// and Prune dropped entries on it — so a revocation for a credential issued
// with a longer --life would have been forgotten while that credential still
// verified, and the device would have walked back onto the mesh. Prune was
// therefore left uncalled and the list grew forever.
//
// Version 2 carries NotAfter, so `until` is now the admin's own statement
// rather than this package's guess, and pruning is safe because the credential
// being withdrawn is provably dead by then. A revocation without one — every
// version 1 revocation, and anything an admin chose not to bound — is kept
// forever, which is the direction that fails safe.
//
// Growth was never attacker-driven in either case: only the holder of the admin
// key can add an entry here at all.
type List struct {
	mu sync.RWMutex
	// by device key, keeping the highest serial seen. A revocation withdraws
	// its serial and everything below, so the highest is all that matters.
	seen map[string]*entry
}

type entry struct {
	serial uint64
	// until is when this can be forgotten: the latest expiry of any credential
	// it could still be withdrawing, as stated by the admin who signed it. The
	// zero time means no bound was given, and the entry is kept forever.
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
//
// How long to keep it comes from the revocation, not from the caller: it is the
// one part of this that is signed, and a caller that could shorten it could
// make a revocation lapse early.
func (l *List) Add(r *Revocation, raw []byte) bool {
	if l == nil || r == nil {
		return false
	}
	keepUntil := r.Forgettable()
	k := hex.EncodeToString(r.DevicePub)

	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.seen[k]; ok && prev.serial >= r.Serial {
		// Keep the longer of the two, where "no bound" is the longest of all.
		// Two revocations for the same device can disagree, and forgetting on
		// the shorter one would re-admit a device the other still withdraws.
		if !prev.until.IsZero() && (keepUntil.IsZero() || keepUntil.After(prev.until)) {
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

// Prune drops entries whose credentials have provably expired anyway.
//
// Only entries carrying a signed NotAfter are eligible; one without a bound is
// kept forever, because "we do not know what this withdrew" and "this no longer
// withdraws anything" are not the same statement, and only the second one is
// safe to act on.
func (l *List) Prune(now time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, e := range l.seen {
		if e.until.IsZero() {
			continue
		}
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
