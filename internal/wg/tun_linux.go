//go:build linux

package wg

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"

	"golang.zx2c4.com/wireguard/tun"
)

// DefaultMTU is 1420: WireGuard's overhead is 60 bytes over IPv4 and 80 over
// IPv6, and 1420 survives an IPv6 underlay. Raising it globally is a common
// cause of "hangs on large transfers" via PMTUD blackholes.
const DefaultMTU = 1420

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
