package wg

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/vpavlin/logos-vpn/internal/identity"
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
}

// Device is a userspace WireGuard device whose UDP socket is shared with our
// control protocol.
type Device struct {
	*device.Device
	Bind *Bind
	tun  tun.Device
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

// SetPeers replaces the peer set.
//
// Note replace_peers=true is used deliberately here: the mesh roster is
// authoritative and we always send the full set. Partial updates via wgctrl are
// a known footgun (wgctrl-go#88 silently wiped AllowedIPs), so we avoid the
// merge semantics entirely.
func (d *Device) SetPeers(peers []Peer) error {
	var b strings.Builder
	b.WriteString("replace_peers=true\n")

	for _, p := range peers {
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(p.WGPub[:]))
		if p.PSK != ([32]byte{}) {
			fmt.Fprintf(&b, "preshared_key=%s\n", hex.EncodeToString(p.PSK[:]))
		}
		if p.Endpoint != "" {
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
	return nil
}

// Close shuts the device down.
func (d *Device) Close() error {
	d.Device.Close()
	return nil
}
