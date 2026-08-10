package mesh

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
)

// An announce is padded to a fixed size and Seal refuses anything larger, so a
// node with enough addresses stopped announcing altogether: it logged a failure
// once per interval and disappeared from every roster, which reads as a network
// fault rather than as a node with too many interfaces.
//
// The real limit was four endpoints while candidates() capped at eight. Two
// reflexive addresses and three local ones is an ordinary laptop with docker
// installed.
func TestAnnounceFitsByTrimmingEndpoints(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	now := time.Now()

	build := func(n int) *control.Announce {
		eps := make([]string, n)
		for i := range eps {
			eps[i] = "203.0.113.42:51820"
		}
		return &control.Announce{
			Kind: control.KindAnnounce, DevicePub: id.DevicePub, WGPub: id.WGPub[:],
			Name: "a-node-with-a-long-enough-name", Endpoints: eps,
			Seq: 4294967295, Timestamp: now.Unix(), Relay: true,
		}
	}

	// Seal escalates to the next padding size, so the count that overflows is
	// larger than it was — the original failure appeared at five endpoints
	// against a fixed 512. Find where it still overflows rather than hardcoding
	// a number that moves whenever a padding size is added.
	overflow := 0
	for n := 1; n <= 64; n++ {
		if _, err := control.Seal(nk, 1, id.DevicePriv, build(n)); err != nil {
			overflow = n
			break
		}
	}
	if overflow == 0 {
		t.Skip("no endpoint count overflows the largest padding; nothing to trim")
	}
	t.Logf("overflows at %d endpoints", overflow)

	// Trimming from the end must always reach something that fits, because zero
	// endpoints certainly fits — a node with no dialable address is still worth
	// announcing, since peers can reach it once it speaks first.
	a := build(overflow)
	var err error
	for {
		_, err = control.Seal(nk, 1, id.DevicePriv, a)
		if err == nil || len(a.Endpoints) == 0 {
			break
		}
		a.Endpoints = a.Endpoints[:len(a.Endpoints)-1]
	}
	if err != nil {
		t.Fatalf("could not fit an announce even with no endpoints: %v", err)
	}
	if len(a.Endpoints) == 0 {
		t.Error("trimmed away every endpoint; the budget is smaller than it should be")
	}
	t.Logf("fits with %d endpoints", len(a.Endpoints))
}
