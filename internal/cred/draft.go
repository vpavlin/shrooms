package cred

import (
	"errors"
	"fmt"
	"time"
)

// Issuing in two halves, for a signer that is not in this process.
//
// IssueFor asks a Signer and waits. That suits a key in a file and a card on a
// reader this process opened; it does not suit Basecamp, where the card belongs
// to another module (keycard-basecamp) and the person approves on its screen,
// in their own time, while the daemon holds the invite open. So the credential
// is drafted here — the same fields, defaults and mesh id IssueFor would use —
// and finished here once the signature comes back, verified before it leaves.
// Nothing outside this package learns the wire format, which is the one thing
// worth not having written twice (ADR-050).

// Draft is the credential IssueFor would sign, unsigned, and the digest a
// signer has to sign for it.
func Draft(auth *Authority, devPub, wgPub, sealPub []byte, name string,
	serial uint64, now time.Time, life time.Duration) (*Credential, [32]byte, error) {

	if auth == nil {
		return nil, [32]byte{}, errors.New("no authority to issue against")
	}
	if len(devPub) != 32 || len(wgPub) != 32 || (len(sealPub) != 0 && len(sealPub) != sealPubLen) {
		return nil, [32]byte{}, errors.New("a device key, a tunnel key and a sealing key are 32 bytes each")
	}
	if len(name) > maxNameLen {
		return nil, [32]byte{}, fmt.Errorf("name is %d bytes, at most %d", len(name), maxNameLen)
	}
	if serial == 0 {
		serial = uint64(now.Unix())
	}
	c := &Credential{
		MeshID:    auth.ID(),
		DevicePub: append([]byte(nil), devPub...),
		WGPub:     append([]byte(nil), wgPub...),
		SealPub:   append([]byte(nil), sealPub...),
		Name:      name,
		Serial:    serial,
		// The same minute of slack IssueFor allows.
		NotBefore: now.Add(-time.Minute).Unix(),
		NotAfter:  now.Add(life).Unix(),
	}
	d, err := c.Digest()
	if err != nil {
		return nil, [32]byte{}, err
	}
	// Carried unsigned in wire form, so it can travel to the signer and back
	// as bytes; a zero signature is what marks it a draft.
	c.Sig = make([]byte, sigLen)
	return c, d, nil
}

// Finish signs a draft with a signature made elsewhere and returns the
// credential in wire form, or says why the signature is not one this mesh
// takes. The signature may be as a card hands it over (SignatureFrom).
func Finish(auth *Authority, draft []byte, sig []byte, now time.Time) ([]byte, error) {
	c, err := UnmarshalCredential(draft)
	if err != nil {
		return nil, err
	}
	if c.MeshID != auth.ID() {
		return nil, errors.New("that draft is for another mesh")
	}
	d, err := c.Digest()
	if err != nil {
		return nil, err
	}
	compact, err := SignatureFrom(sig)
	if err != nil {
		return nil, err
	}
	c.Sig = compact
	// keycard-go loses two bytes of s on the way out of its parser; another
	// card stack may or may not. RepairCardSignature verifies first, so a
	// signature that is already right costs one check and is left alone.
	for _, k := range auth.Keys {
		if fixed, ok := RepairCardSignature(k, d, compact); ok {
			c.Sig = fixed
			break
		}
	}
	if err := VerifyBy(auth, c, now); err != nil {
		return nil, fmt.Errorf("the signature is not one this mesh accepts — is it the card "+
			"that minted the mesh, at the right account? %w", err)
	}
	return c.MarshalBinary()
}

// SignatureFrom reads an ECDSA signature in the forms a card stack hands one
// over: 64 bytes r‖s, 65 bytes r‖s‖v (the recovery byte is not ours to use),
// or DER, which is what a Keycard's legacy signature template carries.
//
// s is not normalised: see CompactSig.
func SignatureFrom(b []byte) ([]byte, error) {
	switch {
	case len(b) == secp256k1SigSize:
		return append([]byte(nil), b...), nil
	case len(b) == secp256k1SigSize+1 && b[0] != 0x30:
		return append([]byte(nil), b[:secp256k1SigSize]...), nil
	case len(b) > 8 && b[0] == 0x30:
		r, s, err := parseDER(b)
		if err != nil {
			return nil, err
		}
		return CompactSig(r, s)
	}
	return nil, fmt.Errorf("a %d-byte signature is none of r‖s, r‖s‖v or DER", len(b))
}

// parseDER reads SEQUENCE { INTEGER r, INTEGER s } with short-form lengths,
// which is all a secp256k1 signature needs. Written out rather than borrowed:
// the strict parsers refuse a high s, and a card is not obliged to make a low
// one.
func parseDER(b []byte) (r, s []byte, err error) {
	bad := errors.New("not a DER signature")
	if len(b) < 8 || b[0] != 0x30 || int(b[1]) != len(b)-2 {
		return nil, nil, bad
	}
	i := 2
	next := func() ([]byte, error) {
		if i+2 > len(b) || b[i] != 0x02 {
			return nil, bad
		}
		n := int(b[i+1])
		i += 2
		if n == 0 || n > 33 || i+n > len(b) {
			return nil, bad
		}
		v := b[i : i+n]
		i += n
		return v, nil
	}
	if r, err = next(); err != nil {
		return nil, nil, err
	}
	if s, err = next(); err != nil {
		return nil, nil, err
	}
	if i != len(b) {
		return nil, nil, bad
	}
	return r, s, nil
}

// IsDraft says whether wire bytes are an unsigned draft rather than a
// credential: a draft's signature is all zeros, which no signer produces.
func IsDraft(b []byte) bool {
	if len(b) < sigLen {
		return false
	}
	for _, x := range b[len(b)-sigLen:] {
		if x != 0 {
			return false
		}
	}
	return true
}

// DraftRevocation is the withdrawal `shrooms admin revoke` would sign,
// unsigned, and its digest. keep is how long peers hold it (zero: forever).
func DraftRevocation(auth *Authority, devPub []byte, serial uint64, now time.Time, keep time.Duration) (*Revocation, [32]byte, error) {
	if auth == nil {
		return nil, [32]byte{}, errors.New("no authority to revoke for")
	}
	if len(devPub) != 32 {
		return nil, [32]byte{}, errors.New("a device key is 32 bytes")
	}
	if serial == 0 {
		serial = uint64(now.Unix())
	}
	r := &Revocation{MeshID: auth.ID(), DevicePub: append([]byte(nil), devPub...), Serial: serial,
		Issued: now.Unix()}
	if keep > 0 {
		r.NotAfter = now.Add(keep).Unix()
	}
	d, err := r.Digest()
	if err != nil {
		return nil, [32]byte{}, err
	}
	r.Sig = make([]byte, sigLen)
	return r, d, nil
}

// FinishRevocation signs a drafted revocation with a signature made
// elsewhere, as Finish does a credential.
func FinishRevocation(auth *Authority, draft []byte, sig []byte) ([]byte, error) {
	r, err := UnmarshalRevocation(draft)
	if err != nil {
		return nil, err
	}
	if r.MeshID != auth.ID() {
		return nil, errors.New("that draft is for another mesh")
	}
	d, err := r.Digest()
	if err != nil {
		return nil, err
	}
	compact, err := SignatureFrom(sig)
	if err != nil {
		return nil, err
	}
	r.Sig = compact
	for _, k := range auth.Keys {
		if fixed, ok := RepairCardSignature(k, d, compact); ok {
			r.Sig = fixed
			break
		}
	}
	if err := VerifyRevocationBy(auth, r); err != nil {
		return nil, fmt.Errorf("the signature is not one this mesh accepts: %w", err)
	}
	return r.MarshalBinary()
}
