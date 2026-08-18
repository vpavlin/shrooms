package cred

import (
	"testing"
	"time"
)

// revFor mints one with no NotAfter: the version 1 shape, kept forever.
func revFor(t *testing.T, a *Admin, dev []byte, serial uint64) (*Revocation, []byte) {
	t.Helper()
	return revUntil(t, a, dev, serial, time.Time{})
}

// revUntil mints one that says when it may be forgotten.
func revUntil(t *testing.T, a *Admin, dev []byte, serial uint64, notAfter time.Time) (*Revocation, []byte) {
	t.Helper()
	r, err := a.Revoke(dev, serial, notAfter, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return r, raw
}

// Every node keeps its own list and checks it itself. That is what makes
// revocation mean anything on a bus with no authority: a compromised node
// cannot un-revoke a device by staying quiet, because its peers already hold
// the statement.
func TestListWithdrawsACredential(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()
	l := NewList()

	c, _ := admin.Issue(dev, wg, "phone", 4, now, time.Hour)
	if l.Revoked(c) {
		t.Fatal("revoked before anything was revoked")
	}

	r, raw := revFor(t, admin, dev, 4)
	if !l.Add(r, raw) {
		t.Error("a new revocation was not recorded")
	}
	if !l.Revoked(c) {
		t.Error("a revoked credential was not recognised")
	}

	// Learning the same thing twice is not news, which is what stops a relayed
	// revocation echoing around the mesh forever.
	if l.Add(r, raw) {
		t.Error("the same revocation was treated as new")
	}
}

// A revocation withdraws its serial and everything below, so re-enrolling with
// a higher serial restores the device — and an older revocation replayed later
// must not undo that.
func TestAHigherSerialSurvivesAnOlderRevocation(t *testing.T) {
	admin, _ := NewAdmin()
	dev, wg := device(t)
	now := time.Now()
	l := NewList()

	r, raw := revFor(t, admin, dev, 4)
	l.Add(r, raw)

	reissued, _ := admin.Issue(dev, wg, "phone", 5, now, time.Hour)
	if l.Revoked(reissued) {
		t.Error("an old revocation withdrew a later credential")
	}

	// And a newer revocation covers the newer credential.
	r2, raw2 := revFor(t, admin, dev, 5)
	if !l.Add(r2, raw2) {
		t.Error("a higher-serial revocation was not recorded")
	}
	if !l.Revoked(reissued) {
		t.Error("the reissued credential was not withdrawn")
	}
}

// An entry is dropped once the credential it withdraws would have expired
// anyway — but only when the revocation itself said when that is. The admin
// signs that moment; this package no longer guesses it.
func TestListForgetsWhatExpiryWouldHandle(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	now := time.Now()
	l := NewList()

	r, raw := revUntil(t, admin, dev, 1, now.Add(time.Hour))
	l.Add(r, raw)
	if l.Len() != 1 {
		t.Fatalf("list holds %d", l.Len())
	}
	if n := l.Prune(now.Add(30 * time.Minute)); n != 0 {
		t.Errorf("pruned %d entries that were still needed", n)
	}
	if n := l.Prune(now.Add(2 * time.Hour)); n != 1 {
		t.Errorf("pruned %d entries, want 1", n)
	}
	if l.Len() != 0 {
		t.Error("an expired revocation was kept")
	}
}

// Only the mesh's own authority may revoke. Otherwise any member could eject
// any other, which is worse than having no revocation at all.
func TestOnlyThisMeshsAuthorityMayRevoke(t *testing.T) {
	ours, _ := NewAdmin()
	theirs, _ := NewAdmin()
	auth, _ := NewAuthority(ours.Pub)
	dev, _ := device(t)

	_, raw := revFor(t, theirs, dev, 1)
	r, err := UnmarshalRevocation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRevocationBy(auth, r); err == nil {
		t.Error("a revocation from another authority was accepted")
	}

	_, mine := revFor(t, ours, dev, 1)
	m, _ := UnmarshalRevocation(mine)
	m.MeshID = auth.ID()
	d, _ := m.Digest()
	m.Sig = signRaw(ours, d)
	if err := VerifyRevocationBy(auth, m); err != nil {
		t.Errorf("our own revocation was rejected: %v", err)
	}
}

// A mesh with no authority never builds a list, and a caller should not have to
// know that before asking whether something is revoked.
func TestNilListAnswersEverythingSafely(t *testing.T) {
	var l *List
	admin, _ := NewAdmin()
	dev, wg := device(t)
	c, _ := admin.Issue(dev, wg, "phone", 1, time.Now(), time.Hour)
	r, raw := revFor(t, admin, dev, 1)

	if l.Revoked(c) {
		t.Error("a nil list reported something revoked")
	}
	if l.Add(r, raw) {
		t.Error("a nil list accepted a revocation")
	}
	if l.Len() != 0 || l.Prune(time.Now()) != 0 || l.All() != nil {
		t.Error("a nil list did not behave as empty")
	}
}

// A revocation that names no expiry is kept forever, which is the whole reason
// Prune sat uncalled for as long as it did: a revocation for a credential
// issued with a longer --life would have been forgotten while that credential
// still verified, and the device would have walked back onto the mesh.
func TestARevocationWithNoBoundIsNeverPruned(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	now := time.Now()
	l := NewList()

	r, raw := revFor(t, admin, dev, 1) // no NotAfter
	l.Add(r, raw)

	if n := l.Prune(now.Add(10 * 365 * 24 * time.Hour)); n != 0 {
		t.Errorf("pruned %d unbounded revocations; they must be kept", n)
	}
	if l.Len() != 1 {
		t.Error("an unbounded revocation was dropped")
	}
}

// Two revocations for the same device can disagree about when it stops
// mattering. Keeping the shorter one would re-admit a device the other still
// withdraws, so the longer wins — and "no bound" is longer than any date.
func TestTheLongerRetentionWins(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	now := time.Now()

	// A later, shorter one must not shorten a longer one.
	l := NewList()
	long, longRaw := revUntil(t, admin, dev, 5, now.Add(48*time.Hour))
	short, shortRaw := revUntil(t, admin, dev, 5, now.Add(time.Hour))
	l.Add(long, longRaw)
	l.Add(short, shortRaw)
	if n := l.Prune(now.Add(2 * time.Hour)); n != 0 {
		t.Error("a later, shorter revocation shortened the retention")
	}

	// And an unbounded one outlasts a bounded one.
	l2 := NewList()
	unbounded, unboundedRaw := revFor(t, admin, dev, 5)
	l2.Add(unbounded, unboundedRaw)
	l2.Add(short, shortRaw)
	if n := l2.Prune(now.Add(2 * time.Hour)); n != 0 {
		t.Error("a bounded revocation shortened an unbounded one")
	}
}

// The bound is signed, so a holder cannot decide a revocation lapses early.
// Editing NotAfter must break the signature rather than move the deadline.
func TestTheRetentionBoundIsSigned(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	dev, _ := device(t)
	now := time.Now()

	r, raw := revUntil(t, admin, dev, 1, now.Add(30*24*time.Hour))
	if err := VerifyRevocationBy(auth, r); err != nil {
		t.Fatalf("a freshly signed revocation did not verify: %v", err)
	}

	// Bring the deadline forward, the way a node wanting a removed device back
	// would.
	tampered, err := UnmarshalRevocation(raw)
	if err != nil {
		t.Fatal(err)
	}
	tampered.NotAfter = now.Add(time.Minute).Unix()
	if err := VerifyRevocationBy(auth, tampered); err == nil {
		t.Error("a revocation with an edited expiry still verified")
	}
}

// Version 1 revocations are on disk in every mesh that has ever revoked
// anything. Refusing to parse them at upgrade would drop them all, which
// un-revokes exactly the devices somebody deliberately removed.
func TestVersionOneRevocationsStillVerify(t *testing.T) {
	admin, _ := NewAdmin()
	auth, _ := NewAuthority(admin.Pub)
	dev, _ := device(t)

	// Built the way version 1 built them: no NotAfter, signed over the v1 body
	// with the v1 domain separator.
	r := &Revocation{MeshID: MeshOf(admin.Pub), DevicePub: dev, Serial: 3,
		Issued: time.Now().Unix(), ver: 1}
	d, err := r.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.Sig = signRaw(admin, d)
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != 1 {
		t.Fatalf("wrote version %d, wanted a version 1 blob", raw[0])
	}

	back, err := UnmarshalRevocation(raw)
	if err != nil {
		t.Fatalf("a version 1 revocation no longer parses: %v", err)
	}
	if err := VerifyRevocationBy(auth, back); err != nil {
		t.Errorf("a version 1 revocation no longer verifies: %v", err)
	}
	if !back.Forgettable().IsZero() {
		t.Error("a version 1 revocation claimed a retention bound")
	}
}

// New revocations are written as version 2 and carry the bound on the wire.
func TestANewRevocationIsVersionTwo(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	until := time.Now().Add(72 * time.Hour).Truncate(time.Second)

	_, raw := revUntil(t, admin, dev, 1, until)
	if raw[0] != 2 {
		t.Fatalf("wrote version %d, want 2", raw[0])
	}
	back, err := UnmarshalRevocation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Forgettable().Equal(until) {
		t.Errorf("NotAfter came back as %v, want %v", back.Forgettable(), until)
	}
}
