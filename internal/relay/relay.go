// Package relay carries WireGuard traffic between peers that cannot reach each
// other directly.
//
// The design is deliberately dumb, following Tailscale's DERP: the relay is a
// packet reflector keyed by WireGuard public key. It never terminates a tunnel,
// never holds a session key, and cannot read anything it forwards — WireGuard
// has already encrypted and authenticated the payload, so the relay needs no
// cryptographic involvement at all.
//
// What it does need is to not be an open reflector, hence the mesh-wide MAC on
// the header. That authenticates "a mesh member sent this", which is enough to
// stop strangers using it as an amplifier. It is not per-device authentication;
// see SECURITY.md phase 4.
//
// Frame layout, after the shared-socket demux has stripped its magic and
// subtype byte:
//
//	[1]  type          register | forward
//	[32] key           register: the sender's own key. forward: the DESTINATION
//	[32] src           forward only: who it came from (filled in by the relay)
//	[16] mac           over everything above
//	[..] payload       an opaque WireGuard packet
//
// A client sends `forward` naming the destination; the relay rewrites the frame
// with `src` set and delivers it. The receiving client hands the payload to
// WireGuard as if it had arrived from that peer.
package relay

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/shrooms/internal/identity"
)

// Type identifies a relay frame.
type Type uint8

const (
	// TypeRegister tells a relay where to reach us. Sent periodically, since
	// the relay's mapping is soft state and a NAT rebinding invalidates it.
	TypeRegister Type = 1
	// TypeForward carries a payload to another peer.
	TypeForward Type = 2
)

const (
	keyLen = 32
	macLen = 16

	// devicePubLen and sigLen carry the proof that the registrant owns the
	// tunnel key it is registering (ADR-029, the relay half).
	//
	// Without them the frame was authenticated only by the mesh-wide MAC, which
	// every member can compute, and nothing tied the key inside to the sender —
	// so a member could tell a relay that somebody else's key was reachable at
	// its own address, and every peer relaying to that victim delivered to the
	// attacker instead.
	devicePubLen = 32
	sigLen       = 64
	// stampLen bounds replay: a captured registration is only useful for as
	// long as the relay will accept its timestamp.
	stampLen = 8

	registerLen = 1 + keyLen + stampLen + devicePubLen + sigLen + macLen
	// A forward frame carries both keys so the receiver knows the sender
	// without the relay having to be trusted to tell the truth separately.
	forwardHeaderLen = 1 + keyLen + keyLen + macLen
)

// MaxPayload bounds a forwarded packet. WireGuard's own maximum is well under
// this; the cap exists so a malformed frame cannot allocate arbitrarily.
const MaxPayload = 65535

// Key authenticates relay frames. Derived from the network key, so any mesh
// member can use the relay and nobody else can.
type Key [32]byte

// DeriveKey returns the relay key for a mesh.
func DeriveKey(nk identity.NetworkKey) Key {
	r := hkdf.New(sha256.New, nk[:], nil, []byte("mesh/v1/relay"))
	var k Key
	if _, err := r.Read(k[:]); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return k
}

// Frame is a decoded relay frame.
type Frame struct {
	Type    Type
	Key     identity.WGKey // register: sender. forward: destination.
	Src     identity.WGKey // forward only
	Payload []byte

	// DevicePub and At are register only: who asked, and when. The server
	// checks that the device owns Key and that the frame is recent.
	DevicePub []byte
	At        int64

	// Nonce is the routability challenge, on the two frames that carry one.
	Nonce [NonceLen]byte
}

// ErrNotSignedByDevice is a registration whose signature does not match the
// device key it carries.
var ErrNotSignedByDevice = errors.New("registration is not signed by the device it names")

// EncodeRegister builds a registration frame.
// EncodeRegister builds a registration proving the sender owns the key it
// registers.
//
// Two layers, doing different jobs. The MAC says "a member of this mesh sent
// this", which keeps outsiders out and is all it ever said. The signature says
// "the device named here asked for it", which is what stops one member
// registering another's key.
func EncodeRegister(k Key, self identity.WGKey, priv ed25519.PrivateKey, now time.Time) []byte {
	buf := make([]byte, 0, registerLen)
	buf = append(buf, byte(TypeRegister))
	buf = append(buf, self[:]...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(now.Unix()))
	buf = append(buf, priv.Public().(ed25519.PublicKey)...)
	buf = append(buf, ed25519.Sign(priv, buf)...)
	return append(buf, mac(k, buf)...)
}

// EncodeForward builds a frame addressed to dst. src is zero when a client
// sends; the relay fills it in before delivery so the receiver knows who sent
// it without trusting a separate channel.
func EncodeForward(k Key, dst, src identity.WGKey, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("payload is %d bytes, max %d", len(payload), MaxPayload)
	}
	buf := make([]byte, 0, forwardHeaderLen+len(payload))
	buf = append(buf, byte(TypeForward))
	buf = append(buf, dst[:]...)
	buf = append(buf, src[:]...)
	buf = append(buf, mac(k, buf)...)
	return append(buf, payload...), nil
}

// Decode parses and authenticates a frame.
func Decode(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) < 1 {
		return nil, errors.New("empty frame")
	}

	switch Type(pkt[0]) {
	case TypeRegister:
		if len(pkt) != registerLen {
			return nil, fmt.Errorf("register frame is %d bytes, want %d", len(pkt), registerLen)
		}
		signed := 1 + keyLen + stampLen + devicePubLen
		if !verify(k, pkt[:signed+sigLen], pkt[signed+sigLen:]) {
			return nil, errors.New("authentication failed")
		}
		f := &Frame{Type: TypeRegister}
		copy(f.Key[:], pkt[1:1+keyLen])
		f.At = int64(binary.BigEndian.Uint64(pkt[1+keyLen : 1+keyLen+stampLen]))
		f.DevicePub = append([]byte(nil), pkt[1+keyLen+stampLen:signed]...)
		// The device's own signature over everything it is claiming. Checked
		// here so no caller can read Key without it having been proven.
		if !ed25519.Verify(ed25519.PublicKey(f.DevicePub), pkt[:signed], pkt[signed:signed+sigLen]) {
			return nil, ErrNotSignedByDevice
		}
		return f, nil

	case TypeForward:
		if len(pkt) < forwardHeaderLen {
			return nil, fmt.Errorf("forward frame is %d bytes, want at least %d", len(pkt), forwardHeaderLen)
		}
		body := pkt[:1+keyLen+keyLen]
		if !verify(k, body, pkt[1+keyLen+keyLen:forwardHeaderLen]) {
			return nil, errors.New("authentication failed")
		}
		f := &Frame{Type: TypeForward}
		copy(f.Key[:], pkt[1:1+keyLen])
		copy(f.Src[:], pkt[1+keyLen:1+keyLen+keyLen])
		f.Payload = pkt[forwardHeaderLen:]
		return f, nil

	case TypeChallenge:
		return decodeChallenge(k, pkt)

	case TypeConfirm:
		return decodeConfirm(k, pkt)

	default:
		return nil, fmt.Errorf("unknown relay frame type %d", pkt[0])
	}
}

func mac(k Key, body []byte) []byte {
	h := hmac.New(sha256.New, k[:])
	h.Write(body)
	return h.Sum(nil)[:macLen]
}

func verify(k Key, body, got []byte) bool {
	return hmac.Equal(got, mac(k, body))
}
