package dns

import "testing"

// Every suffix the server answers has to be registered, or the unregistered
// one is answerable by `dig @…` and dead everywhere else — which is the exact
// failure Register exists to close, reintroduced for half the names.
func TestRoutingDomainsCoversEverySuffix(t *testing.T) {
	got := routingDomains(DefaultSuffix, []string{LegacySuffix})
	want := []string{"~" + DefaultSuffix, "~" + LegacySuffix}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("domain %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// A config already on the legacy suffix names it twice — once as its own and
// once as the transitional alias. resolved would take it, but the command is
// also printed for people to read and run.
func TestRoutingDomainsDeduplicates(t *testing.T) {
	if got := routingDomains(LegacySuffix, []string{LegacySuffix}); len(got) != 1 {
		t.Errorf("a repeated suffix should appear once, got %v", got)
	}
	if got := routingDomains(".mesh.", []string{"mesh"}); len(got) != 1 || got[0] != "~mesh" {
		t.Errorf("dots should be trimmed before comparing, got %v", got)
	}
}

// An unset suffix falls back to what the server would serve, not to whatever
// the caller happened to have.
func TestRoutingDomainsFallsBackToDefault(t *testing.T) {
	got := routingDomains("", nil)
	if len(got) != 1 || got[0] != "~"+DefaultSuffix {
		t.Errorf("got %v, want [~%s]", got, DefaultSuffix)
	}
}

// The printed command is run by a person or by the host-side registrar in
// scripts/install.sh, so it has to be a valid single line with the domains
// quoted — an unquoted ~ is a home directory to a shell.
func TestRegisterCommandIsRunnable(t *testing.T) {
	got := RegisterCommand("sudo ", "shrooms0", "fd00::1", DefaultSuffix, LegacySuffix)
	want := "sudo resolvectl dns shrooms0 fd00::1 && " +
		"sudo resolvectl domain shrooms0 '~" + DefaultSuffix + "' '~" + LegacySuffix + "'"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if plain := RegisterCommand("", "shrooms0", "fd00::1", DefaultSuffix); plain[:11] != "resolvectl " {
		t.Errorf("an empty prefix should leave the command bare: %s", plain)
	}
}
