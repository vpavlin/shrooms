// Package disco is the socket-level discovery protocol: small authenticated
// probes sent over the same UDP socket as WireGuard, used to find which of a
// peer's candidate endpoints actually works.
//
// This is Tailscale's disco shape, and the reason it exists is that WireGuard
// holds exactly one endpoint per peer. You cannot "spray" WireGuard handshakes
// at five candidates and let the best win — you would be overwriting the
// endpoint under yourself. So probe with cheap packets first, then set the
// endpoint that answered.
//
// Two jobs:
//
//  1. Liveness — which candidate can actually carry packets to this peer.
//  2. Reflexive discovery — every Pong echoes the source address it was
//     observed at, so peers tell each other their public ip:port. With N peers
//     you get N-1 independent vantage points and need no STUN server. Tailscale
//     does the same thing and its source calls Pong "effectively a STUN
//     response".
//
// Packets are authenticated with a key derived from the network key, which in
// v1 means "authenticated as a mesh member" — consistent with the bearer model.
// That is enough to stop off-path injection; it is not per-device
// authentication, which arrives with credentials in M5.
package disco

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// Type identifies a disco message.
type Type uint8

const (
	TypePing Type = 1
	TypePong Type = 2
)

const (
	// TxIDLen is the length of the transaction id correlating a Pong to a Ping.
	TxIDLen = 12
	// macLen is the truncated HMAC length. 16 bytes is ample for rejecting
	// off-path forgeries on a personal mesh.
	macLen = 16
	// devicePubLen is an ed25519 public key.
	devicePubLen = 32

	// addrLen is a serialised ip:port: 16-byte v6 address plus 2-byte port.
	// v4 is stored as v4-in-v6 so the wire format is fixed-size, which keeps
	// the packet a constant length.
	addrLen = 18

	headerLen = 1 + 1 + devicePubLen + TxIDLen // version, type, sender, txid

	// PingLen and PongLen are fixed. Constant-size probes give a traffic
	// observer nothing to distinguish a ping from a pong beyond direction.
	PingLen = headerLen + macLen
	PongLen = headerLen + addrLen + macLen
)

const version byte = 1

// TxID correlates a Pong with the Ping that caused it.
type TxID [TxIDLen]byte

// NewTxID returns a random transaction id.
func NewTxID() (TxID, error) {
	var t TxID
	if _, err := rand.Read(t[:]); err != nil {
		return TxID{}, fmt.Errorf("generate txid: %w", err)
	}
	return t, nil
}

// Key is the disco authentication key, derived from the network key.
type Key [32]byte

// DeriveKey returns the disco key for a mesh.
func DeriveKey(nk identity.NetworkKey) Key {
	r := hkdf.New(sha256.New, nk[:], nil, []byte("mesh/v1/disco"))
	var k Key
	if _, err := r.Read(k[:]); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return k
}

// Message is a decoded disco packet.
type Message struct {
	Type      Type
	SenderPub [devicePubLen]byte
	TxID      TxID

	// Observed is the address the sender saw us at. Only set on a Pong, and
	// this is the reflexive discovery result.
	Observed netip.AddrPort
}

// EncodePing builds an authenticated ping.
func EncodePing(k Key, senderPub []byte, tx TxID) ([]byte, error) {
	if len(senderPub) != devicePubLen {
		return nil, errors.New("sender public key must be 32 bytes")
	}
	buf := make([]byte, 0, PingLen)
	buf = append(buf, version, byte(TypePing))
	buf = append(buf, senderPub...)
	buf = append(buf, tx[:]...)
	return append(buf, mac(k, buf)...), nil
}

// EncodePong builds an authenticated pong echoing the observed source address.
func EncodePong(k Key, senderPub []byte, tx TxID, observed netip.AddrPort) ([]byte, error) {
	if len(senderPub) != devicePubLen {
		return nil, errors.New("sender public key must be 32 bytes")
	}
	buf := make([]byte, 0, PongLen)
	buf = append(buf, version, byte(TypePong))
	buf = append(buf, senderPub...)
	buf = append(buf, tx[:]...)
	buf = append(buf, encodeAddr(observed)...)
	return append(buf, mac(k, buf)...), nil
}

// Decode parses and authenticates a disco packet.
func Decode(k Key, pkt []byte) (*Message, error) {
	if len(pkt) < headerLen+macLen {
		return nil, errors.New("packet too short")
	}
	if pkt[0] != version {
		return nil, fmt.Errorf("unsupported disco version %d", pkt[0])
	}

	body, gotMAC := pkt[:len(pkt)-macLen], pkt[len(pkt)-macLen:]
	if !hmac.Equal(gotMAC, mac(k, body)) {
		return nil, errors.New("authentication failed")
	}

	m := &Message{Type: Type(pkt[1])}
	copy(m.SenderPub[:], pkt[2:2+devicePubLen])
	copy(m.TxID[:], pkt[2+devicePubLen:headerLen])

	switch m.Type {
	case TypePing:
		if len(pkt) != PingLen {
			return nil, fmt.Errorf("ping is %d bytes, want %d", len(pkt), PingLen)
		}
	case TypePong:
		if len(pkt) != PongLen {
			return nil, fmt.Errorf("pong is %d bytes, want %d", len(pkt), PongLen)
		}
		ap, err := decodeAddr(pkt[headerLen : headerLen+addrLen])
		if err != nil {
			return nil, err
		}
		m.Observed = ap
	default:
		return nil, fmt.Errorf("unknown disco type %d", m.Type)
	}
	return m, nil
}

func mac(k Key, body []byte) []byte {
	h := hmac.New(sha256.New, k[:])
	h.Write(body)
	return h.Sum(nil)[:macLen]
}

// encodeAddr writes an AddrPort as 16-byte address (v4-mapped if needed) plus
// a 2-byte port, so every pong is the same size.
func encodeAddr(ap netip.AddrPort) []byte {
	out := make([]byte, addrLen)
	a := ap.Addr()
	if a.Is4() {
		a = netip.AddrFrom16(a.As16()) // v4-in-v6
	}
	if a.IsValid() {
		b := a.As16()
		copy(out[:16], b[:])
	}
	binary.BigEndian.PutUint16(out[16:], ap.Port())
	return out
}

func decodeAddr(b []byte) (netip.AddrPort, error) {
	if len(b) != addrLen {
		return netip.AddrPort{}, errors.New("bad address length")
	}
	var raw [16]byte
	copy(raw[:], b[:16])
	addr := netip.AddrFrom16(raw).Unmap()
	port := binary.BigEndian.Uint16(b[16:])
	return netip.AddrPortFrom(addr, port), nil
}
