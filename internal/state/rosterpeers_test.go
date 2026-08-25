package state

import (
	"testing"
	"time"
)

func remembered(seen time.Time, dev string) RosterPeer {
	return RosterPeer{
		DevicePub: dev,
		WGPub:     "d2c=",
		Name:      "peer",
		Endpoints: []string{"203.0.113.7:51820"},
		Seq:       9,
		Seen:      seen.Unix(),
	}
}

// The endpoints are the point of the file: everything else can be rebuilt from
// an announce, and an announce is what we do not have yet.
func TestRosterPeersRoundTrip(t *testing.T) {
	s := &State{dir: t.TempDir()}
	now := time.Now()

	in := remembered(now.Add(-time.Minute), "ZGV2aWNl")
	in.Credential = "Y3JlZA=="
	in.Relay = true
	if err := s.SetRosterPeers("abc234", []RosterPeer{in}); err != nil {
		t.Fatal(err)
	}

	out := s.RosterPeers("abc234", now)
	if len(out) != 1 {
		t.Fatalf("read back %d peers, want 1", len(out))
	}
	got := out[0]
	if got.DevicePub != in.DevicePub || got.Seq != 9 || !got.Relay {
		t.Errorf("came back as %+v", got)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != "203.0.113.7:51820" {
		t.Errorf("endpoints came back as %v", got.Endpoints)
	}
	if got.Credential != "Y3JlZA==" {
		t.Error("the credential did not survive, so the peer could not be re-checked")
	}
}

// Per mesh, like the replay marks: one mesh's peers must not be offered to
// another, whose authority never signed for them.
func TestRosterPeersAreKeptPerMesh(t *testing.T) {
	s := &State{dir: t.TempDir()}
	now := time.Now()

	if err := s.SetRosterPeers("aaa222", []RosterPeer{remembered(now, "b25l")}); err != nil {
		t.Fatal(err)
	}
	if len(s.RosterPeers("bbb333", now)) != 0 {
		t.Error("one mesh's remembered peers showed up in another")
	}
	if len(s.RosterPeers("aaa222", now)) != 1 {
		t.Error("a mesh lost its own remembered peers")
	}
}

// Past the TTL an entry is a device that may not exist any more, and dialling
// it is a handshake sent to whoever holds that address now.
func TestRosterPeersExpire(t *testing.T) {
	s := &State{dir: t.TempDir()}
	now := time.Now()

	fresh := remembered(now.Add(-time.Hour), "ZnJlc2g=")
	stale := remembered(now.Add(-RosterPeerTTL-time.Hour), "c3RhbGU=")
	if err := s.SetRosterPeers("abc234", []RosterPeer{fresh, stale}); err != nil {
		t.Fatal(err)
	}

	out := s.RosterPeers("abc234", now)
	if len(out) != 1 || out[0].DevicePub != "ZnJlc2g=" {
		t.Errorf("expiry kept %+v", out)
	}
}

// Freshest first and bounded, so a long-lived mesh does not accumulate an
// unbounded file of devices nobody has seen in a year.
func TestRosterPeersAreBoundedAndOrdered(t *testing.T) {
	s := &State{dir: t.TempDir()}
	now := time.Now()

	var in []RosterPeer
	for i := 0; i < MaxRosterPeers+20; i++ {
		// The later ones are the more recently seen.
		in = append(in, remembered(now.Add(-time.Duration(MaxRosterPeers+20-i)*time.Minute),
			string(rune('a'+i%26))+"AAAA"))
	}
	if err := s.SetRosterPeers("abc234", in); err != nil {
		t.Fatal(err)
	}

	out := s.RosterPeers("abc234", now)
	if len(out) != MaxRosterPeers {
		t.Fatalf("kept %d peers, want %d", len(out), MaxRosterPeers)
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].Seen < out[i].Seen {
			t.Fatalf("not ordered freshest first at %d", i)
		}
	}
	// The one dropped must be the oldest, not whichever happened to be last.
	if out[len(out)-1].Seen <= in[0].Seen {
		t.Error("kept an older peer over a newer one")
	}
	// And the caller's slice is still in the order it was built in. Sorting it
	// in place is invisible until something downstream depends on that order,
	// and the roster snapshot this is called with is sorted by name.
	if in[0].Seen != now.Add(-time.Duration(MaxRosterPeers+20)*time.Minute).Unix() {
		t.Error("the caller's slice was reordered underneath it")
	}
}

// A half-written or hand-edited entry must be skipped rather than turned into a
// peer with no keys, which would reach the data plane as a nonsense handshake.
func TestRosterPeersSkipsUnusableEntries(t *testing.T) {
	s := &State{dir: t.TempDir()}
	now := time.Now()

	good := remembered(now, "Z29vZA==")
	noDev := remembered(now, "")
	noWG := remembered(now, "bm93Zw==")
	noWG.WGPub = ""
	if err := s.SetRosterPeers("abc234", []RosterPeer{good, noDev, noWG}); err != nil {
		t.Fatal(err)
	}

	out := s.RosterPeers("abc234", now)
	if len(out) != 1 || out[0].DevicePub != "Z29vZA==" {
		t.Errorf("kept %+v", out)
	}
}

// A missing file is a first run, not a failure: refusing to start over it would
// be worse than the cold start it is trying to avoid.
func TestNoRememberedPeersIsNotAnError(t *testing.T) {
	s := &State{dir: t.TempDir()}
	if got := s.RosterPeers("abc234", time.Now()); got != nil {
		t.Errorf("a first run produced %v", got)
	}
}

// The name goes into a path. A network id arrives from a config file, and a
// separator in one would otherwise choose where the file lands.
func TestNetworkNamesCannotEscapeTheStateDirectory(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "a/b", "", "..", "A/B\\C"} {
		got := safeNetworkName(bad)
		for _, r := range got {
			if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7') || r == '-') {
				t.Errorf("%q produced %q, which contains %q", bad, got, r)
			}
		}
		if got == "" {
			t.Errorf("%q produced an empty name", bad)
		}
	}
}
