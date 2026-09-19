package main

import (
	"strings"
	"testing"

	dnssrv "github.com/vpavlin/shrooms/internal/dns"
)

// The hint has to name this device's own interface and address, because those
// are the two things nobody can type from memory — and it has to say "on the
// host" for a container install, where running it through the wrapper puts the
// command back where the binary is missing.
func TestDNSRegisterFix(t *testing.T) {
	base := statusPayload{
		Overlay: "fd8d:4efd:5d78:d561:21ff:ba85:1620:2836",
		Meshes:  []meshStatus{{Label: "default", Iface: "shrooms0"}},
	}
	base.DNS = dnsStatus{
		Serving: true,
		Suffix:  "mesh",
		Address: "fd8d:4efd:5d78:d561:21ff:ba85:1620:2836",
	}

	got := dnsRegisterFix(base, false)
	for _, want := range []string{
		"resolvectl dns shrooms0 fd8d:4efd:5d78:d561:21ff:ba85:1620:2836",
		// One `domain` call naming every suffix the resolver answers. A config
		// on the legacy suffix must not have it registered twice.
		"resolvectl domain shrooms0 '~mesh'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "on the host") {
		t.Errorf("a host install should not be told to avoid the wrapper:\n%s", got)
	}

	// The container case, which is every install done by scripts/install.sh.
	container := base
	container.DNS.Err = `no resolvectl: exec: "resolvectl": executable file not found in $PATH`
	if got := dnsRegisterFix(base, true); !strings.Contains(got, "on the host") {
		t.Errorf("a container install has to be told where to run it:\n%s", got)
	}

	// A daemon that reported neither suffix nor address: the line still has to
	// be a runnable command, and the suffix it falls back to has to be the one
	// dns.Register itself falls back to — `.internal` since ADR-032, not the
	// `.mesh` that a config written by DefaultConfig happens to carry. Guessing
	// the other one would print a command that registers a domain the resolver
	// was never asked to serve.
	sparse := statusPayload{Overlay: "fd00::1"}
	sparse.DNS = dnsStatus{Serving: true}
	if got := dnsRegisterFix(sparse, false); !strings.Contains(got, "shrooms0 fd00::1") ||
		!strings.Contains(got, "'~"+dnssrv.DefaultSuffix+"'") {
		t.Errorf("defaults did not fill in:\n%s", got)
	}

	// A config on the current default still has to register the legacy suffix,
	// because the resolver answers both and only the registered ones reach it.
	current := base
	current.DNS.Suffix = dnssrv.DefaultSuffix
	got = dnsRegisterFix(current, false)
	for _, want := range []string{"'~" + dnssrv.DefaultSuffix + "'", "'~" + dnssrv.LegacySuffix + "'"} {
		if !strings.Contains(got, want) {
			t.Errorf("both suffixes should be registered, %q is missing:\n%s", want, got)
		}
	}
}
