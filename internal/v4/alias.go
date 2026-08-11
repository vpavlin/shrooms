// Package v4 gives every peer a synthetic IPv4 address (ADR-021).
//
// The overlay is IPv6-only, because addresses are derived rather than allocated
// and that needs 128 bits (ADR-005). Browsers do not cope: Chromium stops
// sending AAAA queries when its IPv6 connectivity probe fails, so on a v4-only
// network it asks only for A records, gets a correct empty answer, and gives
// up. Measured on a phone: fifteen A queries, five type-65, zero AAAA.
//
// So each peer gets an IPv4 alias, and packets addressed to it are translated
// to the peer's overlay address before WireGuard sees them.
//
// The alias never leaves this machine. It is not announced, no peer has to
// agree to it, and two devices may pick different aliases for the same peer
// without anything noticing — which is what keeps this from needing the
// coordination the whole project exists to avoid.
package v4

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"sync"
)

// Prefix is the range aliases come from: RFC 2544, reserved for benchmarking.
//
// Chosen for what it avoids. 100.64/10 is carrier-grade NAT — what Tailscale
// uses, and it collides with exactly the mobile networks a phone sits behind.
// RFC 1918 collides with somebody's home LAN, including yours. 240/4 is still
// dropped by several stacks.
var Prefix = netip.MustParsePrefix("198.18.0.0/15")

// Alias derives a peer's synthetic address from its device key.
//
// The nonce breaks a collision: two devices whose hashes land on the same
// address are resolved locally, by whoever noticed, without telling anyone.
func Alias(devicePub ed25519.PublicKey, nonce uint8) netip.Addr {
	h := sha256.New()
	h.Write([]byte("mesh/v1/v4alias"))
	h.Write([]byte{nonce})
	h.Write(devicePub)
	sum := h.Sum(nil)

	// The low 17 bits of the /15, so both /16s are used.
	v := binary.BigEndian.Uint32(sum[:4]) & 0x0001FFFF
	base := Prefix.Addr().As4()
	addr := binary.BigEndian.Uint32(base[:]) | v

	var out [4]byte
	binary.BigEndian.PutUint32(out[:], addr)
	a := netip.AddrFrom4(out)

	// .0 and .255 in any /24 confuse enough software to be worth skipping, and
	// so does the first address of the range.
	last := out[3]
	if last == 0 || last == 255 || !Prefix.Contains(a) {
		return Alias(devicePub, nonce+1)
	}
	return a
}

// A Table maps between overlay addresses and their aliases.
//
// Rebuilt from the roster whenever it changes, which is rare — a peer appearing
// or going away — so the read path is a plain map lookup under an RWMutex
// rather than anything clever.
type Table struct {
	mu    sync.RWMutex
	to4   map[netip.Addr]netip.Addr // overlay -> alias
	to6   map[netip.Addr]netip.Addr // alias -> overlay
	self  netip.Addr                // this device's own alias
	self6 netip.Addr
}

// Entry is one device: its overlay address and the key that derives its alias.
type Entry struct {
	Overlay   netip.Addr
	DevicePub ed25519.PublicKey
}

// NewTable builds a mapping for this device and its peers.
func NewTable(self Entry, peers []Entry) *Table {
	t := &Table{
		to4:   make(map[netip.Addr]netip.Addr, len(peers)+1),
		to6:   make(map[netip.Addr]netip.Addr, len(peers)+1),
		self6: self.Overlay,
	}
	t.add(self)
	t.self = t.to4[self.Overlay]
	for _, p := range peers {
		t.add(p)
	}
	return t
}

// add assigns an alias, stepping the nonce until it is unused. Collisions are
// resolved here and nowhere else: the alias is local, so consistency with any
// other device is neither required nor attempted.
func (t *Table) add(e Entry) {
	if !e.Overlay.IsValid() || len(e.DevicePub) == 0 {
		return
	}
	if _, seen := t.to4[e.Overlay]; seen {
		return
	}
	for nonce := uint8(0); nonce < 64; nonce++ {
		a := Alias(e.DevicePub, nonce)
		if _, taken := t.to6[a]; taken {
			continue
		}
		t.to4[e.Overlay] = a
		t.to6[a] = e.Overlay
		return
	}
}

// Update replaces the peer set, keeping this device's own alias fixed.
//
// Aliases must not move underneath a live connection, so a peer that is already
// mapped keeps the address it has even if it briefly leaves the roster.
func (t *Table) Update(peers []Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range peers {
		t.add(p)
	}
}

// Alias returns a peer's synthetic address.
func (t *Table) Alias(overlay netip.Addr) (netip.Addr, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	a, ok := t.to4[overlay]
	return a, ok
}

// Overlay returns the address an alias stands for.
func (t *Table) Overlay(alias netip.Addr) (netip.Addr, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	a, ok := t.to6[alias]
	return a, ok
}

// Self returns this device's own alias, which translated packets are sent from.
func (t *Table) Self() netip.Addr {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.self
}

// SelfOverlay returns this device's overlay address.
func (t *Table) SelfOverlay() netip.Addr {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.self6
}
