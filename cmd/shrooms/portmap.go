package main

import (
	"context"
	"log/slog"

	"github.com/vpavlin/shrooms/internal/portmap"
)

// Asking the router for a way in (ADR-024).
//
// A node behind NAT learns its public address by reflection — a peer's pong
// echoes what that peer saw — which needs a peer outside the NAT. On a mesh
// whose members are all in one house there is no such peer, so no node ever
// learns its own address, every announced candidate is a LAN address, and the
// mesh works from the sofa and not from the street. The router knows the
// answer and there is a standard way to ask.

// nudgeRemap asks every mesh's mapper to forget what it has and ask again.
//
// Called when the underlay changes. Never blocks: this runs on the watchdog's
// tick, which also drives restarts, and a mapper that is busy talking to a
// router must not be able to hold that up. A nudge already pending is as good
// as two, because what it triggers is idempotent.
func nudgeRemap(instances []*instance) {
	for _, in := range instances {
		if in == nil || in.remap == nil {
			continue
		}
		select {
		case in.remap <- struct{}{}:
		default:
		}
	}
}

// keepMapped runs the mesh's mapper for this instance (mesh.KeepMapped).
func keepMapped(ctx context.Context, _ *slog.Logger, in *instance) {
	in.mesh.KeepMapped(ctx, func() *portmap.Client { return &portmap.Client{} }, in.label, in.listenPort, in.remap)
}
