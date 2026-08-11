package cred

import (
	"testing"
	"time"
)

func revFor(t *testing.T, a *Admin, dev []byte, serial uint64) (*Revocation, []byte) {
	t.Helper()
	r, err := a.Revoke(dev, serial, time.Now())
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
	if !l.Add(r, raw, now.Add(time.Hour)) {
		t.Error("a new revocation was not recorded")
	}
	if !l.Revoked(c) {
		t.Error("a revoked credential was not recognised")
	}

	// Learning the same thing twice is not news, which is what stops a relayed
	// revocation echoing around the mesh forever.
	if l.Add(r, raw, now.Add(time.Hour)) {
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
	l.Add(r, raw, now.Add(time.Hour))

	reissued, _ := admin.Issue(dev, wg, "phone", 5, now, time.Hour)
	if l.Revoked(reissued) {
		t.Error("an old revocation withdrew a later credential")
	}

	// And a newer revocation covers the newer credential.
	r2, raw2 := revFor(t, admin, dev, 5)
	if !l.Add(r2, raw2, now.Add(time.Hour)) {
		t.Error("a higher-serial revocation was not recorded")
	}
	if !l.Revoked(reissued) {
		t.Error("the reissued credential was not withdrawn")
	}
}

// Entries are dropped once the credential they withdraw would have expired
// anyway. Keeping them forever would grow without bound on exactly the input an
// attacker controls: the number of device keys they can invent.
func TestListForgetsWhatExpiryWouldHandle(t *testing.T) {
	admin, _ := NewAdmin()
	dev, _ := device(t)
	now := time.Now()
	l := NewList()

	r, raw := revFor(t, admin, dev, 1)
	l.Add(r, raw, now.Add(time.Hour))
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
	if l.Add(r, raw, time.Now()) {
		t.Error("a nil list accepted a revocation")
	}
	if l.Len() != 0 || l.Prune(time.Now()) != 0 || l.All() != nil {
		t.Error("a nil list did not behave as empty")
	}
}
