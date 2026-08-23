package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/shrooms/internal/identity"
)

// KindRekey carries a generation secret to one device.
const KindRekey Kind = "rekey"

// Rekey hands one member the generation secret, sealed to that member alone.
//
// Published once per epoch by every member that has the secret, one message per
// member of the roster. A dozen small messages an hour against the eighty
// announces an hour each device already sends.
//
// A standing statement rather than an event, which is the lesson
// republishRevocations already records: a device that wakes up waits at most an
// epoch and finds its own envelope waiting. Nothing has to notice it is behind,
// it does not have to ask, and there is therefore no request for anyone to
// replay. An earlier design had stragglers ask, and a revoked device — which
// holds the network key — could copy one genuine request and have every member
// answer it, turning one message into as many as the mesh has members.
type Rekey struct {
	Kind Kind `json:"kind"`

	// DevicePub is the member that published this, whose signature covers the
	// body. Not who it is for: that is To.
	DevicePub []byte `json:"device_pub"`

	// Rotation is the admin-signed statement (cred.Rotation, wire form) naming
	// the generation and committing to its secret. Carried with the secret so a
	// recipient can check the two against each other without having to have
	// heard the statement separately.
	Rotation []byte `json:"rot"`

	// To is the recipient's sealing key, from its credential. In the clear so a
	// member can tell at a glance whether an envelope is for it without trying
	// to open every one — and it is a public key belonging to a device whose
	// credential is already on the wire, so it discloses nothing new.
	To []byte `json:"to"`

	// EphPub is a fresh X25519 key for this envelope. Fresh rather than the
	// sender's static, so that compromising a member later does not open the
	// rekeys it sent before.
	EphPub []byte `json:"eph"`

	Box       []byte `json:"box"` // the generation secret, sealed to To
	Timestamp int64  `json:"ts"`
}

// rekeyBoxKey derives the key one envelope is sealed under.
//
// Both public keys go into the info, so a box cannot be lifted out of one
// envelope and replayed inside another addressed elsewhere.
func rekeyBoxKey(priv, peerPub, ephPub, toPub []byte) ([]byte, error) {
	shared, err := curve25519.X25519(priv, peerPub)
	if err != nil {
		return nil, fmt.Errorf("rekey key exchange failed: %w", err)
	}
	info := append([]byte("mesh/v1/rekey"), ephPub...)
	info = append(info, toPub...)
	r := hkdf.New(sha256.New, shared, nil, info)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// SealRekey builds one envelope, for one recipient.
//
// The OUTER message is sealed at generation zero — the network key alone —
// because the whole point is to reach a device that does not have the current
// generation. Sealing it under the generation it delivers would be a lock whose
// key is inside it. The secret is protected by the inner box, not by the outer
// envelope, so a holder of the network key sees that a rekey went past and
// cannot read it.
func SealRekey(nk identity.NetworkKey, epoch int64, priv ed25519.PrivateKey,
	rotation, toSealPub, secret []byte, now time.Time) ([]byte, error) {

	if len(toSealPub) != 32 {
		return nil, fmt.Errorf("recipient sealing key is %d bytes, want 32", len(toSealPub))
	}
	if len(secret) == 0 {
		return nil, errors.New("no generation secret to seal")
	}
	var ephPriv [32]byte
	if _, err := rand.Read(ephPriv[:]); err != nil {
		return nil, err
	}
	ephPub, err := curve25519.X25519(ephPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	key, err := rekeyBoxKey(ephPriv[:], toSealPub, ephPub, toSealPub)
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

	return NewKeyring(nk, nil).Seal(epoch, priv, &Rekey{
		Kind:      KindRekey,
		DevicePub: priv.Public().(ed25519.PublicKey),
		Rotation:  rotation,
		To:        append([]byte(nil), toSealPub...),
		EphPub:    ephPub,
		Box:       aead.Seal(nonce, nonce, secret, nil),
		Timestamp: now.Unix(),
	})
}

// OpenRekey reads the envelope. It does NOT open the box — see Unseal.
func OpenRekey(nk identity.NetworkKey, epoch int64, raw []byte, now time.Time) (*Rekey, error) {
	plain, err := NewKeyring(nk, nil).open(epoch, raw)
	if err != nil {
		return nil, err
	}
	envBody, envSig, err := decodeEnvelope(plain)
	if err != nil {
		return nil, err
	}
	var r Rekey
	if err := json.Unmarshal(envBody, &r); err != nil {
		return nil, fmt.Errorf("unmarshal rekey: %w", err)
	}
	if r.Kind != KindRekey {
		return nil, fmt.Errorf("unexpected kind %q", r.Kind)
	}
	if len(r.DevicePub) != ed25519.PublicKeySize {
		return nil, errors.New("bad device public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(r.DevicePub), envBody, envSig) {
		return nil, errors.New("signature verification failed")
	}
	if len(r.To) != 32 || len(r.EphPub) != 32 {
		return nil, errors.New("bad key length in rekey")
	}
	if len(r.Box) == 0 {
		return nil, errors.New("rekey carries no sealed secret")
	}
	if skew := now.Sub(time.Unix(r.Timestamp, 0)); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, fmt.Errorf("timestamp skew %s exceeds %s", skew.Round(time.Second), MaxClockSkew)
	}
	return &r, nil
}

// For reports whether this envelope is addressed to a device's sealing key.
func (r *Rekey) For(sealPub identity.WGKey) bool {
	return r != nil && len(r.To) == 32 && string(r.To) == string(sealPub[:])
}

// Unseal opens the box with the recipient's sealing private key.
//
// The secret it returns is UNVERIFIED: anyone can address an envelope to this
// device and put anything inside it. The caller must check it against the
// commitment in an admin-signed rotation before believing it — that check, not
// this one, is what stops a member injecting a generation of its own.
func (r *Rekey) Unseal(sealPriv identity.WGKey) ([]byte, error) {
	if r == nil {
		return nil, errors.New("no rekey")
	}
	key, err := rekeyBoxKey(sealPriv[:], r.EphPub, r.EphPub, r.To)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(r.Box) < aead.NonceSize() {
		return nil, errors.New("sealed secret is truncated")
	}
	nonce, ct := r.Box[:aead.NonceSize()], r.Box[aead.NonceSize():]
	secret, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("could not open the sealed secret")
	}
	return secret, nil
}
