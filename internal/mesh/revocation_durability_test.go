package mesh

import (
	"log/slog"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// SECURITY.md says a revocation is kept until the credential it withdraws would
// have expired. It was kept in memory only, so the real answer was "until this
// machine next restarts" — revoke a stolen laptop, reboot the node, and the
// laptop is a member again with nothing said.
//
// These tests use the List and the state layer directly rather than a running
// Mesh: accepting a revocation on a Mesh publishes to the bus, which needs a
// live rendezvous node. What is under test is that the withdrawal survives the
// gap between two processes.
func TestRevocationSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	admin, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(admin.Pub)
	gone, _ := identity.New()
	now := time.Now()

	// The credential the stolen device holds, and the admin's withdrawal of it.
	c, err := admin.Issue(gone.DevicePub, gone.WGPub[:], "stolen", 1, now, cred.DefaultLife)
	if err != nil {
		t.Fatal(err)
	}
	r, err := admin.Revoke(gone.DevicePub, 1, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := r.MarshalBinary()

	// A node learns it and writes it down.
	first, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := cred.NewList()
	if !list.Add(r, raw) {
		t.Fatal("the list refused a fresh revocation")
	}
	if !list.Revoked(c) {
		t.Fatal("the credential is not revoked in the list that just accepted it")
	}
	if err := first.SetRevocations(state.NetworkID(identity.NetworkKey{}), list.All()); err != nil {
		t.Fatal(err)
	}

	// The daemon restarts: new process, new State, new empty List.
	second, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mesh{
		log:       slog.New(slog.DiscardHandler),
		st:        second,
		authority: auth,
		revoked:   cred.NewList(),
		networkID: state.NetworkID(identity.NetworkKey{}),
	}
	m.loadRevocations()

	if !m.revoked.Revoked(c) {
		t.Error("the restarted node re-admitted a device that had been revoked")
	}
}

// The stored list is verified again on load rather than trusted for being on
// our own disk. Anyone who can write the state dir could otherwise plant an
// entry — or, more likely, a file copied between meshes would silently withdraw
// a device the local authority never signed for.
func TestStoredRevocationsAreVerifiedOnLoad(t *testing.T) {
	dir := t.TempDir()
	ours, _ := cred.NewAdmin()
	theirs, _ := cred.NewAdmin()
	auth, _ := cred.NewAuthority(ours.Pub)
	victim, _ := identity.New()
	now := time.Now()

	// A withdrawal signed by somebody else's admin, sitting in our state dir.
	r, err := theirs.Revoke(victim.DevicePub, 1, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := r.MarshalBinary()

	st, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRevocations("planted", [][]byte{raw}); err != nil {
		t.Fatal(err)
	}

	m := &Mesh{
		log:       slog.New(slog.DiscardHandler),
		st:        st,
		authority: auth,
		revoked:   cred.NewList(),
		networkID: "planted",
	}
	m.loadRevocations()

	if m.revoked.Len() != 0 {
		t.Error("loaded a revocation this mesh's authority did not sign")
	}
}

// A mesh whose membership is the network key alone has no authority to verify
// against and nothing that can be revoked, so it must not try.
func TestNoAuthorityLoadsNothing(t *testing.T) {
	dir := t.TempDir()
	st, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mesh{
		log:     slog.New(slog.DiscardHandler),
		st:      st,
		revoked: cred.NewList(),
	}
	m.loadRevocations() // must not panic on a nil authority
	m.saveRevocations()

	if m.revoked.Len() != 0 {
		t.Error("a mesh with no authority loaded a revocation")
	}
}

// The round trip has to survive the file, not just the function: an entry is
// stored as the signed wire bytes and must come back byte-identical, because
// that is what the signature covers.
func TestRevocationRoundTripThroughTheStateDir(t *testing.T) {
	dir := t.TempDir()
	admin, _ := cred.NewAdmin()
	a, _ := identity.New()
	b, _ := identity.New()
	now := time.Now()

	var raws [][]byte
	for _, id := range []*identity.Identity{a, b} {
		r, err := admin.Revoke(id.DevicePub, 7, time.Time{}, now)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := r.MarshalBinary()
		raws = append(raws, raw)
	}

	st, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRevocations("mesh1", raws); err != nil {
		t.Fatal(err)
	}

	reopened, err := state.LoadOrCreateState(dir)
	if err != nil {
		t.Fatal(err)
	}
	back := reopened.Revocations("mesh1")
	if len(back) != len(raws) {
		t.Fatalf("stored %d revocations, read back %d", len(raws), len(back))
	}
	seen := map[string]bool{}
	for _, r := range back {
		seen[string(r)] = true
	}
	for _, want := range raws {
		if !seen[string(want)] {
			t.Error("a revocation did not survive the round trip byte-for-byte")
		}
	}

	// A different mesh's file must not answer for this one.
	if len(st.Revocations("mesh2")) != 0 {
		t.Error("one mesh's revocations leaked into another's")
	}
}

// A revocation only means something if it reaches a node that was offline when
// it was published, so a peer appearing is a reason to say it again. These pin
// the decision — the switch, the empty list, and the cooldown — rather than the
// publish, which needs a live rendezvous node.
func TestAPeerAppearingEarnsOneRepetition(t *testing.T) {
	admin, err := cred.NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := identity.New()
	r, err := admin.Revoke(dev.DevicePub, 1, time.Time{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	m := &Mesh{revoked: cred.NewList()}
	now := time.Now()

	// Nothing withdrawn, nothing to say — however many peers turn up.
	if m.shouldRepeatRevocations(now) {
		t.Error("repeated an empty revocation list")
	}

	m.revoked.Add(r, raw)
	if !m.shouldRepeatRevocations(now) {
		t.Fatal("the first peer to appear did not earn a repetition")
	}

	// A restart discovers every peer at once. Each publish goes to the mesh
	// topic, not to the peer that arrived, so repeating per arrival would send
	// the same list to the same audience N times.
	for i := 0; i < 20; i++ {
		if m.shouldRepeatRevocations(now.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("a wave of arrivals repeated again after %ds", i)
		}
	}

	if !m.shouldRepeatRevocations(now.Add(RevocationRepublishCooldown + time.Second)) {
		t.Error("a peer appearing after the cooldown did not earn a repetition")
	}
}

// Off means off: a node on a metered uplink says nothing, and relies on another
// node that does. There is no designated repeater to fall back on — the admin
// key is not on any daemon — so this is the setting that trades a guarantee for
// bytes, and it must actually be honoured.
func TestQuietRevocationsSaysNothing(t *testing.T) {
	admin, err := cred.NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := identity.New()
	r, _ := admin.Revoke(dev.DevicePub, 1, time.Time{}, time.Now())
	raw, _ := r.MarshalBinary()

	m := &Mesh{revoked: cred.NewList(), cfg: state.Config{QuietRevocations: true}}
	m.revoked.Add(r, raw)

	if m.shouldRepeatRevocations(time.Now()) {
		t.Error("a node set to stay quiet repeated a revocation")
	}
}
