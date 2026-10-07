package mesh

import (
	"testing"
	"time"
)

// Every rejected announce was logged as a warning. Store replays a few old
// ones on every reconnect, which produced dozens of warnings about nothing
// and hid the case that matters: a device refused for minutes on end.

func TestAReplayBurstIsNotWarnedAbout(t *testing.T) {
	var r replayStats
	dev := []byte{1, 2, 3}
	now := time.Now()
	for i := 0; i < 50; i++ {
		if r.reject(dev, uint64(100+i), now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("warned on rejection %d, within a minute of the first", i)
		}
	}
}

func TestADeviceRefusedForMinutesIsWarnedAboutOnce(t *testing.T) {
	var r replayStats
	dev := []byte{1, 2, 3}
	now := time.Now()

	r.reject(dev, 10, now)
	if r.reject(dev, 11, now.Add(ReplayWarnAfter-time.Second)) {
		t.Fatal("warned before ReplayWarnAfter had passed")
	}
	if !r.reject(dev, 12, now.Add(ReplayWarnAfter)) {
		t.Fatal("not warned once the streak had lasted ReplayWarnAfter")
	}
	if r.reject(dev, 13, now.Add(2*ReplayWarnAfter)) {
		t.Fatal("warned a second time for the same streak")
	}

	// An acceptance ends the streak; a later one is a new case.
	r.accept(dev, now.Add(3*ReplayWarnAfter))
	later := now.Add(4 * ReplayWarnAfter)
	r.reject(dev, 5, later)
	if !r.reject(dev, 6, later.Add(ReplayWarnAfter)) {
		t.Fatal("a new streak after an acceptance was not warned about")
	}
}
