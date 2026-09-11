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

// usableMapping reports whether an address a router handed back is one another
// member could actually dial.
//
// A mapping is only worth announcing if the external address is globally
// routable. Behind carrier-grade NAT it is not: the router maps a port on its
// own RFC 1918 or 100.64/10 WAN address and reports success, and the mapping is
// real — it simply cannot be reached from anywhere that matters.
//
// 100.64.0.0/10 is checked explicitly because netip does not consider it
// private: it is the shared address space carriers use for exactly this, so it
// is the single most likely thing to come back from a CGNAT router.
func usableMapping(a netip.Addr) bool {
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() ||
		a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

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
	// The external port to ask for. Starts as our own, which routers usually
	// honour, and moves only when the one we were given turns out to belong to
	// somebody else as well.
	want := in.port

	for {
		// A mapping a peer also claims is not a mapping. The router gave the
		// same external port to two machines — which it is entitled to get
		// wrong, and neither node can tell from its side — so ask for a
		// different one rather than advertise an address that works for at most
		// one of us.
		//
		// Checked before renewing rather than on discovery, because renewal is
		// already the loop that talks to the router, and a collision is not
		// urgent: until it clears, both nodes relay, which works.
		if in.mesh.MappedIsContested() {
			next := want + 1
			if next < in.port || next == 0 {
				next = in.port + 1
			}
			log.Info("the router gave us a port a peer also claims; asking for another",
				"mesh", in.label, "contested", want, "asking", next)
			want = next
			announced = false // say what we get, since it will be new
		}

		m, err := c.MapTo(ctx, in.port, want, portmap.DefaultLifetime)
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
			if !usableMapping(m.External.Addr()) {
				// The router mapped a port on its own private WAN address,
				// which internal/portmap warns about in as many words: behind
				// carrier-grade NAT this "looks like a success" and is not
				// proof of reachability. Announcing it is worse than useless.
				//
				// Worse, because every peer behind the SAME carrier NAT
				// announces the SAME address, tries it, and the router
				// hairpins just enough for WireGuard to roam the peer's
				// endpoint onto it — after which yieldRoam stops us writing
				// the working LAN address back and the tunnel sits stale.
				// Seen on 2026-09-11: a laptop and a pi5 three metres apart,
				// both announcing 10.77.57.173, neither able to reach the
				// other, with a 5ms LAN path between them the whole time.
				if !announced {
					log.Info("the router mapped a port on a private address; not announcing it",
						"mesh", in.label, "external", m.External.String(),
						"why", "carrier-grade NAT: no peer can reach this")
					announced = true
				}
				in.mapped = netip.AddrPort{}
				in.mesh.SetMapped(netip.AddrPort{})
				wait = max(m.Lifetime/2, mapFloor)
				break
			}
			in.mapped = m.External
			in.mesh.SetMapped(m.External)
			// Half the granted lifetime, which is the usual soft-state
			// discipline: late enough to be cheap, early enough that one lost
			// packet does not cost the mapping.
			// Follow the router rather than our own request: it is free to
			// ignore the suggestion, and the mapping it returned is the truth.
			// Without this a node that was refused its choice would ask for the
			// same wrong port forever.
			want = m.External.Port()
			wait = max(m.Lifetime/2, mapFloor)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-in.remap:
			// The ground moved. Whatever the last router said describes a
			// network this node has left, and announcing it is not merely
			// stale — it is the one address in the list guaranteed not to
			// work, and it is announced first.
			//
			// Cleared before asking rather than after: the request can fail,
			// or take a moment, and a peer reading the announce in between
			// should see an address short by one rather than a wrong one.
			log.Info("the network changed; asking the new router for a mapping",
				"mesh", in.label, "dropping", in.mapped.String())
			in.mapped = netip.AddrPort{}
			in.mesh.SetMapped(netip.AddrPort{})
			// Say what the new one answers, whatever it is.
			announced = false
			// And ask for our own port again. `want` holds whatever the last
			// router assigned, which means nothing to this one.
			want = in.port
		}
	}
}
