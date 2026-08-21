package wg

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/vpavlin/shrooms/internal/identity"
)

// Peer is a mesh member as far as the data plane is concerned.
type Peer struct {
	WGPub     identity.WGKey
	Endpoint  string // host:port; empty means "wait for them to reach us"
	AllowedIP netip.Addr
	PSK       [32]byte

	// PersistentKeepalive in seconds. DESIGN §10: 15-25s, never above 25 —
	// 74% of CGNATs expire idle UDP state within 60s and the non-cellular CGN
	// median is 35s.
	Keepalive int

	// KeepEndpoint leaves the peer's current endpoint untouched.
	//
	// Writing `endpoint=` overwrites whatever WireGuard has, including an
	// address it learned from an authenticated packet. When we have nothing
	// better than an unvalidated announced candidate, saying nothing is
	// strictly better than replacing a working endpoint with a guess.
	KeepEndpoint bool

	// RelayVia routes this peer through a relay instead of a direct endpoint.
	// Mutually exclusive with Endpoint; when set, WireGuard is told to send to
	// the relay and the Bind wraps each packet.
	RelayVia *RelayEndpoint
}

// Device is a userspace WireGuard device whose UDP socket is shared with our
// control protocol.
type Device struct {
	*device.Device
	Bind *Bind
	tun  tun.Device

	mu      sync.Mutex
	applied []Peer // last configuration written, for diffing
}

// SetMSSLimit installs a per-peer TCP segment limit on the tun.
//
// A no-op on a device whose tun is not one of ours, which is the case in tests
// and in cmd/m0demo — clamping is a correctness fix for a relayed path, and a
// netstack tun in a test has no relay under it.
func (d *Device) SetMSSLimit(f func(netip.Addr) uint16) {
	if p, ok := d.tun.(*Port); ok {
		p.SetMSSLimit(f)
	}
}

// NewDevice brings up a WireGuard device on the given TUN, using a Bind that
// demultiplexes control packets off the same socket.
//
// Userspace rather than kernel WireGuard is a deliberate choice: kernel
// WireGuard owns its UDP socket and will not share it, which makes NAT
// traversal impossible to do correctly. See the package doc.
func NewDevice(t tun.Device, priv identity.WGKey, listenPort uint16, logger *device.Logger) (*Device, error) {
	b := NewBind()
	dev := device.NewDevice(t, b, logger)

	cfg := fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(priv[:]), listenPort)
	if err := dev.IpcSet(cfg); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("bring device up: %w", err)
	}
	return &Device{Device: dev, Bind: b, tun: t}, nil
}

// SetPeers reconciles the peer set.
//
// It deliberately does NOT use replace_peers. That removes and recreates every
// peer, and a peer that is being recreated is not running — so a handshake
// initiation arriving in that window is rejected with "Received invalid
// initiation message", and the handshake never completes.
//
// This is easy to miss on a LAN, where a handshake finishes in a millisecond
// and never overlaps the gap. Over the internet, with 100-450 ms round trips
// and a sync on every probe response, it loses the race repeatedly.
//
// Instead peers are updated in place, which preserves handshake state, and only
// peers that have genuinely left are removed. Endpoint changes in particular
// must not disturb an established tunnel — that is how roaming works.
// Invalidate forgets what was last applied, so the next SetPeers writes
// unconditionally.
//
// For the case where the device has changed underneath us — endpoint roaming —
// and our idea of the configuration is therefore stale rather than wrong.
func (d *Device) Invalidate() {
	d.mu.Lock()
	d.applied = nil
	d.mu.Unlock()
}

func (d *Device) SetPeers(peers []Peer) error {
	want := make(map[identity.WGKey]bool, len(peers))
	for _, p := range peers {
		want[p.WGPub] = true
	}

	// Nothing to do if the configuration is identical. Skipping the write
	// avoids touching the device at all on the common no-op sync.
	//
	// The cache is what WE last wrote, not what the device currently holds, and
	// those diverge: WireGuard roams a peer's endpoint to the source of any
	// authenticated packet it receives. So a peer that answers over a relay
	// silently moves the device's endpoint while this cache still says direct,
	// and every later sync is a no-op that preserves the drift. Invalidate
	// makes the caller able to say "write it anyway"; see Mesh.syncPeers, which
	// compares intent against the device's real endpoints.
	d.mu.Lock()
	unchanged := d.applied != nil && samePeers(d.applied, peers)
	d.mu.Unlock()
	if unchanged {
		return nil
	}

	var b strings.Builder

	// Remove peers that have left the roster. Without replace_peers they would
	// otherwise linger.
	d.mu.Lock()
	for _, prev := range d.applied {
		if !want[prev.WGPub] {
			fmt.Fprintf(&b, "public_key=%s\nremove=true\n", hex.EncodeToString(prev.WGPub[:]))
		}
	}
	d.mu.Unlock()

	for _, p := range peers {
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(p.WGPub[:]))
		if p.PSK != ([32]byte{}) {
			fmt.Fprintf(&b, "preshared_key=%s\n", hex.EncodeToString(p.PSK[:]))
		}
		switch {
		case p.KeepEndpoint:
			// Deliberately emits no endpoint line.
		case p.RelayVia != nil:
			// Our own endpoint form, which Bind.ParseEndpoint turns back into a
			// RelayEndpoint. WireGuard treats the string as opaque.
			fmt.Fprintf(&b, "endpoint=%s\n", p.RelayVia.DstToString())
		case p.Endpoint != "":
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
		if p.Keepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.Keepalive)
		}
		if p.AllowedIP.IsValid() {
			// /128 — the address is a pure function of the peer's device key,
			// so cryptokey routing is self-enforcing with no shared state.
			fmt.Fprintf(&b, "allowed_ip=%s/128\n", p.AllowedIP)
		}
	}
	if err := d.IpcSet(b.String()); err != nil {
		return fmt.Errorf("set peers: %w", err)
	}

	d.mu.Lock()
	d.applied = append([]Peer(nil), peers...)
	d.mu.Unlock()
	return nil
}

// samePeers reports whether two peer sets are equivalent for configuration
// purposes. Order is ignored: the roster is sorted by name, so a rename would
// otherwise reshuffle it and trigger a pointless rewrite.
func samePeers(a, b []Peer) bool {
	if len(a) != len(b) {
		return false
	}
	idx := make(map[identity.WGKey]Peer, len(a))
	for _, p := range a {
		idx[p.WGPub] = p
	}
	for _, p := range b {
		prev, ok := idx[p.WGPub]
		if !ok || prev.Endpoint != p.Endpoint || prev.PSK != p.PSK ||
			prev.AllowedIP != p.AllowedIP || prev.Keepalive != p.Keepalive ||
			prev.KeepEndpoint != p.KeepEndpoint {
			return false
		}
		// Relay endpoints compare by their serialised form, which encodes both
		// the peer key and the relay address.
		switch {
		case (prev.RelayVia == nil) != (p.RelayVia == nil):
			return false
		case prev.RelayVia != nil && prev.RelayVia.DstToString() != p.RelayVia.DstToString():
			return false
		}
	}
	return true
}

// Close shuts the device down.
func (d *Device) Close() error {
	d.Device.Close()
	return nil
}
