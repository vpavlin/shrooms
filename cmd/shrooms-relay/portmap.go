package main

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/vpavlin/shrooms/internal/portmap"
)

// Asking the router to forward the relay's port.
//
// Not NAT traversal, and it is worth being precise about the difference. Hole
// punching works because both ends try to reach each other at the same moment
// while something coordinates them; a relay is contacted by strangers at
// arbitrary times and cannot send an outbound packet to somebody it does not
// know exists. There is nothing to punch through. A relay has to be genuinely
// reachable.
//
// What this does instead is ask the router, over PCP or NAT-PMP, to forward a
// port — the same thing you would otherwise do by hand in a web interface. When
// the router agrees, running a relay at home costs no configuration at all.
//
// Off unless asked for. On hosted infrastructure the port is already forwarded
// and the gateway is a container bridge that either ignores this or maps
// something meaningless.

// mapRenewAfter is how much of a mapping's life to use before renewing.
//
// Half, which is the ordinary way to hold soft state: late enough to be cheap,
// early enough that one lost packet does not drop the mapping and take the
// relay off the internet until the next attempt.
const mapRenewAfter = 2

func holdPortMapping(ctx context.Context, log *slog.Logger, port uint16) {
	var c portmap.Client
	var last netip.AddrPort

	for {
		m, err := c.Map(ctx, port, portmap.DefaultLifetime)
		wait := portmap.DefaultLifetime / mapRenewAfter
		switch {
		case err != nil:
			log.Warn("could not ask the router to forward the relay port; "+
				"forward it by hand, or the relay is reachable only from this network",
				"port", port, "err", err)
			// Retried, but not eagerly: a router that does not speak PCP or
			// NAT-PMP will never start, and asking every thirty seconds for the
			// life of the process achieves nothing but noise.
			wait = 10 * time.Minute
		case m.External != last:
			last = m.External
			log.Info("router is forwarding the relay port",
				"external", m.External, "via", m.Proto, "for", m.Lifetime)
			// A mapping onto a private address is what carrier-grade NAT looks
			// like from inside: the router cheerfully forwards a port on its
			// own RFC 1918 WAN address and reports success, and nothing on the
			// internet can reach it. Said plainly, because the alternative is
			// an operator who believes they are running a relay and is not.
			if !m.External.Addr().IsValid() || isPrivate(m.External.Addr()) {
				log.Warn("the router mapped a private address, which means this "+
					"connection is behind carrier-grade NAT — the relay is not "+
					"reachable from the internet and cannot be made so from here",
					"external", m.External)
			} else {
				log.Info("check it from outside this network",
					"probe", "shrooms-relay -probe "+m.External.String())
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// isPrivate reports an address that cannot be reached from the internet.
func isPrivate(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsUnspecified() ||
		// 100.64.0.0/10, the range carriers use for their own NAT. A router
		// reporting one of these has been given it by the carrier and is not
		// on the internet either.
		(a.Is4() && a.As4()[0] == 100 && a.As4()[1]&0xc0 == 64)
}
