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
// Every suffix the resolver answers, not only the configured one. The server
// takes Also — LegacySuffix, so a change of default does not break every ssh
// config on the same day — and registering one of the two left the other
// answerable by `dig @…` and dead everywhere else, which is the same "serving
// but never asked" failure this function exists to close.
func Register(ctx context.Context, iface string, addr netip.Addr, suffix string, also ...string) error {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return fmt.Errorf("no resolvectl: %w", err)
	}

	// Both, in order: the address alone gives the link a resolver but no reason
	// to be consulted, and the domain alone points at nothing.
	steps := [][]string{
		{"dns", iface, addr.String()},
		append([]string{"domain", iface}, routingDomains(suffix, also)...),
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

// routingDomains renders the `~domain` arguments for one link.
//
// Deduplicated and ordered, because a config already using the legacy suffix
// would otherwise register it twice — harmless to resolved, and confusing in
// `resolvectl status` and in anything that prints this command for a human to
// run.
func routingDomains(suffix string, also []string) []string {
	out := make([]string, 0, len(also)+1)
	seen := map[string]bool{}
	for _, s := range append([]string{suffix}, also...) {
		s = strings.Trim(s, ".")
		if s == "" {
			s = DefaultSuffix
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, "~"+s)
	}
	return out
}

// RegisterCommand is what Register does, as a line somebody can run.
//
// Printed by the daemon when registration fails and by `shrooms status` for as
// long as it has not happened — the two used to build the string separately and
// only one of them knew about the second suffix. A container install cannot
// register at all (no resolvectl in the image), so this is the actual
// instruction on the most common install, not a debugging aid.
// The address is a string rather than a netip.Addr: the daemon has one parsed
// and the CLI has only what the status payload carried, and re-parsing a value
// solely to print it is how a command line acquires an error path it does not
// need.
func RegisterCommand(prefix, iface, addr, suffix string, also ...string) string {
	return fmt.Sprintf("%sresolvectl dns %s %s && %sresolvectl domain %s %s",
		prefix, iface, addr, prefix, iface,
		strings.Join(quoted(routingDomains(suffix, also)), " "))
}

// quoted wraps each domain so a shell does not read `~` as a home directory.
func quoted(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		out = append(out, "'"+d+"'")
	}
	return out
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
