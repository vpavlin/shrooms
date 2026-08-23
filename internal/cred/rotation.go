package cred

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Rotation announces a new announce generation.
//
// It is a statement, not a secret. That is the point: the admin key is meant to
// live offline and increasingly on a card (ADR-022), and a card can sign a
// digest. Asking it to perform key agreement against every member of a mesh
// would tie every rotation to having the card in hand, so the admin authorises
// the change and members distribute the secret it names.
//
// What it commits to is the hash of the generation secret. A member handed a
// secret checks it against this before accepting it, so a rogue member cannot
// inject a generation of its own choosing — the worst it can do is stay silent,
// and any other member will serve.
//
// Being public is safe and is why it can be replayed harmlessly: knowing
// H(S_N) does not yield S_N, and a device that cannot be given S_N by a member
// gains nothing from holding the statement.
type Rotation struct {
	MeshID MeshID

	// Generation is N: which generation this names. Strictly increasing per
	// mesh, and never reused — a node stores the highest it holds a secret for
	// and refuses anything lower, so an old statement cannot walk a mesh back
	// to a generation a revoked device can still read.
	Generation uint64

	// Commit is SHA-256 over the generation secret.
	Commit []byte

	// Serial is the revocation this rotation enforces, or zero when no single
	// revocation prompted it.
	//
	// Signed and carried so that a member can refuse to serve a generation
	// while its own revocation list is behind: a device that was offline during
	// the revoke comes back, is rekeyed by a peer, and would otherwise answer
	// the revoked device's request with the new secret — having checked a list
	// that does not yet contain it.
	Serial uint64

	Issued int64 // unix seconds
	Sig    []byte
}

const (
	rotVersion   byte = 1
	rotCommitLen      = sha256.Size
	rotFixed          = 1 + MeshIDLen + 8 + rotCommitLen + 8 + 8
)

// RotationCommit is what a Rotation commits to.
func RotationCommit(secret []byte) []byte {
	sum := sha256.Sum256(append([]byte("shrooms/rotate/secret/v1"), secret...))
	return sum[:]
}

// Commits reports whether a secret is the one this rotation names.
//
// Constant time, because this runs on a secret an untrusted member handed us
// and the comparison would otherwise leak how much of a guess was right.
func (r *Rotation) Commits(secret []byte) bool {
	if r == nil || len(secret) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(RotationCommit(secret), r.Commit) == 1
}

func (r *Rotation) signedBytes() ([]byte, error) {
	if len(r.Commit) != rotCommitLen {
		return nil, fmt.Errorf("commitment is %d bytes, want %d", len(r.Commit), rotCommitLen)
	}
	if r.Generation == 0 {
		return nil, errors.New("generation zero is the un-rotated mesh and cannot be announced")
	}
	b := make([]byte, 0, rotFixed)
	b = append(b, rotVersion)
	b = append(b, r.MeshID[:]...)
	b = binary.BigEndian.AppendUint64(b, r.Generation)
	b = append(b, r.Commit...)
	b = binary.BigEndian.AppendUint64(b, r.Serial)
	b = binary.BigEndian.AppendUint64(b, uint64(r.Issued))
	return b, nil
}

// Digest is what is signed, for the same reason as a credential's: a card signs
// a fixed-size input whatever its algorithm.
func (r *Rotation) Digest() ([32]byte, error) {
	body, err := r.signedBytes()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("shrooms/rotate/v1"), body...)), nil
}

// MarshalBinary renders a rotation for the wire.
func (r *Rotation) MarshalBinary() ([]byte, error) {
	body, err := r.signedBytes()
	if err != nil {
		return nil, err
	}
	if len(r.Sig) != sigLen {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(r.Sig), sigLen)
	}
	return append(body, r.Sig...), nil
}

// UnmarshalRotation reads one, checking every length before using it. These
// bytes arrive from a mesh member, and a member may be hostile.
func UnmarshalRotation(b []byte) (*Rotation, error) {
	if len(b) != rotFixed+sigLen {
		return nil, fmt.Errorf("rotation is %d bytes, want %d", len(b), rotFixed+sigLen)
	}
	if b[0] != rotVersion {
		return nil, fmt.Errorf("rotation version %d is not supported", b[0])
	}
	r := &Rotation{}
	i := 1
	copy(r.MeshID[:], b[i:i+MeshIDLen])
	i += MeshIDLen
	r.Generation = binary.BigEndian.Uint64(b[i : i+8])
	i += 8
	r.Commit = append([]byte(nil), b[i:i+rotCommitLen]...)
	i += rotCommitLen
	r.Serial = binary.BigEndian.Uint64(b[i : i+8])
	i += 8
	r.Issued = int64(binary.BigEndian.Uint64(b[i : i+8]))
	i += 8
	r.Sig = append([]byte(nil), b[i:]...)
	if r.Generation == 0 {
		return nil, errors.New("rotation names generation zero")
	}
	return r, nil
}

// VerifyRotationBy checks a rotation against a mesh's authority.
//
// The mesh id first, then the signature. A rotation for another mesh signed by
// that mesh's admin is perfectly valid and simply not ours, and accepting it
// would let anyone who runs a mesh move ours.
func VerifyRotationBy(auth *Authority, r *Rotation) error {
	if auth == nil {
		return errors.New("no authority")
	}
	if r == nil {
		return errors.New("no rotation")
	}
	if r.MeshID != auth.ID() {
		return ErrWrongMesh
	}
	d, err := r.Digest()
	if err != nil {
		return fmt.Errorf("rotation is malformed: %w", err)
	}
	for _, k := range auth.Keys {
		if verifyKey(k, d[:], r.Sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// RotateWith signs a rotation through the Signer seam, so the admin key can be
// a file or a card.
func RotateWith(s Signer, auth *Authority, generation, serial uint64,
	secret []byte, now time.Time) (*Rotation, error) {

	if s == nil {
		return nil, errors.New("no signer")
	}
	if auth == nil {
		return nil, errors.New("no authority to rotate against")
	}
	if len(secret) == 0 {
		return nil, errors.New("no generation secret")
	}
	r := &Rotation{
		MeshID:     auth.ID(),
		Generation: generation,
		Commit:     RotationCommit(secret),
		Serial:     serial,
		Issued:     now.Unix(),
	}
	d, err := r.Digest()
	if err != nil {
		return nil, err
	}
	if r.Sig, err = s.SignDigest(d); err != nil {
		return nil, err
	}
	// Verified before it leaves, for the same reason issuance is: a card can
	// return something well-formed and wrong, and finding out later means
	// finding out when the mesh has already half-moved.
	if err := VerifyRotationBy(auth, r); err != nil {
		return nil, fmt.Errorf("the signer produced a rotation this mesh will not accept: %w", err)
	}
	return r, nil
}
