package main

import (
	"net/netip"
	"testing"
	"time"
)

// A port mapping is only worth announcing if a peer could dial it.
//
// internal/portmap says so at the point it returns one: behind carrier-grade
// NAT "the home router happily maps a port on its own RFC 1918 WAN address, and
// the result looks like a success". keepMapped announced it anyway.
//
// On 2026-09-11 that put a laptop and a pi5 three metres apart, both behind one
// CGNAT router, both announcing 10.77.57.173, and neither able to reach the
// other — while a 5ms LAN path sat unused between them. The address is
// announced first, both ends try it, the router hairpins enough for WireGuard
// to roam the peer's endpoint onto it, and yieldRoam then keeps it there.
func TestUsableMappingRejectsWhatNobodyCanDial(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
		why  string
	}{
		{"10.77.57.173", false, "the one from the field: RFC 1918 WAN behind CGNAT"},
		{"192.168.1.1", false, "RFC 1918"},
		{"172.16.4.9", false, "RFC 1918"},
		{"100.64.0.1", false, "100.64/10 is carrier shared space, and not IsPrivate"},
		{"100.127.255.254", false, "the top of 100.64/10"},
		{"127.0.0.1", false, "loopback"},
		{"169.254.3.4", false, "link-local"},
		{"0.0.0.0", false, "unspecified"},
		{"224.0.0.1", false, "multicast"},

		{"85.160.39.54", true, "a real public address — this laptop's, when the router gives one"},
		{"128.140.55.128", true, "the vps"},
		{"100.128.0.1", true, "just above 100.64/10, and ordinary public space"},
		{"99.255.255.255", true, "just below it"},
	} {
		a, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		if got := usableMapping(a); got != tc.want {
			t.Errorf("usableMapping(%s) = %v, want %v — %s", tc.addr, got, tc.want, tc.why)
		}
	}
}

// An address that never parsed is not a mapping either.
func TestUsableMappingRejectsTheZeroValue(t *testing.T) {
	if usableMapping(netip.Addr{}) {
		t.Error("the zero address was treated as a usable mapping")
	}
}

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
