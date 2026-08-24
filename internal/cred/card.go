package cred

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Turning what a smartcard hands back into what this package verifies.
//
// A Keycard speaks the shapes Ethereum wanted: an uncompressed 65-byte point
// and a signature whose r and s are minimal-length big-endian integers. This
// package wants a 33-byte compressed point and a fixed 64-byte r‖s. Neither
// side is wrong and the conversion is small — but it is exactly the kind of
// small that fails silently, because a signature with a short r is still 63
// bytes of plausible-looking hex and verifies against nothing.
//
// So it lives here, next to the verifier that will reject it, and it is tested
// against vectors rather than against a card. A card is needed to find out
// whether the *session* works; it is not needed to find out whether 31 bytes
// were left-padded correctly, and a test that needs hardware is a test nobody
// runs.

// CompressPoint turns a card's uncompressed public key into the compressed form
// an authority is written with.
//
// Parsed rather than sliced. The compressed form is a parity byte and X, so
// "compression" could be four lines of byte shuffling that never notices it was
// handed 65 bytes of noise. Going through the curve means a point that is not
// on it is refused here, at the moment somebody is minting an authority from
// it, rather than at the moment the first credential fails to verify.
//
// Already-compressed input is accepted and returned unchanged, so a caller that
// does not know which form it holds can ask for the one it wants.
func CompressPoint(pub []byte) ([]byte, error) {
	switch len(pub) {
	case secp256k1PubKeySize, 65:
	default:
		return nil, fmt.Errorf("public key is %d bytes, want %d compressed or 65 uncompressed",
			len(pub), secp256k1PubKeySize)
	}
	p, err := secp256k1.ParsePubKey(pub)
	if err != nil {
		return nil, fmt.Errorf("not a point on secp256k1: %w", err)
	}
	return p.SerializeCompressed(), nil
}

// CompactSig turns a card's r and s into the fixed 64-byte form.
//
// The card returns each as a minimal-length big-endian integer, so a value that
// happens to start with a zero byte comes back 31 bytes long — about one
// signature in 256, which is frequently enough to reach production and rarely
// enough to look like a flake when it does. Each half is left-padded to 32.
//
// Leading zeros beyond 32 bytes are stripped rather than refused: they carry no
// value and some encoders emit one. Anything longer than that after stripping
// is a different number than the caller thinks it has, and is an error.
//
// s is deliberately NOT normalised to low-S here. See verifyKey: this package
// accepts either, because keycard-go does not canonicalise s and requiring it
// would make signing a coin flip. A caller that has already normalised — the
// mobile stack does — passes straight through.
func CompactSig(r, s []byte) ([]byte, error) {
	rp, err := pad32(r)
	if err != nil {
		return nil, fmt.Errorf("r: %w", err)
	}
	sp, err := pad32(s)
	if err != nil {
		return nil, fmt.Errorf("s: %w", err)
	}
	out := make([]byte, 0, secp256k1SigSize)
	out = append(out, rp...)
	out = append(out, sp...)
	return out, nil
}

// pad32 left-pads a big-endian integer to 32 bytes.
func pad32(b []byte) ([]byte, error) {
	for len(b) > 32 && b[0] == 0 {
		b = b[1:]
	}
	if len(b) > 32 {
		return nil, fmt.Errorf("%d bytes, want at most 32", len(b))
	}
	if len(b) == 0 {
		return nil, errors.New("empty")
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out, nil
}

// SignerOf works out which key actually produced a pair of signatures.
//
// A Keycard has been observed reporting one public key in its signature
// response while signing with another — 04681f8e… reported, while the signature
// recovers to 02e6242f… or 03970770…, and neither is the reported key. Whatever
// the reason, the key that will verify a credential is the one that SIGNED, not
// the one the card mentioned, and this finds it without trusting either.
//
// ECDSA recovery yields two candidate keys per signature and both verify it, so
// one signature cannot say which is real. Two signatures over different digests
// can: the signing key is a candidate for both, and any other candidate is a
// coincidence of one. Two is enough in practice and the check below insists on
// exactly one survivor rather than taking the first.
//
// Nothing here trusts a reported key, so a card that reports the wrong one, or
// none at all, is handled the same way.
func SignerOf(digestA [32]byte, sigA []byte, digestB [32]byte, sigB []byte) ([]byte, error) {
	candA, err := recoverCandidates(digestA, sigA)
	if err != nil {
		return nil, err
	}
	candB, err := recoverCandidates(digestB, sigB)
	if err != nil {
		return nil, err
	}
	var common [][]byte
	for _, a := range candA {
		for _, b := range candB {
			if bytes.Equal(a, b) {
				common = append(common, a)
			}
		}
	}
	switch len(common) {
	case 1:
		return common[0], nil
	case 0:
		return nil, errors.New("no key signed both digests — the card is not signing " +
			"with one key, or a signature was mangled on the way back")
	default:
		return nil, fmt.Errorf("%d keys could have signed both digests, which should "+
			"not happen; sign again", len(common))
	}
}

// recoverCandidates returns every public key that could have produced a
// signature over a digest. There are at most two.
func recoverCandidates(digest [32]byte, sig []byte) ([][]byte, error) {
	if len(sig) != secp256k1SigSize {
		return nil, fmt.Errorf("signature is %d bytes, want %d", len(sig), secp256k1SigSize)
	}
	var out [][]byte
	for recid := 0; recid < 2; recid++ {
		c := make([]byte, 65)
		c[0] = byte(27 + recid)
		copy(c[1:], sig)
		pub, _, err := ecdsa.RecoverCompact(c, digest[:])
		if err != nil {
			continue
		}
		out = append(out, pub.SerializeCompressed())
	}
	if len(out) == 0 {
		return nil, errors.New("no key could have produced that signature")
	}
	return out, nil
}

// RepairCardSignature recovers the two bytes keycard-go loses from s.
//
// The bug is in the library, not the card. parseLegacySignature reads r and s
// as slices INTO the response buffer, then calculateV does:
//
//	rs := append(r, s...)
//
// r's backing array is that buffer, so the append writes s on top of the two
// DER header bytes (02 20) that sit between r and s — and the s slice still
// points at the old offset. What comes back is s shifted two bytes left with
// two bytes of whatever followed on the end. Demonstrated with a signature made
// locally, no card involved: 366fdd72…a5d2 in, dd72a3ae…01d2 out.
//
// v, r and the public key survive, because calculateV computes with its own
// correct copy and the append does not reach backwards over r.
//
// So the first thirty bytes of the real s are the last thirty of what we were
// given, and the first two are gone — and they can be solved for rather than
// guessed, because s enters ECDSA verification linearly. See solvePrefix.
//
// The result is verified before it is returned, so this cannot hand back a
// signature that does not check out. That is what makes repairing a signature
// defensible rather than a guess: the card's own key decides, not this code.
//
// Remove this when the library is fixed. VerifyDigest is tried first, so a
// correct signature costs one verification and never reaches the repair.
func RepairCardSignature(pub ed25519.PublicKey, digest [32]byte, sig []byte) ([]byte, bool) {
	if len(sig) != secp256k1SigSize {
		return nil, false
	}
	if VerifyDigest(pub, digest, sig) {
		return sig, true
	}
	fixed := make([]byte, secp256k1SigSize)
	copy(fixed, sig[:32])        // r is intact
	copy(fixed[34:], sig[32:62]) // the surviving thirty bytes of s
	hi, lo, ok := solvePrefix(pub, digest[:], fixed[:32], fixed[34:])
	if !ok {
		return nil, false
	}
	fixed[32], fixed[33] = hi, lo
	if !VerifyDigest(pub, digest, fixed) {
		return nil, false
	}
	return fixed, true
}

// solvePrefix recovers the two bytes of s that keycard-go destroyed.
//
// It used to guess them: sixty-five thousand candidates, each put through a
// full ECDSA verification — two scalar multiplications apiece, three to five
// seconds while somebody holds a card against the back of a phone. Correct,
// and long enough that the first person to run it thought it had hung.
//
// It does not have to be a search. The missing bytes are the top of s, and s
// enters verification linearly, so they can be solved for. Verification accepts
// (r, s) when the nonce point R satisfies
//
//	s·R = e·G + r·Q
//
// where e is the digest, Q the card's key, and R.x ≡ r. Every term there is
// public: R is one of the two curve points with x = r, and e·G + r·Q needs
// nothing secret. Writing the unknown two bytes as A and the surviving thirty
// as B, s = A·2^240 + B, so
//
//	A·(2^240·R) = (e·G + r·Q) − B·R
//
// Both sides are computable without knowing A. So finding A is walking the
// multiples of one fixed point until one matches — sixty-five thousand point
// additions rather than sixty-five thousand verifications, which is roughly two
// orders of magnitude, and the wait stops being a thing anybody notices.
//
// The caller verifies the result, so a wrong answer here cannot escape; the
// worst this can do is fail to find one. It gives up rather than trying the
// x = r + n form of R, which is legal and occurs with probability around 2^-127
// — a card would have to be tapped for longer than the universe has run to see
// it once, and the honest failure is a retry.
func solvePrefix(pub, digest, r, tail []byte) (hi, lo byte, ok bool) {
	if len(pub) != secp256k1PubKeySize || len(digest) != 32 || len(r) != 32 || len(tail) != 30 {
		return 0, 0, false
	}
	q, err := secp256k1.ParsePubKey(pub)
	if err != nil {
		return 0, 0, false
	}
	var rs, es, bs secp256k1.ModNScalar
	// Overflow in r is a malformed signature. In e it is ordinary: the digest
	// is reduced mod n by definition, and verification does the same.
	if rs.SetByteSlice(r) {
		return 0, 0, false
	}
	es.SetByteSlice(digest)
	var b [32]byte
	copy(b[2:], tail) // B is s with the two missing bytes read as zero
	bs.SetByteSlice(b[:])

	// P = e·G + r·Q, the right-hand side, fixed for the whole search.
	var eG, qj, rQ, p secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(&es, &eG)
	q.AsJacobian(&qj)
	secp256k1.ScalarMultNonConst(&rs, &qj, &rQ)
	secp256k1.AddNonConst(&eG, &rQ, &p)

	var step secp256k1.ModNScalar
	var stepBytes [32]byte
	stepBytes[1] = 1 // 2^240: one, thirty bytes up
	step.SetByteSlice(stepBytes[:])

	// R is the point with x = r. Which of the two parities it has is not
	// recorded anywhere this code can reach — keycard-go's v would say, but v
	// is computed from the corrupted s — so both are tried.
	for _, parity := range []byte{0x02, 0x03} {
		rp, err := secp256k1.ParsePubKey(append([]byte{parity}, r...))
		if err != nil {
			continue // x = r is not on the curve for either parity
		}
		var rj, u, bR, target secp256k1.JacobianPoint
		rp.AsJacobian(&rj)
		secp256k1.ScalarMultNonConst(&step, &rj, &u)
		secp256k1.ScalarMultNonConst(&bs, &rj, &bR)
		bR.ToAffine()
		bR.Y.Negate(1).Normalize() // subtracting B·R is adding its negation
		secp256k1.AddNonConst(&p, &bR, &target)
		target.ToAffine()

		var acc, next secp256k1.JacobianPoint // A = 0 is the point at infinity
		for a := 0; a < 1<<16; a++ {
			if samePoint(&acc, &target) {
				return byte(a >> 8), byte(a), true
			}
			secp256k1.AddNonConst(&acc, &u, &next)
			acc = next
		}
	}
	return 0, 0, false
}

// samePoint compares a Jacobian point against an affine one without inverting.
//
// An inversion per step would cost more than the addition it is checking, so
// the affine point is scaled up into the other's frame instead: (X, Y) matches
// (X′, Y′, Z) when X·Z² = X′ and Y·Z³ = Y′.
func samePoint(acc, affine *secp256k1.JacobianPoint) bool {
	if acc.Z.IsZero() || (acc.X.IsZero() && acc.Y.IsZero()) {
		return affine.Z.IsZero() || (affine.X.IsZero() && affine.Y.IsZero())
	}
	var z2, z3, x, y secp256k1.FieldVal
	z2.SquareVal(&acc.Z)
	z3.Mul2(&z2, &acc.Z)
	x.Mul2(&affine.X, &z2)
	y.Mul2(&affine.Y, &z3)
	return x.Normalize().Equals(acc.X.Normalize()) && y.Normalize().Equals(acc.Y.Normalize())
}
