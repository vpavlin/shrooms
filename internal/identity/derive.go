package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// A separate identity per mesh, derived from one secret (ADR-015).
//
// This is load-bearing rather than hygiene. WireGuard identifies a peer by its
// public key within a device and allows one preshared key per peer, while our
// PSK is per mesh — so a device sharing two meshes with us cannot be
// represented twice on one WireGuard device unless it presents a different
// public key in each. Per-mesh derivation gives that for free.
//
// It also closes a linkability leak. OverlayAddr mixes the network key into the
// prefix but not into the host bits, which are SHA256("mesh/v1/addr" ‖
// device_pub); a device reusing one identity therefore carries the same 80-bit
// suffix in every mesh it joins, and anyone in two of them can correlate it.
// The same is true of DevicePub in the announce. Deriving per mesh removes
// both, so joining a shared mesh reveals nothing about membership of a private
// one.

// MasterLen is the size of the secret every per-mesh identity derives from.
const MasterLen = 32

// Master is the root secret of a device. It is never used directly and never
// leaves the machine: it exists so that a device has one thing to keep rather
// than one per mesh.
type Master [MasterLen]byte

// NewMaster generates a device's root secret.
func NewMaster() (Master, error) {
	var m Master
	if _, err := rand.Read(m[:]); err != nil {
		return Master{}, fmt.Errorf("generate master secret: %w", err)
	}
	return m, nil
}

// Derive produces this device's identity within one mesh.
//
// networkID names the mesh (see state.NetworkID) and is derived from its
// network key, not from its admin keys — a mesh may have no admin keys at all,
// and this has to work for every mesh.
func (m Master) Derive(networkID string) (*Identity, error) {
	devSeed := m.expand("mesh/v1/identity", networkID, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(devSeed)

	var wgPriv WGKey
	copy(wgPriv[:], m.expand("mesh/v1/wg", networkID, 32))
	clamp(&wgPriv)

	wgPubSlice, err := curve25519.X25519(wgPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive wireguard public key: %w", err)
	}
	var wgPub WGKey
	copy(wgPub[:], wgPubSlice)

	id := &Identity{
		DevicePriv: priv,
		DevicePub:  priv.Public().(ed25519.PublicKey),
		WGPriv:     wgPriv,
		WGPub:      wgPub,
	}
	// The control-plane sealing key, from the device key rather than from the
	// master. There must be exactly ONE definition of it: the state loader
	// rebuilds an identity from the stored device and tunnel keys and has no
	// master to hand, so a master-derived sealing key came back different after
	// a restart — the device would advertise one key and hold another, and
	// rekeys addressed to it would silently stop opening. It is still per-mesh,
	// because the device key it comes from is.
	if err := id.deriveSealing(); err != nil {
		return nil, err
	}
	return id, nil
}

func (m Master) expand(label, networkID string, n int) []byte {
	r := hkdf.New(sha256.New, m[:], nil, append([]byte(label), networkID...))
	out := make([]byte, n)
	if _, err := r.Read(out); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return out
}
