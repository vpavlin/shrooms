package mesh

import (
	"context"
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

// KeepMapped maintains a port mapping for this mesh's listen port (ADR-024),
// announcing each result, until ctx ends. listenPort says the port now (it can
// move under us); a send on remap says the network changed. client is asked
// for each round's client, so a phone can name the router of the network it is
// on now (a zero Gateway discovers it).
//
// Shared by the daemon and the phone (ADR-075): the phone never asked, and
// behind a home router that would have granted a mapping, office machines
// behind carrier-grade NAT could not reach it except through the relay.
//
// One per instance rather than one per node: each mesh has its own WireGuard
// device on its own port (ADR-015), and a mapping is for a port.
//
// Failure is not an error here. A router that says nothing leaves the node
// exactly where it was, with advertise available for anyone who wants to
// configure it by hand — so this logs once at startup and then stays quiet
// rather than reporting the same refusal every half hour.
func (m *Mesh) KeepMapped(ctx context.Context, client func() *portmap.Client, label string, listenPort func() uint16, remap <-chan struct{}) {
	log := m.log
	announced := false
	var have netip.AddrPort // the mapping announced now
	// The external port to ask for. Starts as our own, which routers usually
	// honour, and moves only when the one we were given turns out to belong to
	// somebody else as well.
	port := listenPort()
	want := port

	for {
		// The port can move under us (moveEphemeralPorts), and a mapping for
		// the old one forwards to a socket that no longer exists. Start over
		// for the new one, and say what we get, since it will be new.
		if now := listenPort(); now != port {
			port, want, announced = now, now, false
		}

		// A mapping a peer also claims is not a mapping. The router gave the
		// same external port to two machines — which it is entitled to get
		// wrong, and neither node can tell from its side — so ask for a
		// different one rather than advertise an address that works for at most
		// one of us.
		//
		// Checked before renewing rather than on discovery, because renewal is
		// already the loop that talks to the router, and a collision is not
		// urgent: until it clears, both nodes relay, which works.
		if m.MappedIsContested() {
			next := want + 1
			if next < port || next == 0 {
				next = port + 1
			}
			log.Info("the router gave us a port a peer also claims; asking for another",
				"mesh", label, "contested", want, "asking", next)
			want = next
			announced = false // say what we get, since it will be new
		}

		mp, err := client().MapTo(ctx, port, want, portmap.DefaultLifetime)
		wait := mapRetry
		switch {
		case err != nil:
			if !announced {
				// Once, at info: on most home networks this is the normal
				// outcome and it is not a fault, but the person wondering why
				// their node is unreachable needs to be able to find out that
				// the router was asked and declined.
				log.Info("no port mapping from the router",
					"mesh", label, "port", port, "err", err)
				announced = true
			}
			m.SetMapped(netip.AddrPort{})
		default:
			if !announced || have != mp.External {
				log.Info("port mapped by the router",
					"mesh", label, "external", mp.External.String(),
					"proto", mp.Proto, "lifetime", mp.Lifetime.Round(time.Second))
				announced = true
			}
			if !usableMapping(mp.External.Addr()) {
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
						"mesh", label, "external", mp.External.String(),
						"why", "carrier-grade NAT: no peer can reach this")
					announced = true
				}
				have = netip.AddrPort{}
				m.SetMapped(netip.AddrPort{})
				wait = max(mp.Lifetime/2, mapFloor)
				break
			}
			have = mp.External
			m.SetMapped(mp.External)
			// Half the granted lifetime, which is the usual soft-state
			// discipline: late enough to be cheap, early enough that one lost
			// packet does not cost the mapping.
			// Follow the router rather than our own request: it is free to
			// ignore the suggestion, and the mapping it returned is the truth.
			// Without this a node that was refused its choice would ask for the
			// same wrong port forever.
			want = mp.External.Port()
			wait = max(mp.Lifetime/2, mapFloor)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-remap:
			// The ground moved. Whatever the last router said describes a
			// network this node has left, and announcing it is not merely
			// stale — it is the one address in the list guaranteed not to
			// work, and it is announced first.
			//
			// Cleared before asking rather than after: the request can fail,
			// or take a moment, and a peer reading the announce in between
			// should see an address short by one rather than a wrong one.
			log.Info("the network changed; asking the new router for a mapping",
				"mesh", label, "dropping", have.String())
			have = netip.AddrPort{}
			m.SetMapped(netip.AddrPort{})
			// Say what the new one answers, whatever it is.
			announced = false
			// And ask for our own port again. `want` holds whatever the last
			// router assigned, which means nothing to this one.
			port = listenPort()
			want = port
		}
	}
}
