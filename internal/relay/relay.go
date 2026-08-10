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
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"

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

	registerLen = 1 + keyLen + macLen
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
}

// EncodeRegister builds a registration frame.
func EncodeRegister(k Key, self identity.WGKey) []byte {
	buf := make([]byte, 0, registerLen)
	buf = append(buf, byte(TypeRegister))
	buf = append(buf, self[:]...)
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
		if !verify(k, pkt[:1+keyLen], pkt[1+keyLen:]) {
			return nil, errors.New("authentication failed")
		}
		f := &Frame{Type: TypeRegister}
		copy(f.Key[:], pkt[1:1+keyLen])
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
