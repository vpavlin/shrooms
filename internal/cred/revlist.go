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
// Entries are dropped once the credential they withdraw would have expired
// anyway. After that expiry does the same job, and keeping them forever would
// grow without bound on exactly the input an attacker controls — the number of
// distinct device keys they can invent.
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
