package cred

import (
	"crypto/ed25519"
	"errors"
)

// The seam a smartcard needs (ADR-022).
//
// Everything this package signs is a 32-byte digest — SHA-256 over a domain
// string and the body — and that was chosen before any card was in hand
// precisely so this seam could exist. A card signs a fixed-size input and
// picks its algorithm per call, so a digest works whatever the card supports
// and the body may be any length.
//
// Above this line nothing changes: issuing, revoking and renewing already work
// in terms of a digest, and the authority is a set of public keys, so where the
// private half lives is invisible to every peer and needs no migration.

// Signer is something that can sign on behalf of the mesh's authority.
//
// Two implementations are expected: an admin key held in a file, which is what
// exists today, and a Keycard, which is why this is an interface at all.
type Signer interface {
	// Public is the key peers verify against, and the one written into
	// admin_keys at mint time. A card will not hand over the private half, so
	// this must be readable without signing anything.
	Public() ed25519.PublicKey

	// SignDigest signs a 32-byte digest.
	//
	// An error rather than a panic because a card can fail in ways a file
	// cannot: unplugged mid-operation, wrong PIN, pairing lost. Those are
	// ordinary outcomes here and the caller has to be able to say so.
	SignDigest(d [32]byte) ([]byte, error)
}

// Public satisfies Signer for a key held in memory.
func (a *Admin) Public() ed25519.PublicKey { return a.Pub }

// SignDigest satisfies Signer for a key held in memory.
//
// The error is always nil, and the signature exists so that a caller written
// against the interface behaves identically whichever implementation it has —
// the file case must not be the one that never fails, or the card case becomes
// a path nothing else exercises.
func (a *Admin) SignDigest(d [32]byte) ([]byte, error) {
	if len(a.Priv) != ed25519.PrivateKeySize {
		return nil, errors.New("no admin private key in memory")
	}
	return ed25519.Sign(a.Priv, d[:]), nil
}

// IssueWith signs a credential with any Signer.
//
// The same as Admin.Issue except for where the signature comes from, and it is
// what the admin tooling calls: Issue stays for the tests and callers that
// already hold a key in memory.
func IssueWith(s Signer, devicePub, wgPub, sealPub []byte, name string, serial uint64,
	now int64, notAfter int64, meshID MeshID) (*Credential, error) {

	if s == nil {
		return nil, errors.New("no signer")
	}
	c := &Credential{
		MeshID:    meshID,
		DevicePub: append([]byte(nil), devicePub...),
		WGPub:     append([]byte(nil), wgPub...),
		SealPub:   append([]byte(nil), sealPub...),
		Name:      name,
		Serial:    serial,
		NotBefore: now,
		NotAfter:  notAfter,
	}
	d, err := c.Digest()
	if err != nil {
		return nil, err
	}
	if c.Sig, err = s.SignDigest(d); err != nil {
		return nil, err
	}
	return c, nil
}

// SignRevocationWith signs a withdrawal with any Signer, for the same reason.
func SignRevocationWith(s Signer, r *Revocation) error {
	if s == nil {
		return errors.New("no signer")
	}
	d, err := r.Digest()
	if err != nil {
		return err
	}
	r.Sig, err = s.SignDigest(d)
	if err != nil {
		return err
	}
	// Checked against the key that just signed it, the way IssueFor checks a
	// credential. Issuing had this and revoking did not, which is the wrong way
	// round: a credential that does not verify is a device that cannot join,
	// noticed within minutes, while a revocation that does not verify is a
	// device that was never removed — and nothing says so, because every peer
	// simply discards it.
	//
	// Not academic. keycard-go returns a signature two bytes short of what the
	// card sent, repaired in the card signer; anything that produces a
	// malformed signature would otherwise reach the bus looking authoritative.
	if !VerifyDigest(s.Public(), d, r.Sig) {
		return errors.New("the signer produced a revocation that does not verify " +
			"against its own key, so no peer would act on it")
	}
	return nil
}
