package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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
