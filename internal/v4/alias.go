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
	"sort"
	"sync"
	"time"
)

// Prefix is the whole range aliases come from: RFC 2544, reserved for
// benchmarking. Each mesh gets a slice of it — see Block.
//
// Chosen for what it avoids. 100.64/10 is carrier-grade NAT — what Tailscale
// uses, and it collides with exactly the mobile networks a phone sits behind.
// RFC 1918 collides with somebody's home LAN, including yours. 240/4 is still
// dropped by several stacks.
var Prefix = netip.MustParsePrefix("198.18.0.0/15")

// MeshBits is how much of the range identifies the mesh, and DeviceBits what
// is left for devices within it.
//
// Every mesh needs its OWN sub-range, because the range is routed at an
// interface and there is one interface per mesh. Sharing it meant the kernel
// installed a single route, so packets for a peer on the second mesh entered
// the first mesh's translator, which had never heard of it and dropped them —
// visible as a browser that cannot reach a service while curl over IPv6 can.
//
// 4 bits is 16 meshes, and 13 bits is 8192 addresses each: far more of both
// than a personal mesh needs.
const (
	MeshBits   = 4
	DeviceBits = 13
)

// Block is the slice of the range belonging to one mesh, as a prefix to route.
//
// Derived from the network id, so two nodes on the same mesh agree — not that
// they have to, since aliases never leave the machine, but a mesh whose
// addresses look the same everywhere is easier to reason about.
// Only correct for a device with one mesh. With more than one, use Blocks,
// which resolves the collisions this cannot see.
func Block(networkID string) netip.Prefix { return blockOf(blockTag(networkID)) }

// Blocks assigns a block to each of this device's meshes, avoiding collisions
// between them.
//
// Block hashes into 4 bits, so two meshes land on the same one about once in
// sixteen — and when they do, both route the same /19, the kernel installs one
// route, and packets for the second mesh enter the first mesh's translator,
// which has never heard of them. That is the failure the comment on MeshBits
// describes, and deriving the block from the network id alone does not prevent
// it; it only makes it rare enough to look like something else.
//
// Resolved the way an address collision already is: locally, by whoever
// noticed. A block is a routing decision on one machine and never leaves it,
// so a device is free to move a mesh to the next free block without telling
// anybody. Assignment walks the meshes in a fixed order and probes forward
// from each one's preferred block, so the same set of meshes always produces
// the same answer.
func Blocks(networkIDs []string) map[string]netip.Prefix {
	ids := append([]string(nil), networkIDs...)
	sort.Strings(ids)

	taken := make(map[uint32]bool, len(ids))
	out := make(map[string]netip.Prefix, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue // the same mesh named twice
		}
		want := blockTag(id)
		tag := want
		for n := 0; taken[tag] && n < 1<<MeshBits; n++ {
			tag = (tag + 1) % (1 << MeshBits)
		}
		taken[tag] = true
		out[id] = blockOf(tag)
	}
	return out
}

// blockTag is the block a network id prefers.
func blockTag(networkID string) uint32 {
	sum := sha256.Sum256([]byte("mesh/v1/v4block" + networkID))
	return uint32(sum[0]) & ((1 << MeshBits) - 1)
}

// blockOf turns a block number into the prefix to route.
func blockOf(tag uint32) netip.Prefix {
	base := Prefix.Addr().As4()
	addr := binary.BigEndian.Uint32(base[:]) | (tag << DeviceBits)
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], addr)
	return netip.PrefixFrom(netip.AddrFrom4(out), 32-DeviceBits)
}

// Alias derives a peer's synthetic address within one mesh's block.
//
// The nonce breaks a collision: two devices whose hashes land on the same
// address are resolved locally, by whoever noticed, without telling anyone.
func Alias(block netip.Prefix, devicePub ed25519.PublicKey, nonce uint8) netip.Addr {
	// Bounded, and iterative. This used to recurse on nonce+1 with nothing to
	// stop it: nonce is a uint8, so on a block too small to hold any usable
	// address it wrapped and recursed until the stack ran out. 256 is the whole
	// space of the argument, so trying each once is exhaustive rather than
	// arbitrary. Returns an invalid address when the block has nothing to give,
	// which the caller must check.
	for i := 0; i < 256; i++ {
		h := sha256.New()
		h.Write([]byte("mesh/v1/v4alias"))
		h.Write([]byte{nonce})
		h.Write(devicePub)
		sum := h.Sum(nil)

		v := binary.BigEndian.Uint32(sum[:4]) & ((1 << DeviceBits) - 1)
		base := block.Addr().As4()
		addr := binary.BigEndian.Uint32(base[:]) | v

		var out [4]byte
		binary.BigEndian.PutUint32(out[:], addr)
		a := netip.AddrFrom4(out)

		// .0 and .255 in any /24 confuse enough software to be worth skipping,
		// and so does the first address of the range.
		if last := out[3]; last != 0 && last != 255 && block.Contains(a) {
			return a
		}
		nonce++
	}
	return netip.Addr{}
}

// A Table maps between overlay addresses and their aliases.
//
// Rebuilt from the roster whenever it changes, which is rare — a peer appearing
// or going away — so the read path is a plain map lookup under an RWMutex
// rather than anything clever.
type Table struct {
	block netip.Prefix
	mu    sync.RWMutex
	to4   map[netip.Addr]netip.Addr // overlay -> alias
	to6   map[netip.Addr]netip.Addr // alias -> overlay
	self  netip.Addr                // this device's own alias
	self6 netip.Addr
	// When each peer was last in the roster, so an alias can be reclaimed a
	// long while after the peer is gone without moving under a live one.
	seen map[netip.Addr]time.Time
	// Peers the block had no room for. Counted rather than logged, because the
	// symptom is a name that answers over IPv6 and returns NODATA for A - which
	// looks like a browser problem and is the exact failure ADR-021 exists to
	// fix, so it must be visible somewhere rather than silent.
	exhausted int
}

// AliasGrace is how long a peer keeps its alias after leaving the roster.
//
// Long enough that a flap, a reboot or a laptop lid never moves an address
// under a live connection; short enough that the block does not fill with
// devices that are never coming back.
const AliasGrace = 6 * time.Hour

// Entry is one device: its overlay address and the key that derives its alias.
type Entry struct {
	Overlay   netip.Addr
	DevicePub ed25519.PublicKey
}

// NewTable builds a mapping for this device and its peers, within the block
// belonging to networkID.
func NewTable(networkID string, self Entry, peers []Entry) *Table {
	return NewTableIn(Block(networkID), self, peers)
}

// NewTableIn builds the same mapping in a block the caller has chosen, which is
// what a device with several meshes needs: the block is assigned across the set
// (see Blocks), not derived one mesh at a time.
func NewTableIn(block netip.Prefix, self Entry, peers []Entry) *Table {
	t := &Table{
		block: block,
		to4:   make(map[netip.Addr]netip.Addr, len(peers)+1),
		to6:   make(map[netip.Addr]netip.Addr, len(peers)+1),
		self6: self.Overlay,
		seen:  make(map[netip.Addr]time.Time, len(peers)+1),
	}
	// Seeded now, so the grace period runs from construction. Without this an
	// entry made here has no timestamp, and the reclaim loop - which walks the
	// timestamps - would never consider it.
	now := time.Now()
	t.add(self)
	t.self = t.to4[self.Overlay]
	t.seen[self.Overlay] = now
	for _, p := range peers {
		t.add(p)
		if p.Overlay.IsValid() {
			t.seen[p.Overlay] = now
		}
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
		a := Alias(t.block, e.DevicePub, nonce)
		if !a.IsValid() {
			break // the block holds no usable address at all
		}
		if _, taken := t.to6[a]; taken {
			continue
		}
		t.to4[e.Overlay] = a
		t.to6[a] = e.Overlay
		return
	}
	// No room. The peer resolves over IPv6 and gets NODATA for A.
	t.exhausted++
}

// Exhausted counts peers the block had no room for, so a caller can say so
// rather than leaving a half-resolving name to be discovered by hand.
func (t *Table) Exhausted() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.exhausted
}

// Update replaces the peer set, keeping this device's own alias fixed.
//
// Aliases must not move underneath a live connection, so a peer that is already
// mapped keeps the address it has even if it briefly leaves the roster.
func (t *Table) Update(peers []Entry) { t.UpdateAt(peers, time.Now()) }

// UpdateAt is Update at a stated time, so the grace period can be tested.
//
// "Briefly" used to mean forever: nothing ever removed an entry, while the
// roster this is fed from prunes on every change. The table grew for the life
// of the process, and as the block filled - 13 bits, 8192 addresses - add
// started failing and peers silently lost their A records while IPv6 kept
// working, which is what made the original bug hard to find.
func (t *Table) UpdateAt(peers []Entry, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[netip.Addr]time.Time{}
	}
	for _, p := range peers {
		t.add(p)
		if p.Overlay.IsValid() {
			t.seen[p.Overlay] = now
		}
	}
	for overlay, last := range t.seen {
		if now.Sub(last) < AliasGrace || overlay == t.self6 {
			continue
		}
		if a, ok := t.to4[overlay]; ok {
			delete(t.to6, a)
		}
		delete(t.to4, overlay)
		delete(t.seen, overlay)
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

// Block returns the range this mesh's aliases come from, which is what has to
// be routed at its interface.
func (t *Table) Block() netip.Prefix { return t.block }

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
