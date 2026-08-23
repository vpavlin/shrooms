package dns

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// Register points the host's resolver at ours for one domain.
//
// Without this the resolver answers correctly and nothing ever asks it, which
// is exactly how it shipped: the daemon logged "name resolution up" while
// `ping vps.mesh` failed, because the two facts are unrelated. Serving DNS and
// being *reached* are different things and only one of them was done.
//
// Scoped to the domain — `~mesh` in resolvectl's notation — so only mesh names
// come here. The system's own resolvers keep everything else, which matters:
// this must never become the device's general resolver.
//
// Best-effort by design. A host without systemd-resolved, or a container
// without the D-Bus socket, is a reason to log and carry on rather than to
// refuse to run a VPN.
func Register(ctx context.Context, iface string, addr netip.Addr, suffix string) error {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return fmt.Errorf("no resolvectl: %w", err)
	}
	suffix = strings.Trim(suffix, ".")
	if suffix == "" {
		suffix = DefaultSuffix
	}

	// Both, in order: the address alone gives the link a resolver but no reason
	// to be consulted, and the domain alone points at nothing.
	steps := [][]string{
		{"dns", iface, addr.String()},
		{"domain", iface, "~" + suffix},
	}
	for _, args := range steps {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(c, "resolvectl", args...).CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("resolvectl %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Unregister drops the settings again.
//
// Rarely needed — resolved forgets a link when the interface disappears — but
// a daemon that stops without its interface going away would otherwise leave
// the host pointing at a resolver that is no longer listening.
func Unregister(iface string) {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(c, "resolvectl", "revert", iface).Run()
}
