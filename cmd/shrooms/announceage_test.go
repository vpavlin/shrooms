package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ANNOUNCE said only "online" or "offline", a three-minute threshold with
// nothing either side of it. Whether a peer announced a second ago or has
// been silent for a day was in the JSON and nowhere on the screen.
func TestStatusShowsHowLongAgoAPeerAnnounced(t *testing.T) {
	now := time.Now()
	sock := statusSocket(t, fmt.Sprintf(`{
	  "prefix": "fd00:1234:5678::/48",
	  "overlay": "fd00:1234:5678::1",
	  "rendezvous": {"status": "Connected", "ok": true},
	  "peers": [
	    {"name": "atlas", "overlay": "fd00:1234:5678::2", "online": true,  "last_seen": %q},
	    {"name": "pixel", "overlay": "fd00:1234:5678::3", "online": false, "last_seen": %q}
	  ]
	}`, now.Add(-30*time.Second).UTC().Format(time.RFC3339), now.Add(-20*time.Minute).UTC().Format(time.RFC3339)))

	out := captureStdout(t, func() {
		if err := cmdStatus([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"online 30s", "offline 20m"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A daemon too old to send last_seen, or a peer never seen, still gets the
// plain word rather than a parse error or "online 0s".
func TestStatusAnnounceWithoutATimestampIsJustTheWord(t *testing.T) {
	sock := statusSocket(t, `{
	  "prefix": "fd00:1234:5678::/48",
	  "overlay": "fd00:1234:5678::1",
	  "rendezvous": {"status": "Connected", "ok": true},
	  "peers": [{"name": "atlas", "overlay": "fd00:1234:5678::2", "online": true}]
	}`)
	out := captureStdout(t, func() {
		if err := cmdStatus([]string{"--socket", sock}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "online") || strings.Contains(out, "online 0s") {
		t.Errorf("announce without a timestamp was not the plain word:\n%s", out)
	}
}
