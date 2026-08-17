package control

import (
	"encoding/hex"
	"testing"
)

func announceAt(devicePub []byte, seq uint64) *Announce {
	return &Announce{Kind: KindAnnounce, DevicePub: devicePub, Seq: seq}
}

// The guard accepts the first announce it sees from a device, because on a
// fresh start it has nothing to compare against. That is correct in itself and
// wrong across a restart: the marks were memory-only, so every restart made
// every peer "first seen" again, and an announce captured off the bus and
// replayed inside the clock-skew window was accepted as current.
func TestLoadedMarksRejectAReplayAfterRestart(t *testing.T) {
	dev := []byte{0xab, 0xcd, 0xef}
	key := hex.EncodeToString(dev)

	// Before the restart: this node had accepted seq 500.
	old := NewReplayGuard()
	if !old.Accept(announceAt(dev, 500)) {
		t.Fatal("a fresh guard refused a first announce")
	}
	marks := old.Snapshot()
	if marks[key] != 500 {
		t.Fatalf("snapshot has %d for the device, want 500", marks[key])
	}

	// After the restart, seeded from disk.
	fresh := NewReplayGuard()
	fresh.Load(marks)

	if fresh.Accept(announceAt(dev, 499)) {
		t.Error("a captured older announce was accepted after a restart")
	}
	if fresh.Accept(announceAt(dev, 500)) {
		t.Error("the same announce was accepted twice across a restart")
	}
	if !fresh.Accept(announceAt(dev, 501)) {
		t.Error("a genuinely newer announce was refused")
	}
}

// Without the marks, the replay lands — which is what makes the test above
// worth having rather than a tautology.
func TestWithoutMarksTheReplayIsAccepted(t *testing.T) {
	dev := []byte{0x01, 0x02}
	fresh := NewReplayGuard()
	if !fresh.Accept(announceAt(dev, 499)) {
		t.Error("expected the old behaviour: first announce accepted whatever its seq")
	}
}

// Load merges upwards only. Anything this process has accepted in its own
// lifetime is at least as new as a file written before it started, and taking
// the lower number would reopen the window.
func TestLoadNeverLowersAMark(t *testing.T) {
	dev := []byte{0x09}
	key := hex.EncodeToString(dev)

	g := NewReplayGuard()
	g.Accept(announceAt(dev, 900))
	g.Load(map[string]uint64{key: 100}) // a stale file

	if g.Accept(announceAt(dev, 200)) {
		t.Error("a stale file lowered the mark and let an old announce through")
	}
	if seq, _ := g.Seq(dev); seq != 900 {
		t.Errorf("mark = %d, want the higher 900", seq)
	}
}

// A device that is forgotten — revoked, or gone long enough to be pruned —
// leaves no mark behind, so a genuinely rebuilt device with the same key can
// come back rather than being locked out by a counter it no longer has.
func TestForgettingClearsTheMark(t *testing.T) {
	dev := []byte{0x77}
	g := NewReplayGuard()
	g.Accept(announceAt(dev, 42))
	g.Forget(dev)

	if _, ok := g.Seq(dev); ok {
		t.Error("a forgotten device kept its mark")
	}
	if len(g.Snapshot()) != 0 {
		t.Error("a forgotten device is still in the snapshot that gets written")
	}
}
