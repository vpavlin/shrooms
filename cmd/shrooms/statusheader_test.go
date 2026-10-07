package main

import (
	"strings"
	"testing"
)

// The header counted peers whose announce was fresh and called them "up",
// while the TUNNEL column says "up" for a live handshake. A status could read
// "peers 1 (0 up)" above a row saying "up 4m". The header now uses the
// columns' own words.
func TestStatusHeaderUsesTheColumnsWords(t *testing.T) {
	sock := statusSocket(t, `{
	  "prefix": "fd00:1234:5678::/48",
	  "overlay": "fd00:1234:5678::1",
	  "rendezvous": {"status": "Connected", "ok": true},
	  "peers": [
	    {"name": "atlas", "overlay": "fd00:1234:5678::2", "online": true,  "live": true,  "handshake_age_s": 40, "handshaked": true},
	    {"name": "pixel", "overlay": "fd00:1234:5678::3", "online": true,  "live": false, "handshake_age_s": 240, "handshaked": true},
	    {"name": "nas",   "overlay": "fd00:1234:5678::4", "online": false, "live": false}
	  ]
	}`)

	out := captureStdout(t, func() {
		if err := cmdStatus([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "peers 3 (2 online, 1 up)") {
		t.Errorf("header does not count announces and tunnels separately:\n%s", out)
	}
}
