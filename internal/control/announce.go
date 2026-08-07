// Package control defines the mesh control-plane messages carried over Waku.
//
// Every message is signed by the publishing device and encrypted under a
// per-epoch key derived from the network key. The signature lives INSIDE the
// ciphertext: Waku relay uses StrictNoSign at the libp2p layer to preserve what
// weak sender anonymity exists, and signing outside would undo that.
//
// Messages are padded to a fixed size so that "device came online" and "device
// changed IP" are indistinguishable from steady-state heartbeats on the wire.
package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// PaddedSize is the fixed plaintext size of every control message. Announces
// are a few hundred bytes; padding to a constant removes message-length as a
// signal.
const PaddedSize = 512

// MaxClockSkew bounds how far a message's timestamp may be from local time.
// Beyond this the message is rejected regardless of signature, which limits how
// long a captured message stays replayable.
const MaxClockSkew = 2 * time.Hour

// Kind identifies a control message type.
type Kind string

const (
	KindAnnounce Kind = "announce"
	KindRelay    Kind = "relay"
	KindRevoke   Kind = "revoke"
)

// Announce is a device advertising itself and its reachable endpoints.
type Announce struct {
	Kind      Kind     `json:"kind"`
	DevicePub []byte   `json:"device_pub"` // ed25519, 32 bytes
	WGPub     []byte   `json:"wg_pub"`     // curve25519, 32 bytes
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"` // candidate host:port, most-preferred first

	// Seq is strictly increasing per device. A public bus lets anyone replay a
	// captured message they cannot decrypt; without this an observer could roll
	// a peer's endpoint back to a stale address. This is the single cheapest
	// defence and the one most often omitted.
	Seq       uint64 `json:"seq"`
	Timestamp int64  `json:"ts"` // unix seconds

	// Relay says this device will forward traffic for peers that cannot reach
	// each other directly.
	//
	// Carried on the ordinary announce rather than as a separate message: a
	// relay is just a peer that is willing to forward, so it should be found
	// the same way. Discovery, endpoint validation and path probing then all
	// apply to it unchanged, and a relay is only used once packets have
	// demonstrably reached it.
	Relay bool `json:"relay,omitempty"`

	// Credential is empty in v1. Reserved so that adding admin-signed
	// credentials in M5 is a behaviour change rather than a wire-format break.
	Credential []byte `json:"cred,omitempty"`
}

// envelope is what actually gets signed and encrypted.
type envelope struct {
	Body []byte `json:"b"` // canonical JSON of the message
	Sig  []byte `json:"s"` // ed25519 over Body
}

// epochKey derives the payload key for an epoch. Rotating per epoch means a
// key compromise does not retroactively decrypt older traffic on the bus.
func epochKey(nk identity.NetworkKey, epoch int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(epoch))
	r := hkdf.New(sha256.New, nk.PayloadKey(), nil, append([]byte("mesh/v1/epoch"), b[:]...))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := r.Read(key); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return key
}

// Seal signs a message with the device key and encrypts it under the epoch key.
func Seal(nk identity.NetworkKey, epoch int64, priv ed25519.PrivateKey, msg any) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	env := envelope{Body: body, Sig: ed25519.Sign(priv, body)}
	plain, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	if len(plain) > PaddedSize-2 {
		return nil, fmt.Errorf("message is %d bytes, exceeds padded size %d", len(plain), PaddedSize)
	}

	// 2-byte big-endian length, then the envelope, then zero padding.
	padded := make([]byte, PaddedSize)
	binary.BigEndian.PutUint16(padded[:2], uint16(len(plain)))
	copy(padded[2:], plain)

	aead, err := chacha20poly1305.NewX(epochKey(nk, epoch))
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, padded, nil), nil
}

// OpenAnnounce decrypts, verifies and returns an Announce.
//
// It checks: the epoch key decrypts it (so the sender holds the network key),
// the signature verifies against the DevicePub inside the message (so the
// sender holds the device key), and the timestamp is within MaxClockSkew.
//
// It does NOT check Seq — that needs cross-message state; see ReplayGuard.
func OpenAnnounce(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Announce, error) {
	plain, err := open(nk, epoch, raw)
	if err != nil {
		return nil, err
	}

	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	var a Announce
	if err := json.Unmarshal(env.Body, &a); err != nil {
		return nil, fmt.Errorf("unmarshal announce: %w", err)
	}
	if a.Kind != KindAnnounce {
		return nil, fmt.Errorf("unexpected kind %q", a.Kind)
	}
	if len(a.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if len(a.WGPub) != 32 {
		return nil, errors.New("bad wireguard public key length")
	}

	// The signature is over the body, and the body names the key. So this
	// proves "the holder of DevicePub wrote this", which is what the overlay
	// address is derived from.
	if !ed25519.Verify(ed25519.PublicKey(a.DevicePub), env.Body, env.Sig) {
		return nil, errors.New("signature verification failed")
	}

	if skew := now.Sub(time.Unix(a.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &a, nil
}

// open decrypts and strips padding.
func open(nk identity.NetworkKey, epoch int64, raw []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(epochKey(nk, epoch))
	if err != nil {
		return nil, fmt.Errorf("aead: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]

	padded, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	if len(padded) != PaddedSize {
		return nil, fmt.Errorf("plaintext is %d bytes, want %d", len(padded), PaddedSize)
	}

	n := int(binary.BigEndian.Uint16(padded[:2]))
	if n > PaddedSize-2 {
		return nil, errors.New("declared length exceeds padding")
	}
	return padded[2 : 2+n], nil
}

// OpenAnnounceWindow tries each epoch in the acceptance window. Peers whose
// clocks differ will be publishing under a neighbouring epoch key.
func OpenAnnounceWindow(nk identity.NetworkKey, epochs []int64, raw []byte, now time.Time) (*Announce, error) {
	var lastErr error
	for _, e := range epochs {
		a, err := OpenAnnounce(nk, e, raw, now)
		if err == nil {
			return a, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no epochs supplied")
	}
	return nil, lastErr
}
