package main

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

// The daemon's /status, abridged to what shrooms-agent reads.
const sampleStatus = `{
  "meshes": [
    {"label": "home",   "overlay": "fd7b:15fb:5ec1:c1c8:8395:b471:36d9:d655"},
    {"label": "office", "overlay": "fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb"}
  ],
  "peers": [
    {"name": "nothing", "mesh": "home", "overlay": "fd7b:15fb:5ec1:b2bf:31ab:8ad3:c152:728a", "overlay_v4": "198.18.57.99"},
    {"name": "vps", "mesh": "office", "overlay": "fdb0:9afc:a5ef:f167:7ad7:68b7:aca0:b9fa"},
    {"name": "laptop", "overlay": "fdb0:9afc:a5ef:388c::5"}
  ]
}`

func parse(t *testing.T) status {
	t.Helper()
	var st status
	if err := json.Unmarshal([]byte(sampleStatus), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// It serves on exactly the meshes asked for — they are who may talk to the
// agents (docs/agents.md) — and refuses a name it is not in rather than
// quietly serving fewer.
func TestServesOnTheMeshesAskedFor(t *testing.T) {
	st := parse(t)
	all, err := st.addresses("")
	if err != nil || len(all) != 2 {
		t.Fatalf("default: %v, %v", all, err)
	}
	one, err := st.addresses("office")
	if err != nil || len(one) != 1 || one["office"] != netip.MustParseAddr("fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb") {
		t.Fatalf("office only: %v, %v", one, err)
	}
	if _, err := st.addresses("office, typo"); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Errorf("an unknown mesh was not refused: %v", err)
	}
}

// A caller is named by the address its request came from, IPv6 or the
// synthetic IPv4, and an address nobody has is not given a name.
func TestCallersAreNamedByTheirAddress(t *testing.T) {
	var p peerNames
	p.update(parse(t))
	for addr, want := range map[string]string{
		"fd7b:15fb:5ec1:b2bf:31ab:8ad3:c152:728a": "nothing.home",
		"198.18.57.99": "nothing.home",
		"fdb0:9afc:a5ef:f167:7ad7:68b7:aca0:b9fa": "vps.office",
		"fdb0:9afc:a5ef:388c::5":                  "laptop",
		"fdb0:9afc:a5ef::1":                       "",
	} {
		if got := p.who(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s named %q, want %q", addr, got, want)
		}
	}
}

// The peer list a phone discovers other agents from: every member with an
// overlay address, on every mesh.
func TestPeersAreListedForDiscovery(t *testing.T) {
	var p peerNames
	p.update(parse(t))
	got := p.list()
	if len(got) != 3 || got[0].Name != "nothing" || got[0].Mesh != "home" || got[1].Overlay != "fdb0:9afc:a5ef:f167:7ad7:68b7:aca0:b9fa" {
		t.Errorf("peers: %+v", got)
	}
}
