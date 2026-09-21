package dns

import (
	"os"
	"strings"
	"testing"
)

// The installer writes a host-side registrar in shell, because a container
// cannot reach systemd-resolved itself. That script cannot call routingDomains,
// so it restates two constants — and a shell script on somebody's disk is the
// one copy no compiler and no test would otherwise check.
//
// The failure it guards against is silent: change DefaultSuffix here and every
// machine already installed keeps registering the old routing domain, names
// stop resolving, and nothing anywhere says why. This turns that into a failing
// test in the commit that causes it.
func TestInstallerRegistersTheSuffixesWeServe(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Skipf("no installer to check: %v", err)
	}
	script := string(raw)

	// The fallback when the daemon reports no suffix, which has to match what
	// Register itself falls back to.
	if want := "suffix=" + DefaultSuffix; !strings.Contains(script, want) {
		t.Errorf("scripts/install.sh does not default to %q — expected a line containing %q.\n"+
			"The registrar it writes and internal/dns must agree on the suffix.", DefaultSuffix, want)
	}
	// The legacy suffix, added alongside whatever is configured.
	if want := `"~` + LegacySuffix + `"`; !strings.Contains(script, want) {
		t.Errorf("scripts/install.sh does not register %q — expected %q.\n"+
			"The resolver answers it, so leaving it unregistered makes those names "+
			"answerable by `dig @…` and dead everywhere else.", LegacySuffix, want)
	}
}
