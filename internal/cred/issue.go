package cred

import (
	"fmt"
	"time"
)

// IssueFor signs a credential and returns it in wire form.
//
// The mesh id is the authority's, not the signer's: a credential says which
// mesh it admits you to, and this admin is one key among that mesh's set.
// Admin.Issue cannot know that — it only has its own key — so every caller has
// to restamp and re-sign against the authority.
//
// This lives here rather than beside one of its callers because there are now
// callers on both sides of the gomobile boundary. The desktop issues from a key
// in a file; a phone issues from a card held against its back. The rules about
// serials, clock slack and which mesh id to stamp are identical and are exactly
// the kind of thing that drifts when it is written twice.
// sealPub may be empty, which issues a version 1 credential — that is what
// every device that has not yet published a sealing key gets, and it works
// exactly as it did before.
func IssueFor(admin Signer, auth *Authority, devPub, wgPub, sealPub []byte,
	name string, serial uint64, now time.Time, life time.Duration) ([]byte, error) {

	if auth == nil {
		return nil, fmt.Errorf("no authority to issue against")
	}
	// A serial of zero means "now", in unix seconds.
	//
	// Serials must increase per device, because a revocation withdraws its
	// serial and everything below — so re-issuing at the same serial would put
	// the renewed credential inside the range an old revocation covers. Nothing
	// tracks a counter per device, and a clock is a counter everyone already
	// agrees on.
	if serial == 0 {
		serial = uint64(now.Unix())
	}
	// Through the Signer seam (ADR-022) rather than signing here: the admin key
	// is the one secret in this system whose usage pattern suits a smartcard —
	// a handful of signatures a year, each a deliberate act by someone present
	// — and everything above this line already works in terms of a digest.
	c, err := IssueWith(admin, devPub, wgPub, sealPub, name, serial,
		// A minute of slack, because clocks differ and a credential that is not
		// yet valid on the machine it was just issued to is a confusing
		// failure.
		now.Add(-time.Minute).Unix(), now.Add(life).Unix(), auth.ID())
	if err != nil {
		return nil, err
	}
	// Verified before it leaves, which matters now that a signer can be a piece
	// of hardware. A file key that signs at all signs correctly; a card can
	// return something well-formed and wrong — wrong curve, wrong key, a
	// truncated response from a tag that moved. The alternative to checking
	// here is finding out on somebody else's device days later, when the
	// credential is the only evidence and the card is in a drawer.
	//
	// It costs one signature verification per issuance, and issuance is a thing
	// a person does a handful of times a year.
	if err := VerifyBy(auth, c, now); err != nil {
		return nil, fmt.Errorf("the signer produced a credential this mesh will not accept: %w", err)
	}
	return c.MarshalBinary()
}
