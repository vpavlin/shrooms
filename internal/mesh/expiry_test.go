package mesh

import (
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
)

// An expired credential used to leave a working tunnel in place for up to six
// hours. checkMembership refused the peer's later announces, which stops the
// roster being updated and nothing else: syncPeers kept the WireGuard peer
// while its tunnel was live, and a 25s keepalive keeps a tunnel live forever.
// The device was dropped only when it aged out at ForgetAfter.
//
// That matters because expiry is the backstop SECURITY.md leans on precisely
// because it cannot be suppressed — unlike revocation, which needs a message to
// arrive.
func TestExpiredCredentialStopsBeingCarried(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	m := &Mesh{
		log:            slog.New(slog.DiscardHandler),
		authority:      auth,
		expiry:         map[string]int64{},
		expiredDropped: map[string]bool{},
	}

	const id = "abc123"
	m.expiry[id] = now.Add(-time.Minute).Unix() // ran out a minute ago
	if !m.expired(id, now) {
		t.Error("a credential that ran out a minute ago is not reported expired")
	}

	m.expiry[id] = now.Add(time.Hour).Unix() // renewed
	if m.expired(id, now) {
		t.Error("a credential with an hour left is reported expired")
	}
}

// A peer whose expiry this process has never seen must be carried, not dropped.
// A node that has just started knows nobody's expiry until each peer announces
// again, and dropping everyone until then would turn every restart into an
// outage — the opposite of what this fix is for.
func TestUnknownExpiryIsNotTreatedAsExpired(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)

	m := &Mesh{
		log:            slog.New(slog.DiscardHandler),
		authority:      auth,
		expiry:         map[string]int64{},
		expiredDropped: map[string]bool{},
	}
	if m.expired("never-seen", time.Now()) {
		t.Error("a peer with no known expiry was treated as expired")
	}
}

// A mesh whose membership is the network key has nothing to expire. Reporting
// expiry there would drop every peer on a bearer mesh, where no credential
// exists to have run out.
func TestBearerMeshNeverExpires(t *testing.T) {
	m := &Mesh{
		log:            slog.New(slog.DiscardHandler),
		expiry:         map[string]int64{"someone": time.Now().Add(-time.Hour).Unix()},
		expiredDropped: map[string]bool{},
	}
	if m.expired("someone", time.Now()) {
		t.Error("a mesh with no authority expired a peer")
	}
}

// Noticing an expiry must ask for one resync, not one per probe tick for the
// six hours the roster keeps the device.
func TestExpiryIsActedOnOnce(t *testing.T) {
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	now := time.Now()

	m := &Mesh{
		log:            slog.New(slog.DiscardHandler),
		authority:      auth,
		expiry:         map[string]int64{"gone": now.Add(-time.Hour).Unix()},
		expiredDropped: map[string]bool{},
		resync:         make(chan struct{}, 1),
	}

	first := m.noteExpired("gone")
	second := m.noteExpired("gone")

	if !first {
		t.Error("the first expiry was not acted on")
	}
	if second {
		t.Error("the same expiry would be acted on twice")
	}

	// A renewal makes it actionable again, so a device that expires, is
	// renewed, and expires once more is dropped the second time too. This is
	// what checkMembership does when a fresh credential arrives.
	m.mu.Lock()
	delete(m.expiredDropped, "gone")
	m.mu.Unlock()
	if !m.noteExpired("gone") {
		t.Error("after a renewal, a second expiry was not acted on")
	}
}
