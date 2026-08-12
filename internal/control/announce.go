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

	"github.com/vpavlin/shrooms/internal/identity"
)

// PaddedSize is the fixed plaintext size of every control message. Announces
// are a few hundred bytes; padding to a constant removes message-length as a
// signal.
const PaddedSize = 512

// PaddedSizes are the plaintext sizes a reader accepts.
//
// Senders use PaddedSize; readers take any of these, so the sending size can
// move without a flag day. 1024 is here ahead of need, so that by the time
// credentials require it every node in the field already accepts it.
var PaddedSizes = []int{512, 1024}

func knownPadding(n int) bool {
	for _, s := range PaddedSizes {
		if n == s {
			return true
		}
	}
	return false
}

// MaxClockSkew bounds how far a message's timestamp may be from local time.
// Beyond this the message is rejected regardless of signature, which limits how
// long a captured message stays replayable.
const MaxClockSkew = 2 * time.Hour

// ErrTooLarge means the message does not fit the fixed padding.
//
// Distinguishable so a caller can shrink and retry rather than give up: a node
// with several addresses would otherwise stop announcing entirely, which is
// indistinguishable from being offline.
var ErrTooLarge = errors.New("message exceeds the padded size")

// Kind identifies a control message type.
type Kind string

const (
	KindAnnounce Kind = "announce"
	KindRelay    Kind = "relay"
	KindRevoke   Kind = "revoke"
	KindGrant    Kind = "grant"
	KindServices Kind = "services"
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

	// Fresh marks an announce sent shortly after the sender started, and asks
	// peers to announce themselves in reply.
	//
	// A restarted node has an empty roster and must otherwise wait out every
	// peer's announce interval to rebuild it — measured at 19.3s against 5.4s
	// in the other direction. It cannot be helped by peers noticing anything,
	// because nothing about it looks new to them: they have had it in their
	// rosters throughout, and its sequence number persists across restarts. The
	// sender is the only party that knows, so it has to say so.
	Fresh bool `json:"fresh,omitempty"`

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
// Seal pads to the smallest known size the message fits.
//
// Not a fixed size any more, because a credential (ADR-018) is ~364 bytes and
// nothing else fits beside it in 512. Readers accept every size in PaddedSizes,
// so a node that carries a credential simply uses the next one up and older
// nodes still read it — which is what makes credentials deployable without a
// flag day.
//
// The cost is honest and worth stating: length now distinguishes an announce
// carrying a credential from one that is not. Within a size the padding is
// still constant, so "came online" and "changed address" remain
// indistinguishable, which is what the padding was for.
func Seal(nk identity.NetworkKey, epoch int64, priv ed25519.PrivateKey, msg any) ([]byte, error) {
	var err error
	for _, size := range PaddedSizes {
		var out []byte
		out, err = sealPadded(nk, epoch, priv, msg, size)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, ErrTooLarge) {
			return nil, err
		}
	}
	return nil, err
}

// sealPadded seals to a specific plaintext size. Exists so the sending size can
// be moved deliberately, and so tests can prove a reader accepts each size
// rather than only the one compiled in today.
func sealPadded(nk identity.NetworkKey, epoch int64, priv ed25519.PrivateKey, msg any, size int) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	env := envelope{Body: body, Sig: ed25519.Sign(priv, body)}
	plain, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	if len(plain) > size-2 {
		return nil, fmt.Errorf("%w: message is %d bytes, exceeds padded size %d",
			ErrTooLarge, len(plain), size)
	}

	// 2-byte big-endian length, then the envelope, then zero padding.
	padded := make([]byte, size)
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
	// Any size we know about, not just the one we send. The reader used to
	// demand exactly PaddedSize, which made changing that constant a flag day:
	// old and new nodes could not read each other at all, so a mesh would have
	// to be upgraded everywhere simultaneously — including a phone that updates
	// through an app store on its own schedule.
	//
	// Accepting a set instead turns the change into two ordinary steps: ship
	// tolerant readers everywhere, then move the senders. Growing the padding
	// is needed for admin-signed credentials (ADR-018), which do not fit in 512
	// bytes with anything else.
	if !knownPadding(len(padded)) {
		return nil, fmt.Errorf("plaintext is %d bytes, want one of %v", len(padded), PaddedSizes)
	}

	n := int(binary.BigEndian.Uint16(padded[:2]))
	if n > len(padded)-2 {
		return nil, errors.New("declared length exceeds padding")
	}
	return padded[2 : 2+n], nil
}

// Revoke carries an admin-signed withdrawal to every node.
//
// A separate message rather than a field on the announce: an announce is
// padded to a fixed size and already carries a credential, and a node that has
// nothing to revoke should not pay for the space. Sealed under the same epoch
// key and published to the same topic, so it reaches exactly the nodes that
// could act on it and nobody else.
//
// The payload is verified against the mesh's admin keys by the receiver, so
// this envelope's own signature only says which member relayed it — anyone may
// pass a revocation on, and that is the point of gossiping one.
type Revoke struct {
	Kind      Kind   `json:"kind"`
	DevicePub []byte `json:"device_pub"` // the relayer, not the revoked device
	Payload   []byte `json:"payload"`    // cred.Revocation, wire form
	Timestamp int64  `json:"ts"`
}

// OpenRevoke reads a revocation message. The withdrawal inside is not checked
// here: only its mesh's authority can do that, and this package does not know
// it.
func OpenRevoke(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Revoke, error) {
	plain, err := open(nk, epoch, raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	var r Revoke
	if err := json.Unmarshal(env.Body, &r); err != nil {
		return nil, fmt.Errorf("unmarshal revoke: %w", err)
	}
	if r.Kind != KindRevoke {
		return nil, fmt.Errorf("unexpected kind %q", r.Kind)
	}
	if len(r.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(r.DevicePub), env.Body, env.Sig) {
		return nil, errors.New("signature verification failed")
	}
	if skew := now.Sub(time.Unix(r.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &r, nil
}

// Grant carries an admin-signed credential towards the device it names.
//
// The mirror of Revoke, and for the same reason: credentials expire (ADR-018),
// so every device needs a new one before its thirty days are up, and the admin
// is not on every device. The admin signs one and hands it to any node; the
// mesh carries it to whoever it names.
//
// Safe to relay for the same reason a revocation is: a credential is public,
// it says nothing secret, and it is worthless to anyone but the device whose
// keys it names. A hostile relayer can drop one — which is indistinguishable
// from being offline, and ends in the same place expiry already does — but it
// cannot forge one or make a device accept another's.
type Grant struct {
	Kind      Kind   `json:"kind"`
	DevicePub []byte `json:"device_pub"` // the relayer, not the device named
	Payload   []byte `json:"payload"`    // cred.Credential, wire form
	Timestamp int64  `json:"ts"`
}

// OpenGrant reads a renewal message. The credential inside is not checked
// here, exactly as OpenRevoke does not check the withdrawal: only the mesh's
// authority can, and this package does not know it.
func OpenGrant(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Grant, error) {
	plain, err := open(nk, epoch, raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	var g Grant
	if err := json.Unmarshal(env.Body, &g); err != nil {
		return nil, fmt.Errorf("unmarshal grant: %w", err)
	}
	if g.Kind != KindGrant {
		return nil, fmt.Errorf("unexpected kind %q", g.Kind)
	}
	if len(g.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(g.DevicePub), env.Body, env.Sig) {
		return nil, errors.New("signature verification failed")
	}
	if skew := now.Sub(time.Unix(g.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &g, nil
}

// Services is what a device offers, by name (ADR-023).
//
// Its own message rather than a field on the announce, for the same reason a
// revocation is: an announce is padded to a fixed size and already carries a
// credential, so a service list would compete for that space and be trimmed
// exactly when there is most to say. A separate message lets the list be
// trimmed on its own and keeps the announce small.
//
// Names only. What port a service is on is the publishing node's business —
// the name router and the address are what a peer uses — and a list of open
// ports is the part of this that would actually be worth having if you were
// nosy.
type Services struct {
	Kind      Kind     `json:"kind"`
	DevicePub []byte   `json:"device_pub"`
	Names     []string `json:"names"`
	Timestamp int64    `json:"ts"`
}

// OpenServices reads a service list.
//
// The signature proves which member sent it, and that is the whole check
// available here: a service name is self-asserted, exactly like a device name
// (ADR-008). The caller is expected to ignore lists from devices it has not
// already admitted, because this package does not know who those are.
func OpenServices(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Services, error) {
	plain, err := open(nk, epoch, raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	var sv Services
	if err := json.Unmarshal(env.Body, &sv); err != nil {
		return nil, fmt.Errorf("unmarshal services: %w", err)
	}
	if sv.Kind != KindServices {
		return nil, fmt.Errorf("unexpected kind %q", sv.Kind)
	}
	if len(sv.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(sv.DevicePub), env.Body, env.Sig) {
		return nil, errors.New("signature verification failed")
	}
	if skew := now.Sub(time.Unix(sv.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &sv, nil
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
