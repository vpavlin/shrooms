//go:build linux

package wg

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"

	"golang.zx2c4.com/wireguard/tun"
)

// DefaultMTU is 1280, the IPv6 minimum, and that is a floor rather than a
// choice.
//
// WireGuard's own overhead is 60 bytes over IPv4 and 80 over IPv6, which is why
// 1420 is the usual answer and was ours. It is wrong once a packet goes through
// a relay, which adds the control header that lets relay frames share the
// WireGuard socket plus the forward header naming both ends — 86 bytes on top
// of WireGuard's own.
//
// So a relayed packet on the wire is the tunnel payload plus 146 bytes: 32 for
// WireGuard, 5 for the control header, 81 for the forward header, 8 for UDP and
// 20 for an IPv4 underlay.
//
// **This does not make relayed transfers work, and cannot.** The overlay is
// IPv6, so the interface may not go below 1280 — the kernel refuses to add an
// IPv6 address to anything smaller, with "RTNETLINK answers: Invalid argument".
// A minimum-size overlay packet is therefore 1426 bytes on the wire, and a
// phone on mobile data commonly sits behind a path carrying about 1250. There
// is no MTU that satisfies both.
//
// Measured, after a 100MB download over a relayed tunnel stopped at 1.5KB.
// Pinging the far end at increasing sizes put the break between a 1098-byte
// tunnel packet, which arrived, and 1128, which did not. The handshake, the
// request and the small responses all fit; the first full-size data packet did
// not, and nothing anywhere reported an error.
//
// The fix is path MTU discovery, not a constant: emit ICMPv6 Packet Too Big
// when a packet will not fit the path a peer is currently reached by, and let
// the sending stack cache a smaller path MTU for that destination. That keeps
// full size for direct peers and shrinks only what is relayed — which is
// exactly the right shape, since which peers are relayed changes at runtime.
// See docs/relay-mtu.md.
//
// 1280 in the meantime because it is the largest value that is legal
// everywhere, matches what the Android client already used, and makes the
// direct path behave the same as the relayed one rather than differently.
const DefaultMTU = 1280

// CreateTUN opens a TUN device and assigns the overlay address and route.
//
// Requires CAP_NET_ADMIN. Interface configuration shells out to `ip` rather
// than pulling in a netlink dependency — this is a handful of one-shot commands
// at startup, not a hot path.
func CreateTUN(name string, addr netip.Addr, prefix netip.Prefix, mtu int, v4Addr netip.Addr, v4Prefix netip.Prefix) (tun.Device, error) {
	if mtu == 0 {
		mtu = DefaultMTU
	}

	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("create tun %s: %w", name, err)
	}

	// CreateTUN may pick a different name than requested.
	actual, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("tun name: %w", err)
	}

	steps := [][]string{
		{"link", "set", "dev", actual, "mtu", strconv.Itoa(mtu)},
		{"address", "add", addr.String() + "/128", "dev", actual},
		{"link", "set", "dev", actual, "up"},
		// Route the whole mesh prefix at the interface; per-peer AllowedIPs
		// inside WireGuard decides which peer each packet actually goes to.
		{"route", "add", prefix.String(), "dev", actual},
	}
	if v4Addr.IsValid() {
		// The synthetic IPv4 side (ADR-021). The MTU is 20 bytes lower than the
		// interface's, because translating to IPv6 grows every packet by
		// exactly one header's difference — carried on the route, since an
		// interface has only one MTU and the IPv6 side must keep the full one.
		steps = append(steps,
			[]string{"address", "add", v4Addr.String() + "/32", "dev", actual},
			[]string{"route", "add", v4Prefix.String(), "dev", actual,
				"mtu", strconv.Itoa(mtu - 20)},
		)
	}
	for _, args := range steps {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			// The route may already exist after a restart; that is not fatal.
			if args[0] == "route" {
				continue
			}
			dev.Close()
			return nil, fmt.Errorf("ip %v: %w: %s", args, err, out)
		}
	}
	return dev, nil
}
