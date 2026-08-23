// Package identity holds device keys, the mesh network key, and the derived
// addressing scheme.
//
// Two independent keypairs per device (see DESIGN §7):
//
//   - Device key (Ed25519) — signs control messages, and the overlay address is
//     derived from it. Never used for Diffie-Hellman.
//   - WireGuard key (X25519) — the tunnel static key.
//
// They are deliberately NOT the same key. WireGuard needs the raw scalar in
// memory, so reusing one key would make it impossible to keep the device key in
// hardware later.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// NetworkKeyLen is the length of the mesh network key in bytes.
const NetworkKeyLen = 32

// b32 is unpadded, uppercase, and human-transcribable.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NetworkKey is the mesh-wide secret. In v1 it is a bearer credential: holding
// it makes you a member. It is the root of topic derivation, payload
// encryption, and per-pair WireGuard PSKs.
type NetworkKey [NetworkKeyLen]byte

// NewNetworkKey generates a fresh network key.
func NewNetworkKey() (NetworkKey, error) {
	var nk NetworkKey
	if _, err := rand.Read(nk[:]); err != nil {
		return NetworkKey{}, fmt.Errorf("generate network key: %w", err)
	}
	return nk, nil
}

// String renders the key for copy/paste between machines.
func (nk NetworkKey) String() string { return b32.EncodeToString(nk[:]) }

// NetworkKeyFromBytes takes a key that arrived as raw bytes rather than text —
// out of an invite, which carries it inside a sealed message where base32 would
// be nothing but padding.
func NetworkKeyFromBytes(b []byte) (NetworkKey, error) {
	if len(b) != NetworkKeyLen {
		return NetworkKey{}, fmt.Errorf("network key is %d bytes, want %d", len(b), NetworkKeyLen)
	}
	var nk NetworkKey
	copy(nk[:], b)
	return nk, nil
}

// ParseNetworkKey parses the String form.
func ParseNetworkKey(s string) (NetworkKey, error) {
	raw, err := b32.DecodeString(s)
	if err != nil {
		return NetworkKey{}, fmt.Errorf("decode network key: %w", err)
	}
	if len(raw) != NetworkKeyLen {
		return NetworkKey{}, fmt.Errorf("network key is %d bytes, want %d", len(raw), NetworkKeyLen)
	}
	var nk NetworkKey
	copy(nk[:], raw)
	return nk, nil
}

// derive is HKDF-SHA256 over the network key with a label.
func (nk NetworkKey) derive(label string, n int) []byte {
	out := make([]byte, n)
	r := hkdf.New(sha256.New, nk[:], nil, []byte(label))
	if _, err := r.Read(out); err != nil {
		// HKDF over a fixed-size key with a small output cannot fail.
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return out
}

// TopicKey is the root of rendezvous-topic derivation.
func (nk NetworkKey) TopicKey() []byte { return nk.derive("mesh/v1/topic", 32) }

// PayloadKey is the root of control-message encryption.
func (nk NetworkKey) PayloadKey() []byte { return nk.derive("mesh/v1/payload", 32) }

// PSKKey is the root of per-pair WireGuard preshared keys.
func (nk NetworkKey) PSKKey() []byte { return nk.derive("mesh/v1/wgpsk", 32) }

// Prefix returns the mesh's ULA /48, derived from the network key.
//
// RFC 4193 defines fd00::/8 for local self-assignment with a pseudo-random
// 40-bit Global ID; SHA256(NK) supplies that. Deliberately not fc00::/8 (cjdns
// squats it, and it collides with real ULA space) nor 0200::/7 (Yggdrasil).
func (nk NetworkKey) Prefix() netip.Prefix {
	h := sha256.Sum256(append([]byte("mesh/v1/ula"), nk[:]...))
	var ip [16]byte
	ip[0] = 0xfd
	copy(ip[1:6], h[:5])
	return netip.PrefixFrom(netip.AddrFrom16(ip), 48)
}

// WGKey is a Curve25519 key (public or private).
type WGKey [32]byte

func (k WGKey) String() string { return hex.EncodeToString(k[:]) }

// IsZero reports whether the key is unset.
func (k WGKey) IsZero() bool { return k == WGKey{} }

// Identity is a device's long-term key material.
type Identity struct {
	DevicePriv ed25519.PrivateKey
	DevicePub  ed25519.PublicKey
	WGPriv     WGKey
	WGPub      WGKey

	// SealPriv/SealPub receive secrets addressed to this device on the control
	// plane — today, a rekey of the announce generation after somebody is
	// revoked.
	//
	// A third keypair rather than reusing one of the two above, and the reasons
	// are worth keeping. WGPub is already X25519 and already in every announce,
	// so it looks free — but it is WireGuard's static key, and sharing one
	// static across two protocols is where cross-protocol attacks live; a
	// domain separator in our KDF does not constrain what Noise does with it.
	// DevicePub could be mapped ed25519 to X25519, which age and Signal both
	// do, but it makes one key both sign announces and perform key agreement,
	// and the conversion needs a dependency this tree does not carry.
	//
	// The cost of a third key is nothing stored and nothing derived twice: it
	// falls out of the same master secret as the other two.
	SealPriv WGKey
	SealPub  WGKey
}

// New generates a fresh device identity.
func New() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}

	var wgPriv WGKey
	if _, err := rand.Read(wgPriv[:]); err != nil {
		return nil, fmt.Errorf("generate wireguard key: %w", err)
	}
	clamp(&wgPriv)

	wgPubSlice, err := curve25519.X25519(wgPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive wireguard public key: %w", err)
	}
	var wgPub WGKey
	copy(wgPub[:], wgPubSlice)

	id := &Identity{DevicePriv: priv, DevicePub: pub, WGPriv: wgPriv, WGPub: wgPub}
	if err := id.deriveSealing(); err != nil {
		return nil, err
	}
	return id, nil
}

// deriveSealing fills SealPriv/SealPub from the ed25519 seed.
//
// Derived rather than generated or stored, so that an identity rebuilt from an
// existing state.json - which has no field for it and predates this key - comes
// back with the same one every time. A device that derived a different sealing
// key on each start could not be sent anything.
//
// From the seed by HKDF, which is one-way: this is not the signing key wearing
// a hat, and nothing about SealPriv reveals DevicePriv.
//
// Every constructor must call this. Leaving it unset is not an inert omission:
// PublicFromPrivate accepts an all-zero scalar without error and returns a
// perfectly ordinary-looking public key whose private half is thirty-two zero
// bytes, so every affected device would advertise the SAME key and anything
// sealed to it would be readable by anybody.
func (i *Identity) deriveSealing() error {
	if len(i.DevicePriv) < ed25519.SeedSize {
		return errors.New("identity has no device key to derive a sealing key from")
	}
	r := hkdf.New(sha256.New, i.DevicePriv.Seed(), nil, []byte("mesh/v1/seal/from-device"))
	if _, err := io.ReadFull(r, i.SealPriv[:]); err != nil {
		return fmt.Errorf("derive sealing key: %w", err)
	}
	clamp(&i.SealPriv)
	pub, err := PublicFromPrivate(i.SealPriv)
	if err != nil {
		return fmt.Errorf("derive sealing public key: %w", err)
	}
	i.SealPub = pub
	return nil
}

// DeriveSealing fills in the sealing keypair on an Identity assembled field by
// field, as the state loader does when it rebuilds one from disk.
func (i *Identity) DeriveSealing() error { return i.deriveSealing() }

// clamp applies the standard X25519 scalar clamping WireGuard expects.
func clamp(k *WGKey) {
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
}

// OverlayAddr returns the device's overlay address: the mesh /48 followed by 80
// bits derived from the device public key.
//
// This deletes IPAM from the control plane — every node computes every other
// node's address locally, so there are no allocation messages, no conflicts and
// no split-brain. 80 bits is ample because the address is NOT an authenticator:
// authentication comes from WireGuard's handshake and (later) the admin-signed
// credential. Collision probability across 10 devices is ~4e-23.
func OverlayAddr(nk NetworkKey, devicePub ed25519.PublicKey) netip.Addr {
	h := sha256.Sum256(append([]byte("mesh/v1/addr"), devicePub...))

	prefix := nk.Prefix().Addr().As16()
	var ip [16]byte
	copy(ip[:6], prefix[:6]) // /48 from the network key
	copy(ip[6:], h[:10])     // 80-bit host from the device key
	return netip.AddrFrom16(ip)
}

// PairPSK returns the WireGuard preshared key for a pair of devices. Sorted so
// both sides derive the same value.
//
// Rotating the network key invalidates every tunnel at the WireGuard layer,
// independent of the control plane being correct, and enables WireGuard's
// post-quantum hedge for free.
func PairPSK(nk NetworkKey, a, b WGKey) [32]byte {
	lo, hi := a, b
	if greater(lo, hi) {
		lo, hi = hi, lo
	}
	r := hkdf.New(sha256.New, nk.PSKKey(), nil, append(append([]byte("mesh/v1/pair"), lo[:]...), hi[:]...))
	var psk [32]byte
	if _, err := r.Read(psk[:]); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return psk
}

func greater(a, b WGKey) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// ParseWGKey parses a hex-encoded Curve25519 key.
func ParseWGKey(s string) (WGKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return WGKey{}, fmt.Errorf("decode key: %w", err)
	}
	if len(raw) != 32 {
		return WGKey{}, errors.New("key must be 32 bytes")
	}
	var k WGKey
	copy(k[:], raw)
	return k, nil
}

// PublicFromPrivate derives the Curve25519 public key for a private key.
func PublicFromPrivate(priv WGKey) (WGKey, error) {
	pubSlice, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return WGKey{}, fmt.Errorf("derive public key: %w", err)
	}
	var pub WGKey
	copy(pub[:], pubSlice)
	return pub, nil
}
