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

	// Boot is this device's delivery multiaddr, published so peers can
	// bootstrap their rendezvous connection from it later (ADR-031).
	//
	// Sent only by a Core node that is also a relay: relay already means
	// publicly reachable, and Core means it carries gossip, so it is both
	// dialable and worth dialing. An Edge node behind a NAT is neither.
	//
	// Carried on the ordinary announce for the same reason Relay is — this is
	// a property of a peer, and peers are already found this way. The receiver
	// keeps it for its *next* start rather than using it now: bootstrap
	// addresses are consumed when the delivery node is constructed and there is
	// no way to add one to a running node.
	Boot string `json:"boot,omitempty"`
}

// compactMarker introduces the compact envelope framing.
//
// 0x00 can never begin the legacy framing, which is JSON and therefore always
// starts with '{'. So one byte distinguishes the two with no version
// negotiation and no ambiguity.
const compactMarker = 0x00

// emitCompact selects the framing SENDERS use. Readers accept both regardless.
//
// Off until every node in the field can read the compact form. This is the
// PaddedSizes manoeuvre again — ship tolerant readers, then move the senders —
// and it matters more here: a node that cannot parse an announce goes deaf, and
// a phone updates through an app store on its own schedule.
//
// Flip this once everything is updated. It is worth flipping: the legacy
// framing is JSON around a body that is itself JSON, so Go base64-encodes the
// body a second time on the way out and the signature with it. That costs about
// 250 bytes on a full announce - a quarter of the 1024-byte budget, paid for
// nothing, and taken out of the endpoints a node can advertise.
var emitCompact = false

// encodeEnvelope frames a signed body for padding and encryption.
//
// Compact framing is marker ‖ signature ‖ body. The signature is fixed at
// ed25519.SignatureSize, so the split needs no length prefix.
func encodeEnvelope(body, sig []byte) ([]byte, error) {
	if !emitCompact {
		return json.Marshal(envelope{Body: body, Sig: sig})
	}
	out := make([]byte, 0, 1+len(sig)+len(body))
	out = append(out, compactMarker)
	out = append(out, sig...)
	return append(out, body...), nil
}

// decodeEnvelope reads either framing.
func decodeEnvelope(plain []byte) (body, sig []byte, err error) {
	if len(plain) > 0 && plain[0] == compactMarker {
		if len(plain) < 1+ed25519.SignatureSize {
			return nil, nil, errors.New("compact envelope is truncated")
		}
		return plain[1+ed25519.SignatureSize:], plain[1 : 1+ed25519.SignatureSize], nil
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env.Body, env.Sig, nil
}

// envelope is what actually gets signed and encrypted.
type envelope struct {
	Body []byte `json:"b"` // canonical JSON of the message
	Sig  []byte `json:"s"` // ed25519 over Body
}

// epochKey derives the payload key for an epoch.
//
// Rotation buys unlinkability, not forward secrecy, and the two are easy to
// confuse — this comment used to claim the second. It is a pure function of the
// long-lived network key, nothing is deleted, and every node holds that key
// permanently, so anyone who obtains it recomputes every epoch key that has
// ever existed or ever will. What rotation does give is that a captured
// ciphertext cannot be tied to an epoch's topic without the key, and that each
// epoch's traffic is a separate keystream.
//
// Real forward secrecy here would mean a ratchet — k(n+1) = H(k(n)) with k(n)
// destroyed — which turns a derived key into held state: a device that loses it
// could no longer rejoin from the network key alone, enrolment would have to
// carry the ratchet position, and two nodes at different positions would fail
// to talk in the way that looks exactly like a healthy mesh delivering nothing.
// ADR-020 accepts that trade deliberately.
//
// Worth keeping in proportion: what is under this key is control-plane
// metadata — announces, revocations, grants, service lists. The traffic itself
// already has forward secrecy from WireGuard, which does ephemeral ECDH per
// handshake and rekeys every couple of minutes without involving this key at
// all. The gap is retrospective metadata, against somebody who by then can
// decrypt everything current and join the mesh outright.
func epochKey(nk identity.NetworkKey, epoch int64, gen []byte) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(epoch))
	// The generation secret is mixed into the key material, not into the label:
	// it is a key, not a name. At generation zero there is none, and the input
	// is byte-for-byte what it has always been — which is what lets a mesh that
	// has never rotated read and write exactly as before.
	ikm := nk.PayloadKey()
	if len(gen) > 0 {
		ikm = append(append([]byte(nil), ikm...), gen...)
	}
	r := hkdf.New(sha256.New, ikm, nil, append([]byte("mesh/v1/epoch"), b[:]...))
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
// Keyring is the material a control message is sealed under: the network key,
// and the generation secret in force.
//
// They travel together because they are used together, and separating them is
// how one of them gets forgotten at a call site. The generation is nil until a
// mesh has rotated, and a nil generation IS generation zero — the derivation
// then reduces byte-for-byte to what it was before generations existed, which
// is what lets this ship without a flag day.
//
// The network key alone still derives the rendezvous TOPIC. That is deliberate,
// and it is why this type does not simply replace the network key everywhere: a
// device that missed a rotation has to keep finding the place where the rekey
// it needs is published.
type Keyring struct {
	nk  identity.NetworkKey
	gen []byte
}

// NewKeyring pairs a network key with the generation secret in force, if any.
func NewKeyring(nk identity.NetworkKey, gen []byte) Keyring {
	return Keyring{nk: nk, gen: append([]byte(nil), gen...)}
}

// NetworkKey is the key the rendezvous topic still derives from.
func (k Keyring) NetworkKey() identity.NetworkKey { return k.nk }

func Seal(nk identity.NetworkKey, epoch int64, priv ed25519.PrivateKey, msg any) ([]byte, error) {
	return NewKeyring(nk, nil).Seal(epoch, priv, msg)
}

func (k Keyring) Seal(epoch int64, priv ed25519.PrivateKey, msg any) ([]byte, error) {
	var err error
	for _, size := range PaddedSizes {
		var out []byte
		out, err = k.sealPadded(epoch, priv, msg, size)
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
func (k Keyring) sealPadded(epoch int64, priv ed25519.PrivateKey, msg any, size int) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	plain, err := encodeEnvelope(body, ed25519.Sign(priv, body))
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

	aead, err := chacha20poly1305.NewX(epochKey(k.nk, epoch, k.gen))
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
	return NewKeyring(nk, nil).OpenAnnounce(epoch, raw, now)
}

func (k Keyring) OpenAnnounce(epoch int64, raw []byte, now time.Time) (*Announce, error) {
	plain, err := k.open(epoch, raw)
	if err != nil {
		return nil, err
	}

	envBody, envSig, err := decodeEnvelope(plain)
	if err != nil {
		return nil, err
	}

	var a Announce
	if err := json.Unmarshal(envBody, &a); err != nil {
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
	if !ed25519.Verify(ed25519.PublicKey(a.DevicePub), envBody, envSig) {
		return nil, errors.New("signature verification failed")
	}

	if skew := now.Sub(time.Unix(a.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &a, nil
}

// open decrypts and strips padding.
func (k Keyring) open(epoch int64, raw []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(epochKey(k.nk, epoch, k.gen))
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
	return NewKeyring(nk, nil).OpenRevoke(epoch, raw, now)
}

func (k Keyring) OpenRevoke(epoch int64, raw []byte, now time.Time) (*Revoke, error) {
	plain, err := k.open(epoch, raw)
	if err != nil {
		return nil, err
	}
	envBody, envSig, err := decodeEnvelope(plain)
	if err != nil {
		return nil, err
	}
	var r Revoke
	if err := json.Unmarshal(envBody, &r); err != nil {
		return nil, fmt.Errorf("unmarshal revoke: %w", err)
	}
	if r.Kind != KindRevoke {
		return nil, fmt.Errorf("unexpected kind %q", r.Kind)
	}
	if len(r.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(r.DevicePub), envBody, envSig) {
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
	return NewKeyring(nk, nil).OpenGrant(epoch, raw, now)
}

func (k Keyring) OpenGrant(epoch int64, raw []byte, now time.Time) (*Grant, error) {
	plain, err := k.open(epoch, raw)
	if err != nil {
		return nil, err
	}
	envBody, envSig, err := decodeEnvelope(plain)
	if err != nil {
		return nil, err
	}
	var g Grant
	if err := json.Unmarshal(envBody, &g); err != nil {
		return nil, fmt.Errorf("unmarshal grant: %w", err)
	}
	if g.Kind != KindGrant {
		return nil, fmt.Errorf("unexpected kind %q", g.Kind)
	}
	if len(g.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(g.DevicePub), envBody, envSig) {
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

	// Bound is what is listening on this device's mesh address, as
	// "name:port" (ADR-026). Separate from Names because they are different
	// claims: a name in Names is forwarded by this device and reached as
	// <name>.<device>.mesh, while one here is a port on the device's own
	// address, reached as <device>.mesh:<port>. Rendering either as the other
	// prints an address that does not work.
	Bound []string `json:"bound,omitempty"`

	// Types names what some of the entries in Names actually ARE, as
	// "name=type" — "backup=logos-storage".
	//
	// A separate field rather than widening Names, because an older reader
	// takes each entry in Names as a DNS label: it would sanitise
	// "backup=logos-storage" into something that resolves to nothing, and the
	// service would disappear for every peer that had not been updated.
	//
	// Sparse. A service has a type only if one was declared, and most will not
	// have one.
	Types []string `json:"types,omitempty"`

	Timestamp int64 `json:"ts"`
}

// OpenServices reads a service list.
//
// The signature proves which member sent it, and that is the whole check
// available here: a service name is self-asserted, exactly like a device name
// (ADR-008). The caller is expected to ignore lists from devices it has not
// already admitted, because this package does not know who those are.
func OpenServices(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Services, error) {
	return NewKeyring(nk, nil).OpenServices(epoch, raw, now)
}

func (k Keyring) OpenServices(epoch int64, raw []byte, now time.Time) (*Services, error) {
	plain, err := k.open(epoch, raw)
	if err != nil {
		return nil, err
	}
	envBody, envSig, err := decodeEnvelope(plain)
	if err != nil {
		return nil, err
	}
	var sv Services
	if err := json.Unmarshal(envBody, &sv); err != nil {
		return nil, fmt.Errorf("unmarshal services: %w", err)
	}
	if sv.Kind != KindServices {
		return nil, fmt.Errorf("unexpected kind %q", sv.Kind)
	}
	if len(sv.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(sv.DevicePub), envBody, envSig) {
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
	return NewKeyring(nk, nil).OpenAnnounceWindow(epochs, raw, now)
}

func (k Keyring) OpenAnnounceWindow(epochs []int64, raw []byte, now time.Time) (*Announce, error) {
	var lastErr error
	for _, e := range epochs {
		a, err := k.OpenAnnounce(e, raw, now)
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
