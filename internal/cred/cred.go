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
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// MeshIDLen is how much of the admin key's hash names a mesh. 16 bytes is far
// beyond collision by accident and short enough to read aloud once.
const MeshIDLen = 16

// DefaultLife is how long an issued credential is valid unless the admin says
// otherwise.
//
// The lifetime is a real trade and it is the admin's to make, per device:
// shorter means a suppressed revocation dies sooner, longer means a device that
// was switched off does not lock itself out. Thirty days suits a personal mesh
// where a phone can be in a drawer for a fortnight; a always-on VPS could take
// far longer, and a device you are unsure about far less.
//
// It lives in the signature, so it is chosen at issue and cannot be changed by
// the holder — a device able to configure its own expiry could extend its own
// membership, which is the whole of revocation undone. Nothing on the verifying
// side is configurable for the same reason.
const DefaultLife = 30 * 24 * time.Hour

// RenewBefore is how long before expiry a device should ask for a new
// credential, when there is something to ask.
//
// A third of the lifetime, so a device that is offline for two of every three
// renewal windows still refreshes before it lapses.
func RenewBefore(life time.Duration) time.Duration { return life / 3 }

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

// Authority is the set of keys a mesh trusts to sign membership.
//
// A set rather than one key, decided at mint and never afterwards, because the
// mesh id commits to it and the address prefix derives from the mesh id.
// Adding a key later would change every address on every node. ADR-018 puts it
// plainly: keeping a second key from the start is cheap to do now and
// impossible to retrofit.
//
// Two keys earn their place immediately. One is recovery — losing the only
// admin key means never enrolling or revoking again. The other is the renewal
// key that lets credentials refresh without the root being present, which is
// the only way an offline root and hands-off renewal can both be true.
//
// With a Keycard these are derivation paths on one card rather than separate
// artefacts, so "two keys" costs a path, not a second device.
type Authority struct {
	Keys []ed25519.PublicKey
}

// NewAuthority orders the keys canonically, so the same set always names the
// same mesh however it was assembled.
func NewAuthority(keys ...ed25519.PublicKey) (*Authority, error) {
	if len(keys) == 0 {
		return nil, errors.New("a mesh needs at least one admin key")
	}
	out := make([]ed25519.PublicKey, 0, len(keys))
	for _, k := range keys {
		if !knownKeySize(len(k)) {
			return nil, fmt.Errorf("admin key is %d bytes, want %d (ed25519) or %d (secp256k1)",
				len(k), ed25519.PublicKeySize, secp256k1PubKeySize)
		}
		out = append(out, append(ed25519.PublicKey(nil), k...))
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	for i := 1; i < len(out); i++ {
		if bytes.Equal(out[i], out[i-1]) {
			return nil, errors.New("the same admin key was given twice")
		}
	}
	return &Authority{Keys: out}, nil
}

// ID names the mesh: a hash over every trusted key, in canonical order.
func (a *Authority) ID() MeshID {
	h := sha256.New()
	h.Write([]byte("mesh/v2/id"))
	for _, k := range a.Keys {
		h.Write(k)
	}
	var id MeshID
	copy(id[:], h.Sum(nil)[:MeshIDLen])
	return id
}

// Trusts reports whether a key may sign membership for this mesh.
func (a *Authority) Trusts(pub ed25519.PublicKey) bool {
	for _, k := range a.Keys {
		if bytes.Equal(k, pub) {
			return true
		}
	}
	return false
}

// MeshOf names the mesh governed by exactly one key. A convenience for the
// single-key case; the mesh id is the same as NewAuthority(pub).ID().
func MeshOf(pub ed25519.PublicKey) MeshID {
	a, err := NewAuthority(pub)
	if err != nil {
		return MeshID{}
	}
	return a.ID()
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
	MeshID    MeshID
	DevicePub []byte // ed25519, 32 bytes
	WGPub     []byte // curve25519, 32 bytes
	Name      string
	Serial    uint64
	NotBefore int64 // unix seconds
	NotAfter  int64
	Sig       []byte // ed25519 over signedBytes()
}

// Wire format. Version 1:
//
//	1   version
//	16  mesh id
//	32  device public key
//	32  wireguard public key
//	8   serial            (big endian)
//	8   not before        (big endian, unix seconds)
//	8   not after
//	1   name length
//	n   name
//	64  signature         (over everything above)
//
// Binary rather than JSON for two reasons. Size: a credential has to ride an
// announce that is padded to a fixed size, and it is base64'd into JSON which
// is then base64'd again inside the envelope — the JSON form measured 364 bytes
// against a 320-byte budget, so it did not fit at all. And canonicality: what is
// signed is a byte layout with exactly one representation, rather than a JSON
// document whose stability depends on a marshaller's field ordering.
const (
	credVersion = 1
	credFixed   = 1 + MeshIDLen + 32 + 32 + 8 + 8 + 8 + 1
	sigLen      = ed25519.SignatureSize
	maxNameLen  = 64
)

// Digest is what is actually signed: SHA-256 over the canonical body.
//
// Signing a digest rather than the body is what lets the root live on a
// Keycard. A card signs a fixed-size input, and its algorithm is chosen per
// call (P2SignEdDSAEd25519, P2SignECDSA, ...) — so a 32-byte digest works
// whatever the card supports, and the body can be any length. It also keeps the
// signature scheme swappable without changing what is covered.
func (c *Credential) Digest() ([32]byte, error) {
	body, err := c.signedBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("shrooms/cred/v1"), body...)), nil
}

// signedBytes is everything the signature covers: the credential without it.
func (c *Credential) signedBytes() ([]byte, error) {
	if len(c.DevicePub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("device key is %d bytes, want %d", len(c.DevicePub), ed25519.PublicKeySize)
	}
	if len(c.WGPub) != 32 {
		return nil, fmt.Errorf("wireguard key is %d bytes, want 32", len(c.WGPub))
	}
	if len(c.Name) > maxNameLen {
		return nil, fmt.Errorf("name is %d bytes, over the %d limit", len(c.Name), maxNameLen)
	}

	b := make([]byte, 0, credFixed+len(c.Name))
	b = append(b, credVersion)
	b = append(b, c.MeshID[:]...)
	b = append(b, c.DevicePub...)
	b = append(b, c.WGPub...)
	b = binary.BigEndian.AppendUint64(b, c.Serial)
	b = binary.BigEndian.AppendUint64(b, uint64(c.NotBefore))
	b = binary.BigEndian.AppendUint64(b, uint64(c.NotAfter))
	b = append(b, byte(len(c.Name)))
	b = append(b, c.Name...)
	return b, nil
}

// MarshalBinary renders a credential for the wire.
func (c *Credential) MarshalBinary() ([]byte, error) {
	body, err := c.signedBytes()
	if err != nil {
		return nil, err
	}
	if len(c.Sig) != sigLen {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(c.Sig), sigLen)
	}
	return append(body, c.Sig...), nil
}

// UnmarshalCredential reads one, rejecting anything malformed.
//
// Every length is checked before it is used. These bytes arrive inside an
// announce from a mesh member, and a member may be hostile — the parse happens
// before anything has been verified, so until Verify returns nil this is
// attacker-controlled input.
func UnmarshalCredential(b []byte) (*Credential, error) {
	if len(b) < credFixed+sigLen {
		return nil, fmt.Errorf("credential is %d bytes, too short", len(b))
	}
	if b[0] != credVersion {
		return nil, fmt.Errorf("credential version %d is not supported", b[0])
	}
	c := &Credential{}
	i := 1
	copy(c.MeshID[:], b[i:i+MeshIDLen])
	i += MeshIDLen
	c.DevicePub = append([]byte(nil), b[i:i+32]...)
	i += 32
	c.WGPub = append([]byte(nil), b[i:i+32]...)
	i += 32
	c.Serial = binary.BigEndian.Uint64(b[i : i+8])
	i += 8
	c.NotBefore = int64(binary.BigEndian.Uint64(b[i : i+8]))
	i += 8
	c.NotAfter = int64(binary.BigEndian.Uint64(b[i : i+8]))
	i += 8
	n := int(b[i])
	i++
	if len(b) != i+n+sigLen {
		return nil, fmt.Errorf("credential length %d does not match its name length %d", len(b), n)
	}
	c.Name = string(b[i : i+n])
	i += n
	c.Sig = append([]byte(nil), b[i:]...)
	return c, nil
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
	d, err := c.Digest()
	if err != nil {
		return nil, err
	}
	c.Sig = ed25519.Sign(a.Priv, d[:])
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

// VerifyBy checks a credential against every key a mesh trusts.
//
// Any trusted key may have signed it: the root that enrolled the device, or a
// renewal key that extended it. Which one signed is not interesting to a
// verifier — that it was one of them, and that the credential names this mesh,
// is the whole question.
func VerifyBy(auth *Authority, c *Credential, now time.Time) error {
	if auth == nil || len(auth.Keys) == 0 {
		return errors.New("no admin keys to verify against")
	}
	if c == nil {
		return errors.New("no credential")
	}
	d, err := c.Digest()
	if err != nil {
		return fmt.Errorf("credential is malformed: %w", err)
	}

	signed := false
	for _, k := range auth.Keys {
		if verifyKey(k, d[:], c.Sig) {
			signed = true
			break
		}
	}
	if !signed {
		return ErrBadSignature
	}
	if c.MeshID != auth.ID() {
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

// Verify checks a credential against an admin public key and a clock.
//
// The order matters: the signature first, then everything else. Reporting
// "expired" for a forged credential would tell an attacker their forgery was
// otherwise acceptable.
func Verify(adminPub ed25519.PublicKey, c *Credential, now time.Time) error {
	if c == nil {
		return errors.New("no credential")
	}
	d, err := c.Digest()
	if err != nil {
		return fmt.Errorf("credential is malformed: %w", err)
	}
	if !verifyKey(adminPub, d[:], c.Sig) {
		return ErrBadSignature
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
	MeshID    MeshID
	DevicePub []byte
	Serial    uint64
	Issued    int64
	Sig       []byte
}

// Wire format, version 1: version, mesh id, device key, serial, issued,
// signature. Binary for the same reasons as the credential.
const revFixed = 1 + MeshIDLen + 32 + 8 + 8

func (r *Revocation) signedBytes() ([]byte, error) {
	if len(r.DevicePub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("device key is %d bytes, want %d", len(r.DevicePub), ed25519.PublicKeySize)
	}
	b := make([]byte, 0, revFixed)
	b = append(b, credVersion)
	b = append(b, r.MeshID[:]...)
	b = append(b, r.DevicePub...)
	b = binary.BigEndian.AppendUint64(b, r.Serial)
	b = binary.BigEndian.AppendUint64(b, uint64(r.Issued))
	return b, nil
}

// Digest is what is signed, for the same reason as a credential's.
func (r *Revocation) Digest() ([32]byte, error) {
	body, err := r.signedBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("shrooms/revoke/v1"), body...)), nil
}

// MarshalBinary renders a revocation for the wire.
func (r *Revocation) MarshalBinary() ([]byte, error) {
	body, err := r.signedBytes()
	if err != nil {
		return nil, err
	}
	if len(r.Sig) != sigLen {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(r.Sig), sigLen)
	}
	return append(body, r.Sig...), nil
}

// UnmarshalRevocation reads one, checking every length first.
func UnmarshalRevocation(b []byte) (*Revocation, error) {
	if len(b) != revFixed+sigLen {
		return nil, fmt.Errorf("revocation is %d bytes, want %d", len(b), revFixed+sigLen)
	}
	if b[0] != credVersion {
		return nil, fmt.Errorf("revocation version %d is not supported", b[0])
	}
	r := &Revocation{}
	i := 1
	copy(r.MeshID[:], b[i:i+MeshIDLen])
	i += MeshIDLen
	r.DevicePub = append([]byte(nil), b[i:i+32]...)
	i += 32
	r.Serial = binary.BigEndian.Uint64(b[i : i+8])
	i += 8
	r.Issued = int64(binary.BigEndian.Uint64(b[i : i+8]))
	i += 8
	r.Sig = append([]byte(nil), b[i:]...)
	return r, nil
}

// Revoke signs a withdrawal of one credential.
func (a *Admin) Revoke(devicePub []byte, serial uint64, now time.Time) (*Revocation, error) {
	r := &Revocation{
		MeshID:    MeshOf(a.Pub),
		DevicePub: append([]byte(nil), devicePub...),
		Serial:    serial,
		Issued:    now.Unix(),
	}
	d, err := r.Digest()
	if err != nil {
		return nil, err
	}
	r.Sig = ed25519.Sign(a.Priv, d[:])
	return r, nil
}

// VerifyRevocationBy checks a revocation against every key a mesh trusts.
func VerifyRevocationBy(auth *Authority, r *Revocation) error {
	if auth == nil || len(auth.Keys) == 0 {
		return errors.New("no admin keys to verify against")
	}
	if r == nil {
		return errors.New("no revocation")
	}
	d, err := r.Digest()
	if err != nil {
		return fmt.Errorf("revocation is malformed: %w", err)
	}
	for _, k := range auth.Keys {
		if verifyKey(k, d[:], r.Sig) {
			if r.MeshID != auth.ID() {
				return ErrWrongMesh
			}
			return nil
		}
	}
	return ErrBadSignature
}

// VerifyRevocation checks a revocation is genuine.
func VerifyRevocation(adminPub ed25519.PublicKey, r *Revocation) error {
	if r == nil {
		return errors.New("no revocation")
	}
	d, err := r.Digest()
	if err != nil {
		return fmt.Errorf("revocation is malformed: %w", err)
	}
	if !verifyKey(adminPub, d[:], r.Sig) {
		return ErrBadSignature
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
