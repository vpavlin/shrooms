package mesh

import (
	"strings"
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

	// Establish the failure this guards against: unmodified, a large announce
	// is refused, and refused with an error a caller can recognise.
	if _, err := control.Seal(nk, 1, id.DevicePriv, build(8)); err == nil {
		t.Fatal("an oversized announce sealed; this test proves nothing")
	} else if !strings.Contains(err.Error(), "padded size") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Trimming from the end must always reach something that fits, because
	// zero endpoints certainly fits — a node with no dialable address is still
	// worth announcing, since peers can reach it once it speaks first.
	a := build(8)
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
