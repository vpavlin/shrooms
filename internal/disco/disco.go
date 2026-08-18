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
// Packets are ENCRYPTED, not merely authenticated, under a key derived from the
// network key. Authenticating alone would leave the sender's device public key
// in cleartext on every probe — a stable 32-byte identifier that any on-path
// observer could use to correlate a device across every network it joins. Pongs
// would additionally expose the observed address, leaking topology.
//
// Every packet is the same size regardless of type, so an observer cannot even
// distinguish a ping from a pong by length.
//
// The key is mesh-wide, so this is "authenticated as a mesh member", consistent
// with the bearer model. Per-device authentication arrives with credentials in
// M5.
package disco

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/shrooms/internal/identity"
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
	// devicePubLen is an ed25519 public key.
	devicePubLen = 32

	// addrLen is a serialised ip:port: 16-byte v6 address plus 2-byte port.
	// v4 is stored as v4-in-v6 so the plaintext is a fixed size.
	addrLen = 18

	// sigLen is an ed25519 signature over everything above it.
	//
	// The packet already names its sender and, until this existed, nothing
	// proved the name: every member holds the same disco key, so any member
	// could compose a packet claiming to be any device — enough to steer
	// another node's path selection and to seed its reflexive addresses
	// (ADR-029). Signing with the device key the packet already names needs no
	// credential and no authority, because the claim is about the key alone.
	sigLen = ed25519.SignatureSize

	// bodyLen is what the signature covers: type, sender, txid and an address
	// slot that a ping leaves zeroed. Padding a ping out to a pong's length is
	// what makes the two indistinguishable on the wire.
	bodyLen = 1 + devicePubLen + TxIDLen + addrLen

	// innerLen is the padded plaintext: the body and its signature.
	innerLen = bodyLen + sigLen

	// PacketLen is the fixed on-wire size: version, nonce, ciphertext, tag.
	PacketLen = 1 + chacha20poly1305.NonceSizeX + innerLen + chacha20poly1305.Overhead
)

// version 2 carries the signature. Bumped rather than relying on the length
// change, so a node meeting an older one gets "unsupported disco version" —
// which says what happened — instead of a size mismatch, which says nothing.
// Both directions fail: this is a flag day, and ADR-029 takes that deliberately
// while every mesh in existence belongs to the people making the change.
const version byte = 2

// ErrSignature is a packet whose contents do not match the device it names.
//
// Distinct so a caller can count it apart from the ordinary undecryptable
// traffic of a shared shard: one is somebody else's application, the other is a
// member of this mesh writing a name that is not theirs.
var ErrSignature = errors.New("disco packet is not signed by the device it names")

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

// EncodePing builds an encrypted ping.
func EncodePing(k Key, priv ed25519.PrivateKey, tx TxID) ([]byte, error) {
	return seal(k, TypePing, priv, tx, netip.AddrPort{})
}

// EncodePong builds an encrypted pong echoing the observed source address.
func EncodePong(k Key, priv ed25519.PrivateKey, tx TxID, observed netip.AddrPort) ([]byte, error) {
	return seal(k, TypePong, priv, tx, observed)
}

// seal builds the fixed-size encrypted packet. A ping and a pong differ only
// in plaintext, so they are indistinguishable on the wire.
func seal(k Key, t Type, priv ed25519.PrivateKey, tx TxID, observed netip.AddrPort) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("device private key is the wrong size")
	}
	senderPub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || len(senderPub) != devicePubLen {
		return nil, errors.New("device key has no usable public half")
	}

	inner := make([]byte, 0, innerLen)
	inner = append(inner, byte(t))
	inner = append(inner, senderPub...)
	inner = append(inner, tx[:]...)
	inner = append(inner, encodeAddr(observed)...) // zeroed for a ping

	// Signed inside the ciphertext, like the announce signature: the signature
	// is not visible to anyone who cannot already decrypt, so it adds nothing
	// for a bystander to correlate.
	inner = append(inner, ed25519.Sign(priv, inner)...)

	return sealInner(k, inner)
}

// sealInner encrypts a finished plaintext.
//
// Split out of seal so a test can build a packet the encoder would never
// produce — one signed by the wrong key, or edited after signing — which is
// exactly the thing the signature exists to refuse, and cannot be exercised
// through an API that always signs correctly.
func sealInner(k Key, inner []byte) ([]byte, error) {
	if len(inner) != innerLen {
		return nil, fmt.Errorf("plaintext is %d bytes, want %d", len(inner), innerLen)
	}
	aead, err := chacha20poly1305.NewX(k[:])
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	// The version byte is authenticated as additional data rather than
	// encrypted, so a receiver can reject a wrong version without a key.
	out := make([]byte, 0, PacketLen)
	out = append(out, version)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, inner, []byte{version}), nil
}

// openInner decrypts a packet without interpreting it. The counterpart of
// sealInner, and used for the same reason.
func openInner(k Key, pkt []byte) ([]byte, error) {
	if len(pkt) != PacketLen {
		return nil, fmt.Errorf("packet is %d bytes, want %d", len(pkt), PacketLen)
	}
	aead, err := chacha20poly1305.NewX(k[:])
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	nonce := pkt[1 : 1+aead.NonceSize()]
	return aead.Open(nil, nonce, pkt[1+aead.NonceSize():], []byte{version})
}

// Decode decrypts and validates a disco packet.
func Decode(k Key, pkt []byte) (*Message, error) {
	if len(pkt) != PacketLen {
		return nil, fmt.Errorf("packet is %d bytes, want %d", len(pkt), PacketLen)
	}
	if pkt[0] != version {
		return nil, fmt.Errorf("unsupported disco version %d", pkt[0])
	}

	aead, err := chacha20poly1305.NewX(k[:])
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	nonce := pkt[1 : 1+aead.NonceSize()]
	ct := pkt[1+aead.NonceSize():]

	inner, err := aead.Open(nil, nonce, ct, []byte{version})
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	if len(inner) != innerLen {
		return nil, fmt.Errorf("plaintext is %d bytes, want %d", len(inner), innerLen)
	}

	m := &Message{Type: Type(inner[0])}
	copy(m.SenderPub[:], inner[1:1+devicePubLen])
	copy(m.TxID[:], inner[1+devicePubLen:1+devicePubLen+TxIDLen])

	// Before anything in it is believed. Holding the disco key proves only
	// membership, which every member has; this is what proves the packet came
	// from the device it names, and it is the whole point of ADR-029. Checked
	// here rather than by the caller so there is no path that reads a field
	// without it.
	if !ed25519.Verify(ed25519.PublicKey(m.SenderPub[:]), inner[:bodyLen], inner[bodyLen:]) {
		return nil, ErrSignature
	}

	switch m.Type {
	case TypePing:
		// The address slot is padding on a ping; ignore whatever it holds.
	case TypePong:
		// Bounded at bodyLen: the signature follows the address, and slicing to
		// the end handed decodeAddr 82 bytes where it wanted 18.
		ap, err := decodeAddr(inner[1+devicePubLen+TxIDLen : bodyLen])
		if err != nil {
			return nil, err
		}
		m.Observed = ap
	default:
		return nil, fmt.Errorf("unknown disco type %d", m.Type)
	}
	return m, nil
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
