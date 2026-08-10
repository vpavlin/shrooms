package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// The status payload names every device and address on the mesh. "0.0.0.0:8787"
// in a config file is an easy way to publish that to the whole network without
// meaning to, so anything but loopback is refused.
func TestUIListenRefusesNonLoopback(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := http.NewServeMux()

	for _, addr := range []string{
		"0.0.0.0:0",
		"192.168.1.10:0",
		"[::]:0",
		"example.com:80",
	} {
		if err := serveUI(context.Background(), log, addr, h); err == nil {
			t.Errorf("accepted %q; the mesh roster would be served to the network", addr)
		}
	}
}

func TestUIListenAcceptsLoopback(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := http.NewServeMux()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := serveUI(ctx, log, addr, h); err != nil {
			t.Errorf("refused %q: %v", addr, err)
		}
	}
}

func TestUIListenRejectsMalformedAddress(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := serveUI(context.Background(), log, "127.0.0.1", http.NewServeMux()); err == nil {
		t.Error("accepted an address with no port")
	}
}

// The services table exists to tell you what to type on another machine, so
// what it must get right is the name — and whether that name needs a port
// depends on whether the shared-port router is holding 80.
func TestPrintServices(t *testing.T) {
	svcs := []serviceStatus{
		{Name: "immich", Port: 2283, Target: "127.0.0.1:2283", DNSName: "home-server.mesh", Listening: true, Conns: 3},
		{Name: "jellyfin", Port: 8096, DNSName: "home-server.mesh", Direct: true},
		{Name: "broken", Port: 9000, DNSName: "home-server.mesh", Err: "permission denied"},
	}

	var b strings.Builder
	printServices(&b, svcs, []routerStatus{{Port: 80, Listening: true}})
	out := b.String()

	if !strings.Contains(out, "http://immich.home-server.mesh") {
		t.Errorf("the name to type is missing:\n%s", out)
	}
	// A service the application already answers for is not an error and must
	// not read like one.
	if !strings.Contains(out, "the application itself") {
		t.Errorf("a directly-reachable service is not explained:\n%s", out)
	}
	// A declared service that failed must say why on its row.
	if !strings.Contains(out, "permission denied") {
		t.Errorf("a failed service hides its reason:\n%s", out)
	}
}

// Without the router, the bare name does not work — so it must not be printed
// as though it does. Printing a URL that fails is worse than printing a longer
// one that works.
func TestPrintServicesFallsBackToPorts(t *testing.T) {
	var b strings.Builder
	printServices(&b, []serviceStatus{
		{Name: "immich", Port: 2283, DNSName: "home-server.mesh", Listening: true},
	}, []routerStatus{{Port: 80, Err: "needs CAP_NET_BIND_SERVICE"}})
	out := b.String()

	if !strings.Contains(out, "immich.home-server.mesh:2283") {
		t.Errorf("the working form is missing:\n%s", out)
	}
	if strings.Contains(out, "http://immich.home-server.mesh\n") {
		t.Errorf("offered a bare name that cannot work:\n%s", out)
	}
	// And it must say why, where the question is asked.
	if !strings.Contains(out, "CAP_NET_BIND_SERVICE") {
		t.Errorf("does not explain why the port is still needed:\n%s", out)
	}
}

// Nothing published means nothing printed — an empty table under a heading is
// worse than silence.
func TestPrintServicesSilentWhenEmpty(t *testing.T) {
	var b strings.Builder
	printServices(&b, nil, nil)
	if b.String() != "" {
		t.Errorf("printed %q for no services", b.String())
	}
}
