package state

import (
	"testing"
	"time"
)

func newState(t *testing.T) *State {
	t.Helper()
	st, err := LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The point of the file: an address learned in one process is available to the
// next one. Bootstrap addresses are read only when the delivery node is built,
// so anything not written down is worth nothing.
func TestBootPeersSurviveTheProcess(t *testing.T) {
	st := newState(t)
	now := time.Now()
	addr := "/ip4/128.140.55.128/tcp/39777/p2p/16Uiu2HAmPxQP7XcHXaJrx3KfYEuXKWyiARCwAVPXGvmfY5awHpXx"

	if got := st.BootPeers(now); len(got) != 0 {
		t.Fatalf("a fresh node already had %d addresses", len(got))
	}
	if err := st.NoteBootPeer(addr, now); err != nil {
		t.Fatal(err)
	}

	// A new State over the same directory is what a restart looks like.
	again, err := LoadOrCreateState(st.dir)
	if err != nil {
		t.Fatal(err)
	}
	got := again.BootPeers(now)
	if len(got) != 1 || got[0] != addr {
		t.Errorf("after a restart: %v", got)
	}
}

// Seeing the same address again refreshes it rather than duplicating it. A
// peer republishes every 45 seconds, so this is the common path by far.
func TestSeeingAnAddressAgainDoesNotDuplicateIt(t *testing.T) {
	st := newState(t)
	now := time.Now()
	addr := "/ip4/1.2.3.4/tcp/1/p2p/abc"

	for i := 0; i < 5; i++ {
		if err := st.NoteBootPeer(addr, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if got := st.BootPeers(now); len(got) != 1 {
		t.Errorf("five sightings of one address stored %d entries", len(got))
	}
}

// The list is bounded, and it is the stale entries that go. Each address is
// dialled at startup, so a long list is a slow start rather than a robust one.
func TestBootPeersAreBoundedAndKeepTheFreshest(t *testing.T) {
	st := newState(t)
	base := time.Now()

	// Oldest first, so the newest are the ones that must survive.
	for i := 0; i < MaxBootPeers+4; i++ {
		addr := "/ip4/1.2.3.4/tcp/1/p2p/peer" + string(rune('a'+i))
		if err := st.NoteBootPeer(addr, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got := st.BootPeers(base.Add(MaxBootPeers * time.Hour))
	if len(got) > MaxBootPeers {
		t.Fatalf("kept %d addresses, cap is %d", len(got), MaxBootPeers)
	}
	// The most recent sighting must be first: it is the most likely to answer.
	if got[0] != "/ip4/1.2.3.4/tcp/1/p2p/peer"+string(rune('a'+MaxBootPeers+3)) {
		t.Errorf("the freshest address is not first: %v", got[0])
	}
}

// An address nobody has republished for a month is a machine that has probably
// gone. Dropped on read, since this is consulted once per start.
func TestStaleAddressesExpire(t *testing.T) {
	st := newState(t)
	old := time.Now().Add(-BootPeerTTL - time.Hour)
	if err := st.NoteBootPeer("/ip4/1.2.3.4/tcp/1/p2p/gone", old); err != nil {
		t.Fatal(err)
	}
	if got := st.BootPeers(time.Now()); len(got) != 0 {
		t.Errorf("a month-old address survived: %v", got)
	}
}

// A corrupt or missing file must not stop a node starting. It falls back to the
// configured addresses, which is where every node started before this existed.
func TestACorruptFileIsNotFatal(t *testing.T) {
	st := newState(t)
	if err := writeFileAtomic(st.bootPeerPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := st.BootPeers(time.Now()); got != nil {
		t.Errorf("a corrupt file produced %v", got)
	}
	// And it can be written over rather than staying broken forever.
	if err := st.NoteBootPeer("/ip4/1.2.3.4/tcp/1/p2p/ok", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := st.BootPeers(time.Now()); len(got) != 1 {
		t.Errorf("could not recover from a corrupt file: %v", got)
	}
}
