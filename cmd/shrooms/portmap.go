package main

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

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

// mapRetry is how long to wait before asking again after a refusal.
//
// Long, because the overwhelmingly common refusal is a router that does not
// speak either protocol, or has them switched off, and that answer will not
// change today. Short enough that a router which was merely rebooting is picked
// up without anyone noticing.
const mapRetry = 30 * time.Minute

// mapFloor bounds how often a mapping is renewed however short a lifetime the
// router grants. A router handing out ten-second leases would otherwise have us
// talking to it constantly.
const mapFloor = time.Minute

// keepMapped maintains a port mapping for one mesh's listen port, handing each
// result to the mesh so it can announce it.
//
// One per instance rather than one per node: each mesh has its own WireGuard
// device on its own port (ADR-015), and a mapping is for a port.
//
// Failure is not an error here. A router that says nothing leaves the node
// exactly where it was, with advertise available for anyone who wants to
// configure it by hand — so this logs once at startup and then stays quiet
// rather than reporting the same refusal every half hour.
func keepMapped(ctx context.Context, log *slog.Logger, in *instance) {
	c := &portmap.Client{}
	announced := false

	for {
		m, err := c.Map(ctx, in.port, portmap.DefaultLifetime)
		wait := mapRetry
		switch {
		case err != nil:
			if !announced {
				// Once, at info: on most home networks this is the normal
				// outcome and it is not a fault, but the person wondering why
				// their node is unreachable needs to be able to find out that
				// the router was asked and declined.
				log.Info("no port mapping from the router",
					"mesh", in.label, "port", in.port, "err", err)
				announced = true
			}
			in.mesh.SetMapped(netip.AddrPort{})
		default:
			if !announced || in.mapped != m.External {
				log.Info("port mapped by the router",
					"mesh", in.label, "external", m.External.String(),
					"proto", m.Proto, "lifetime", m.Lifetime.Round(time.Second))
				announced = true
			}
			in.mapped = m.External
			in.mesh.SetMapped(m.External)
			// Half the granted lifetime, which is the usual soft-state
			// discipline: late enough to be cheap, early enough that one lost
			// packet does not cost the mapping.
			wait = max(m.Lifetime/2, mapFloor)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
