package main

import (
	"testing"
	"time"
)

// A move must reach the port mapper.
//
// The daemon has always logged "the network changed underneath us" and told
// nothing, so each mesh kept announcing the external address of the router it
// had left — for up to an hour, first in the list, and guaranteed not to work.
// Watched on 2026-09-11: a laptop that changed networks went on offering
// 10.77.57.173 on all three meshes, and pi5 could not reach it until the daemon
// was restarted by hand.
func TestNudgeRemapReachesEveryMesh(t *testing.T) {
	a := &instance{label: "home", remap: make(chan struct{}, 1)}
	b := &instance{label: "office", remap: make(chan struct{}, 1)}

	nudgeRemap([]*instance{a, b})

	for _, in := range []*instance{a, b} {
		select {
		case <-in.remap:
		default:
			t.Errorf("%s was not told the network changed", in.label)
		}
	}
}

// It runs on the watchdog's tick, which also drives restarts, so it must never
// block — not on a mesh whose mapper is mid-conversation with a router, and not
// on one that has no channel because port mapping is switched off.
func TestNudgeRemapNeverBlocks(t *testing.T) {
	full := &instance{label: "busy", remap: make(chan struct{}, 1)}
	full.remap <- struct{}{}              // a nudge already pending
	off := &instance{label: "no-portmap"} // remap is nil

	done := make(chan struct{})
	go func() {
		nudgeRemap([]*instance{full, off, nil})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nudgeRemap blocked; it runs on the watchdog tick and must not")
	}

	// The pending nudge is still there, and is as good as two.
	if len(full.remap) != 1 {
		t.Errorf("pending nudges = %d, want 1", len(full.remap))
	}
}
