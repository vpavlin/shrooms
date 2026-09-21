package state

import (
	"os"
	"strings"
	"testing"
)

// scripts/install.sh decides whether `--force init` may replace a config by
// grepping it for the placeholder key, which is how it tells a machine that ran
// `prepare` from one that is already in a mesh.
//
// It cannot call configHasNoKey, so it carries the literal. If KeyPlaceholder
// ever changes, that grep stops matching, every prepared config looks like a
// member's, and --force init refuses on precisely the machines it exists to
// serve. It fails safe and it fails wrong, so pin it here.
func TestInstallerKnowsThePlaceholder(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Skipf("no installer to check: %v", err)
	}
	if !strings.Contains(string(raw), KeyPlaceholder) {
		t.Errorf("scripts/install.sh does not contain KeyPlaceholder (%q).\n"+
			"It greps for that string to tell a prepared config from a member's; "+
			"changing the constant without the script makes `--force init` refuse "+
			"on prepared machines.", KeyPlaceholder)
	}
}
