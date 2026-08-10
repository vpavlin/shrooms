// Package cred separates authority from participation.
//
// Today one secret does everything (ADR-008): the network key derives the
// rendezvous topics, the payload key, the pairwise PSKs and the address prefix,
// so every device must hold it — and holding it is what membership *is*. That
// conflation is the flaw, not the sharing. A leak from any device compromises
// the mesh, revocation means rotating for everyone, and every member can enrol
// members.
//
// Here membership is instead an admin-signed credential naming one device, and
// the mesh's identity is the admin's PUBLIC key. The decisive property is that
// the admin key never has to be on a participating device: it is needed only to
// enrol and to revoke, so it can live offline, on one laptop or a hardware key.
// See ADR-018.
//
// This package is deliberately self-contained: keys, credentials, and the
// checks over them, with no wire format and no I/O. It changes nothing that
// runs until something calls it.
package cred

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// MeshIDLen is how much of the admin key's hash names a mesh. 16 bytes is far
// beyond collision by accident and short enough to read aloud once.
const MeshIDLen = 16

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Admin is the authority for one mesh. The private half enrols and revokes;
// the public half IS the mesh's identity and is not a secret.
type Admin struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
}

// NewAdmin mints a mesh.
func NewAdmin() (*Admin, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate admin key: %w", err)
	}
	return &Admin{Priv: priv, Pub: pub}, nil
}

// MeshID names a mesh: the hash of the admin public key.
//
// Public on purpose. Topics and the address prefix derive from it rather than
// from a secret, so a device can compute where to look before it holds
// anything at all — which is what makes enrolment possible without first
// sharing a secret.
type MeshID [MeshIDLen]byte

// Mesh returns the identity of the mesh this key governs.
func MeshOf(pub ed25519.PublicKey) MeshID {
	sum := sha256.Sum256(append([]byte("mesh/v2/id"), pub...))
	var id MeshID
	copy(id[:], sum[:MeshIDLen])
	return id
}

func (m MeshID) String() string { return b32.EncodeToString(m[:]) }

// ParseMeshID reads the printed form.
func ParseMeshID(s string) (MeshID, error) {
	raw, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(s)))
	if err != nil {
		return MeshID{}, fmt.Errorf("decode mesh id: %w", err)
	}
	if len(raw) != MeshIDLen {
		return MeshID{}, fmt.Errorf("mesh id is %d bytes, want %d", len(raw), MeshIDLen)
	}
	var id MeshID
	copy(id[:], raw)
	return id, nil
}

// Prefix is the mesh's ULA prefix, derived from its public identity rather than
// from a secret — the same shape as ADR-008's, so addresses keep working the
// way they do today.
func (m MeshID) Prefix() netip.Prefix {
	sum := sha256.Sum256(append([]byte("mesh/v2/ula"), m[:]...))
	var a [16]byte
	a[0] = 0xfd
	copy(a[1:6], sum[:5])
	return netip.PrefixFrom(netip.AddrFrom16(a), 48)
}

// Credential is one device's membership, signed by the admin.
//
// It is not secret. It proves the admin admitted this device, and it says until
// when — which is the part that matters, because a gossip bus lets an attacker
// suppress a revocation it cannot forge. Expiry is what bounds that; gossiped
// revocation is only the fast path.
type Credential struct {
	MeshID    MeshID `json:"mesh"`
	DevicePub []byte `json:"device"` // ed25519, 32 bytes
	WGPub     []byte `json:"wg"`     // curve25519, 32 bytes
	Name      string `json:"name"`
	Serial    uint64 `json:"serial"`
	NotBefore int64  `json:"nbf"` // unix seconds
	NotAfter  int64  `json:"exp"`
	Sig       []byte `json:"sig"` // ed25519 over the body below
}

// body is exactly what is signed: the credential without its signature.
// Marshalled from a separate type rather than by blanking a field, so a field
// added later cannot silently fall outside the signature.
type body struct {
	MeshID    MeshID `json:"mesh"`
	DevicePub []byte `json:"device"`
	WGPub     []byte `json:"wg"`
	Name      string `json:"name"`
	Serial    uint64 `json:"serial"`
	NotBefore int64  `json:"nbf"`
	NotAfter  int64  `json:"exp"`
}

func (c *Credential) body() body {
	return body{
		MeshID: c.MeshID, DevicePub: c.DevicePub, WGPub: c.WGPub,
		Name: c.Name, Serial: c.Serial, NotBefore: c.NotBefore, NotAfter: c.NotAfter,
	}
}

// Issue signs a credential for one device.
func (a *Admin) Issue(devicePub, wgPub []byte, name string, serial uint64, now time.Time, life time.Duration) (*Credential, error) {
	if len(devicePub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("device key is %d bytes, want %d", len(devicePub), ed25519.PublicKeySize)
	}
	if len(wgPub) != 32 {
		return nil, fmt.Errorf("wireguard key is %d bytes, want 32", len(wgPub))
	}
	if life <= 0 {
		return nil, errors.New("a credential with no lifetime is already expired")
	}

	c := &Credential{
		MeshID:    MeshOf(a.Pub),
		DevicePub: append([]byte(nil), devicePub...),
		WGPub:     append([]byte(nil), wgPub...),
		Name:      name,
		Serial:    serial,
		// A minute of slack, because clocks differ and a credential that is not
		// yet valid on the machine it was just issued to is a confusing failure.
		NotBefore: now.Add(-time.Minute).Unix(),
		NotAfter:  now.Add(life).Unix(),
	}
	raw, err := json.Marshal(c.body())
	if err != nil {
		return nil, fmt.Errorf("marshal credential: %w", err)
	}
	c.Sig = ed25519.Sign(a.Priv, raw)
	return c, nil
}

// Errors a caller may want to distinguish. A credential that is merely expired
// is a renewal; one that fails its signature is an attack or a bug.
var (
	ErrBadSignature = errors.New("credential signature does not verify")
	ErrWrongMesh    = errors.New("credential is for another mesh")
	ErrExpired      = errors.New("credential has expired")
	ErrNotYetValid  = errors.New("credential is not valid yet")
	ErrRevoked      = errors.New("credential has been revoked")
)

// Verify checks a credential against an admin public key and a clock.
//
// The order matters: the signature first, then everything else. Reporting
// "expired" for a forged credential would tell an attacker their forgery was
// otherwise acceptable.
func Verify(adminPub ed25519.PublicKey, c *Credential, now time.Time) error {
	if c == nil {
		return errors.New("no credential")
	}
	raw, err := json.Marshal(c.body())
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	if !ed25519.Verify(adminPub, raw, c.Sig) {
		return ErrBadSignature
	}
	if c.MeshID != MeshOf(adminPub) {
		return ErrWrongMesh
	}
	if now.Unix() < c.NotBefore {
		return ErrNotYetValid
	}
	if now.Unix() >= c.NotAfter {
		return ErrExpired
	}
	return nil
}

// Revocation withdraws a device before its credential expires.
//
// The serial rather than the whole credential, so a revocation is small enough
// to gossip and to keep. NotBefore exists so a revocation cannot be replayed to
// undo a later re-enrolment of the same device.
type Revocation struct {
	MeshID    MeshID `json:"mesh"`
	DevicePub []byte `json:"device"`
	Serial    uint64 `json:"serial"`
	Issued    int64  `json:"issued"`
	Sig       []byte `json:"sig"`
}

type revBody struct {
	MeshID    MeshID `json:"mesh"`
	DevicePub []byte `json:"device"`
	Serial    uint64 `json:"serial"`
	Issued    int64  `json:"issued"`
}

func (r *Revocation) body() revBody {
	return revBody{MeshID: r.MeshID, DevicePub: r.DevicePub, Serial: r.Serial, Issued: r.Issued}
}

// Revoke signs a withdrawal of one credential.
func (a *Admin) Revoke(devicePub []byte, serial uint64, now time.Time) (*Revocation, error) {
	r := &Revocation{
		MeshID:    MeshOf(a.Pub),
		DevicePub: append([]byte(nil), devicePub...),
		Serial:    serial,
		Issued:    now.Unix(),
	}
	raw, err := json.Marshal(r.body())
	if err != nil {
		return nil, fmt.Errorf("marshal revocation: %w", err)
	}
	r.Sig = ed25519.Sign(a.Priv, raw)
	return r, nil
}

// VerifyRevocation checks a revocation is genuine.
func VerifyRevocation(adminPub ed25519.PublicKey, r *Revocation) error {
	if r == nil {
		return errors.New("no revocation")
	}
	raw, err := json.Marshal(r.body())
	if err != nil {
		return fmt.Errorf("marshal revocation: %w", err)
	}
	if !ed25519.Verify(adminPub, raw, r.Sig) {
		return ErrBadSignature
	}
	if r.MeshID != MeshOf(adminPub) {
		return ErrWrongMesh
	}
	return nil
}

// Revokes reports whether this revocation withdraws the given credential.
//
// Matched on device AND serial: a device that is re-enrolled gets a higher
// serial, and an old revocation must not withdraw the new credential — which is
// the replay this design has to survive, since anyone can rebroadcast one.
func (r *Revocation) Revokes(c *Credential) bool {
	if r == nil || c == nil {
		return false
	}
	if len(r.DevicePub) != len(c.DevicePub) {
		return false
	}
	for i := range r.DevicePub {
		if r.DevicePub[i] != c.DevicePub[i] {
			return false
		}
	}
	return r.Serial >= c.Serial
}
