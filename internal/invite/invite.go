// Package invite implements one-time enrolment tokens (ADR-017).
//
// Enrolling a device used to mean handing it the network key: read aloud,
// pasted into a chat, photographed off a screen — and permanent, because the
// key is the mesh. An invite replaces that with a 26-character token that is
// good for one device and fifteen minutes.
//
// The token is the whole of the protocol. Both sides derive from it where to
// listen and what to encrypt with, so a wrong token addresses a topic nobody
// answers rather than a challenge an attacker can grind against. What comes
// back over that topic is the network key, the admin keys and a credential
// issued to the joining device — everything it needs and nothing it can use to
// enrol anyone else.
//
// This package is crypto and wire format only: no Waku, no files, no clock
// beyond what it is handed. The commands do the I/O.
package invite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/shrooms/internal/topic"
)

// SecretLen is 128 bits: 26 base32 characters, which is typable, and far beyond
// guessing. There is nothing to guess against in any case — a wrong token
// derives a topic nobody is subscribed to.
const SecretLen = 16

// DefaultTTL is how long an invite stays open. Short because the token is the
// authorisation: whoever holds it can join, so the window is the exposure.
const DefaultTTL = 15 * time.Minute

// MaxSkew bounds how stale a message may be. Tighter than the control plane's
// two hours, because both parties are present and talking.
const MaxSkew = 5 * time.Minute

// paddedSize is the plaintext size of every invite message, so a request and a
// response are the same size on the bus. Both fit comfortably: a response
// carries a 32-byte key, two admin keys and a ~364-byte credential.
const paddedSize = 1024

// b32 matches the network key's encoding: uppercase, unpadded, no ambiguity
// about case when it is read off a screen.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// A Secret is the invite token in its raw form.
type Secret [SecretLen]byte

// New mints a token.
func New() (Secret, error) {
	var s Secret
	_, err := rand.Read(s[:])
	return s, err
}

// String renders the token for a human to type or a QR code to carry.
func (s Secret) String() string { return b32.EncodeToString(s[:]) }

// Parse reads a token back, forgiving the things people do to a string they
// have retyped: lowercase it, and break it up with spaces or dashes.
func Parse(text string) (Secret, error) {
	var s Secret
	clean := strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "").Replace(strings.TrimSpace(text)))
	raw, err := b32.DecodeString(clean)
	if err != nil {
		return s, fmt.Errorf("that is not an invite token: %w", err)
	}
	if len(raw) != SecretLen {
		return s, fmt.Errorf("an invite token is %d characters, this one is %d",
			b32.EncodedLen(SecretLen), len(clean))
	}
	copy(s[:], raw)
	return s, nil
}

// derive is the token's key schedule. Everything is domain-separated from
// everything else, so the topic — which is public by construction, since it is
// what we subscribe to — says nothing about the payload key.
func (s Secret) derive(label string, n int) []byte {
	r := hkdf.New(sha256.New, s[:], nil, []byte(label))
	out := make([]byte, n)
	if _, err := r.Read(out); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return out
}

// Topic is where the two sides meet.
//
// Built with the same application and version fields as the rendezvous topics
// (ADR-006) so it lands on the same shard: the joining device is talking to a
// fleet it has never seen, and putting the invite on a shard the mesh's own
// traffic already uses means no extra subscription and no distinguishing
// pattern.
func (s Secret) Topic() string {
	tag := s.derive("invite/v1/topic", 16)
	return fmt.Sprintf("/%s/%s/%s/%s", topic.Application, topic.Version,
		strings.ToLower(b32.EncodeToString(tag)), topic.Encoding)
}

func (s Secret) payloadKey() []byte {
	return s.derive("invite/v1/payload", chacha20poly1305.KeySize)
}

// Scheme is the URI an invite QR code carries.
//
// A URI rather than a bare token so a phone can tell an invite from any other
// text it might scan, and from the network-key invitations `key show --qr`
// still produces. Deliberately the same scheme as those: it is the same app
// being handed the same kind of thing, distinguished by the host part.
//
// Kept equal to state.InviteScheme by a test in that package rather than by
// importing it, so this package stays free of config and file handling.
const Scheme = "shrooms"

// LegacyScheme is what invites said before the project was renamed.
//
// Read forever, written never. An invite is redeemed by whatever the other
// person happens to have installed, and a device a version behind is the normal
// case rather than an edge one — during the rename it is *every* device. The
// cost of accepting it is one string comparison; the cost of not is an invite
// that fails with "that is not an invite" on a phone somebody updated last week.
const LegacyScheme = "logosvpn"

// URI renders a token as something scannable.
func (s Secret) URI() string { return s.URIWithBoot("") }

// URIWithBoot renders a token that also carries somewhere to bootstrap from
// (ADR-031).
//
// One address, not a list. The device needs somewhere to start exactly once:
// the moment it is on the mesh, it learns every Core relay's address from
// announces and keeps them. Carrying three would buy redundancy the mesh
// supplies for free thirty seconds later, and each one costs about ninety
// characters of a QR code somebody has to scan across a table.
//
// A query parameter because the token already is a URI, so a reader that does
// not know the key ignores it and falls back to the public fleet — which is
// what every reader did before this existed.
func (s Secret) URIWithBoot(boot string) string {
	u := Scheme + "://enrol?token=" + s.String()
	if boot != "" {
		u += "&boot=" + url.QueryEscape(boot)
	}
	return u
}

// BootFromToken returns the bootstrap address a token carries, or "".
//
// Never an error: a token that carries nothing readable here is still a valid
// token, and refusing to enrol because a hint was malformed would trade the
// whole feature for a garbled one.
func BootFromToken(text string) string {
	text = strings.TrimSpace(text)
	for _, scheme := range []string{Scheme, LegacyScheme} {
		rest, ok := strings.CutPrefix(text, scheme+"://")
		if !ok {
			continue
		}
		_, query, _ := strings.Cut(rest, "?")
		for _, kv := range strings.Split(query, "&") {
			if v, ok := strings.CutPrefix(kv, "boot="); ok {
				dec, err := url.QueryUnescape(v)
				if err != nil {
					return ""
				}
				return dec
			}
		}
	}
	return ""
}

// ParseToken accepts either URI form, the grouped form, or a bare token.
//
// One implementation, because the CLI writes these and the phone reads them,
// and a second parser is how the two drift.
func ParseToken(text string) (Secret, error) {
	text = strings.TrimSpace(text)
	for _, scheme := range []string{Scheme, LegacyScheme} {
		rest, ok := strings.CutPrefix(text, scheme+"://")
		if !ok {
			continue
		}
		_, query, _ := strings.Cut(rest, "?")
		for _, kv := range strings.Split(query, "&") {
			if tok, ok := strings.CutPrefix(kv, "token="); ok {
				return Parse(tok)
			}
		}
		return Secret{}, errors.New("that invitation carries no token")
	}
	return Parse(text)
}

// Kinds of invite message.
const (
	KindRequest  = "invite-request"
	KindResponse = "invite-response"
)

// Request is the joining device asking to be let in. It carries the keys the
// credential will name, so the inviter can issue one without a second round
// trip.
type Request struct {
	Kind      string `json:"kind"`
	DevicePub []byte `json:"device_pub"` // ed25519, 32 bytes
	WGPub     []byte `json:"wg_pub"`     // curve25519, 32 bytes
	Name      string `json:"name"`

	// EphPub is a fresh X25519 key, used once. The response is sealed to it, so
	// that a second holder of the token cannot read what came back for the
	// device that actually asked. A fresh key rather than the tunnel key: the
	// WireGuard static key is a Curve25519 key too and would work, but reusing
	// it across two protocols buys nothing and has to be argued about.
	EphPub []byte `json:"eph_pub"`

	// Deferred asks the holder to answer with the mesh and hold the credential
	// back for a second request (ADR-017).
	//
	// A device cannot know which mesh it is joining until the response arrives,
	// so it cannot derive the identity it will use there
	// ([ADR-015](015-multiple-meshes-one-daemon.md)) before it asks. Without
	// this it sends its base identity, the credential names that, and a device
	// on two invited meshes presents the same key to both — which anyone in
	// both can see, and which is exactly the linkability per-mesh identities
	// exist to prevent.
	//
	// The keys are still sent alongside it, so a holder that predates this
	// field issues a credential for them and the exchange completes the old
	// way rather than failing.
	Deferred bool `json:"deferred,omitempty"`

	Timestamp int64 `json:"ts"`
}

// Response is what admission looks like: the mesh's secret, the keys that say
// who may admit devices, and this device's own credential.
type Response struct {
	Kind       string   `json:"kind"`
	NetworkKey []byte   `json:"nk"`               // 32 bytes
	AdminKeys  [][]byte `json:"admin_keys"`       // ed25519, may be empty
	Credential []byte   `json:"cred,omitempty"`   // signed for the requesting device
	Suffix     string   `json:"suffix,omitempty"` // the mesh's DNS suffix
	Timestamp  int64    `json:"ts"`
}

// SealRequest encrypts a request under the token.
func SealRequest(s Secret, r *Request) ([]byte, error) {
	r.Kind = KindRequest
	return sealPadded(s.payloadKey(), r)
}

// OpenRequest decrypts and sanity-checks a request.
//
// Everything here is attacker-controlled until it has been opened, and mostly
// still is afterwards: holding the token is what the sender has proved, which
// is exactly the property ADR-017 says an invite has and no more.
func OpenRequest(s Secret, blob []byte, now time.Time) (*Request, error) {
	var r Request
	if err := openPadded(s.payloadKey(), blob, &r); err != nil {
		return nil, err
	}
	if r.Kind != KindRequest {
		return nil, fmt.Errorf("expected an invite request, got %q", r.Kind)
	}
	if len(r.DevicePub) != 32 || len(r.WGPub) != 32 || len(r.EphPub) != 32 {
		return nil, errors.New("invite request has a malformed key")
	}
	if err := checkSkew(r.Timestamp, now); err != nil {
		return nil, err
	}
	return &r, nil
}

// SealResponse encrypts a response so that only the device that sent the
// request can open it, and only someone holding the token can even see that
// much.
//
// Two layers, deliberately. The inner one is an ephemeral-static ECDH to the
// requester's key: it is what makes the response single-recipient. The outer
// one is the token, and it is what keeps the bus from seeing the shape of an
// enrolment at all.
func SealResponse(s Secret, reqEphPub []byte, r *Response) ([]byte, error) {
	if len(reqEphPub) != 32 {
		return nil, errors.New("the request has no usable ephemeral key")
	}
	r.Kind = KindResponse

	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	var ephPriv [32]byte
	if _, err := rand.Read(ephPriv[:]); err != nil {
		return nil, err
	}
	ephPub, err := curve25519.X25519(ephPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	key, err := responseKey(ephPriv[:], reqEphPub, ephPub, reqEphPub)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	inner := make([]byte, 0, 32+len(nonce)+len(body)+aead.Overhead())
	inner = append(inner, ephPub...)
	inner = append(inner, nonce...)
	inner = aead.Seal(inner, nonce, body, nil)

	return sealPaddedRaw(s.payloadKey(), inner)
}

// OpenResponse peels both layers.
func OpenResponse(s Secret, ephPriv []byte, blob []byte, now time.Time) (*Response, error) {
	inner, err := openPaddedRaw(s.payloadKey(), blob)
	if err != nil {
		return nil, err
	}
	aeadSize := chacha20poly1305.NonceSizeX
	if len(inner) < 32+aeadSize {
		return nil, errors.New("invite response is truncated")
	}
	senderPub, rest := inner[:32], inner[32:]
	nonce, ct := rest[:aeadSize], rest[aeadSize:]

	// The transcript binds both ephemeral keys, so the recipient needs its own
	// public half as well as the one it was sent.
	ownPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	key, err := responseKey(ephPriv, senderPub, senderPub, ownPub)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	body, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("invite response was not for this device")
	}

	var r Response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Kind != KindResponse {
		return nil, fmt.Errorf("expected an invite response, got %q", r.Kind)
	}
	if len(r.NetworkKey) != 32 {
		return nil, errors.New("invite response carries no network key")
	}
	if err := checkSkew(r.Timestamp, now); err != nil {
		return nil, err
	}
	return &r, nil
}

// NewEphemeral returns the X25519 pair a request advertises.
func NewEphemeral() (priv, pub []byte, err error) {
	priv = make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return nil, nil, err
	}
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// responseKey hashes the shared secret together with both ephemeral public
// keys, so the key is bound to this exact pair rather than to a raw DH output.
//
// senderPub and recipientPub are named by role, not by who is calling: both
// sides must feed them in the same order or they derive different keys.
func responseKey(priv, dhPub, senderPub, recipientPub []byte) ([]byte, error) {
	shared, err := curve25519.X25519(priv, dhPub)
	if err != nil {
		return nil, fmt.Errorf("invite key exchange failed: %w", err)
	}
	info := append([]byte("invite/v1/response"), senderPub...)
	info = append(info, recipientPub...)
	r := hkdf.New(sha256.New, shared, nil, info)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func checkSkew(ts int64, now time.Time) error {
	d := now.Sub(time.Unix(ts, 0))
	if d < 0 {
		d = -d
	}
	if d > MaxSkew {
		return fmt.Errorf("invite message is %s out of date; check the clocks", d.Round(time.Second))
	}
	return nil
}

// sealPadded marshals a message, pads it to a constant and encrypts it.
func sealPadded(key []byte, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return sealPaddedRaw(key, body)
}

func sealPaddedRaw(key, body []byte) ([]byte, error) {
	if len(body)+4 > paddedSize {
		return nil, fmt.Errorf("invite message is %d bytes, over the %d limit", len(body), paddedSize-4)
	}
	plain := make([]byte, paddedSize)
	binary.BigEndian.PutUint32(plain, uint32(len(body)))
	copy(plain[4:], body)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func openPadded(key, blob []byte, v any) error {
	body, err := openPaddedRaw(key, blob)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func openPaddedRaw(key, blob []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize()+paddedSize+aead.Overhead() {
		return nil, errors.New("not an invite message")
	}
	nonce, ct := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		// Every message on the topic is tried against the token, including
		// other people's. "Not for us" and "tampered with" are the same event.
		return nil, errors.New("invite message does not open with this token")
	}
	if len(plain) != paddedSize {
		return nil, errors.New("invite message has the wrong padding")
	}
	n := binary.BigEndian.Uint32(plain)
	if int(n) > paddedSize-4 {
		return nil, errors.New("invite message declares an impossible length")
	}
	return plain[4 : 4+n], nil
}
