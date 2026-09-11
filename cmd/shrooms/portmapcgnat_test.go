package main

import (
	"net/netip"
	"testing"
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
