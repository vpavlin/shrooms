package state

import "testing"

// Relay options must survive the config round trip. They are appended to a
// generated config by the test harness, so a parser that silently ignored them
// would look exactly like a relay that does not work.
func TestRelayConfigParses(t *testing.T) {
	cfg, err := parseConfig(`
network_key = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
name        = "node"
relay       = "true"
relay_addr  = "10.90.0.10:51820"
`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.Relay {
		t.Error("relay = \"true\" did not set Relay")
	}
	if cfg.RelayAddr != "10.90.0.10:51820" {
		t.Errorf("RelayAddr = %q", cfg.RelayAddr)
	}
}

func TestRelayDefaultsOff(t *testing.T) {
	cfg, err := parseConfig("network_key = \"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\"\nname = \"n\"\n")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Relay || cfg.RelayAddr != "" {
		t.Error("relay options should default off")
	}
}

// WriteConfig must emit what parseConfig accepts, or a rewritten config loses
// the relay settings.
func TestRelayConfigRoundTrip(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	in.Relay = true
	in.RelayAddr = "203.0.113.4:51820"

	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !out.Relay || out.RelayAddr != in.RelayAddr {
		t.Fatalf("round trip lost relay settings: %+v", out)
	}
}

// A config written before cluster_id existed must still load, and must leave
// the cluster unset.
//
// Unset is not a missing value to be filled in: passing any clusterId activates
// a legacy cluster-to-network mapping in the library, where 2 means logos.dev
// whatever the preset says. Defaulting it would silently move logos.test nodes
// onto logos.dev.
func TestConfigWithoutClusterIDLeavesItUnset(t *testing.T) {
	c, err := parseConfig(`
network_key = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"
name        = "old"
interface   = "shrooms0"
listen_port = 51820
preset      = "logos.test"
mode        = "Core"
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.ClusterID != 0 {
		t.Errorf("cluster_id = %d, want 0 (let the preset decide)", c.ClusterID)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a pre-cluster_id config failed validation: %v", err)
	}
}

// The default must be a network that actually works without an override.
func TestDefaultPresetNeedsNoClusterOverride(t *testing.T) {
	c := DefaultConfig()
	if c.Preset != DefaultPreset {
		t.Errorf("default preset = %q, want %q", c.Preset, DefaultPreset)
	}
	if c.ClusterID != 0 {
		t.Errorf("default sets cluster_id = %d; it should let the preset decide", c.ClusterID)
	}
}

func TestClusterIDAndEntryNodesRoundTrip(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"
	in.ClusterID = 3
	in.EntryNodes = []string{"enrtree://AO@example.test", "/ip4/203.0.113.4/tcp/60000/p2p/16Uiu2HAm"}

	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if out.ClusterID != 3 {
		t.Errorf("cluster_id = %d, want 3", out.ClusterID)
	}
	if len(out.EntryNodes) != 2 || out.EntryNodes[0] != in.EntryNodes[0] || out.EntryNodes[1] != in.EntryNodes[1] {
		t.Errorf("entry_nodes round trip lost data: %#v", out.EntryNodes)
	}
}

// entry_nodes alone is a valid bootstrap source; a preset is not required when
// the addresses are given explicitly.
func TestEntryNodesWithoutPresetIsValid(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"
	c.Preset = ""
	c.EntryNodes = []string{"enrtree://AO@example.test"}
	if err := c.Validate(); err != nil {
		t.Errorf("entry_nodes without preset rejected: %v", err)
	}

	c.EntryNodes = nil
	if err := c.Validate(); err == nil {
		t.Error("no preset and no entry_nodes was accepted — the node has nowhere to bootstrap")
	}
}

// A Core node relays only its mesh's shard unless told otherwise, and the
// setting that brings the old behaviour back survives a rewrite (ADR-067).
func TestRelayAllShardsIsOffAndRoundTrips(t *testing.T) {
	if DefaultConfig().RelayAllShards {
		t.Fatal("a Core node relays every shard by default")
	}
	in := DefaultConfig()
	in.NetworkKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	in.Name = "node"
	in.RelayAllShards = true
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.RelayAllShards {
		t.Error("relay_all_shards lost in the round trip")
	}
}

// An array item may hold commas of its own, as a LAN host's ports do; only the
// commas between items separate them (found in the lab, 2026-10-21).
func TestAnArrayItemMayHoldCommas(t *testing.T) {
	got := parseArray(`["web=10.77.0.13/tcp:8000,8001", "printer=192.168.10.42/tcp:8883,990,50000-50100"]`)
	if len(got) != 2 || got[0] != "web=10.77.0.13/tcp:8000,8001" || got[1] != "printer=192.168.10.42/tcp:8883,990,50000-50100" {
		t.Fatalf("parsed %q", got)
	}
	if got := parseArray(`["a", "b"]`); len(got) != 2 || got[1] != "b" {
		t.Errorf("a plain list: %q", got)
	}
	if got := parseArray(`[a, b]`); len(got) != 2 || got[0] != "a" {
		t.Errorf("bare items: %q", got)
	}
	in := []string{"web=10.77.0.13/tcp:8000,8001", "w"}
	if got := parseArray(formatArray(in)); len(got) != 2 || got[0] != in[0] {
		t.Errorf("round trip of an item with commas: %q", got)
	}
}

// What formatArray writes, quotes and backslashes included, reads back as it
// was, and a quote left open is an error rather than a merged item (audit
// F-36).
func TestAnArrayRoundTripsQuotesAndBackslashes(t *testing.T) {
	in := []string{`say "hi"`, `C:\path`, "plain,with,commas"}
	got := parseArray(formatArray(in))
	if len(got) != len(in) {
		t.Fatalf("parsed %q", got)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("item %d: %q, want %q", i, got[i], in[i])
		}
	}
	if _, err := parseConfig("name = \"laptop\nservices = [\"web\", \"ssh]\n"); err == nil {
		t.Error("an unclosed quote was accepted")
	}
	if c, err := parseConfig(`name = "C:\path"` + "\n"); err != nil || c.Name != `C:\path` {
		t.Errorf("a hand-written backslash: %q, %v", c.Name, err)
	}
}

func TestNameServersRoundTripAndValidation(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"
	in.NameServers = []string{"192.168.7.1", "2001:db8::53"}

	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(out.NameServers) != 2 || out.NameServers[0] != in.NameServers[0] || out.NameServers[1] != in.NameServers[1] {
		t.Errorf("name_servers round trip lost data: %#v", out.NameServers)
	}

	// Unset stays unset: the library's own default is a deliberate fallback,
	// and an empty list written out would read as "no resolvers".
	in.NameServers = nil
	if err := WriteConfig(path, in); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if out, err = LoadConfig(path); err != nil || out.NameServers != nil {
		t.Errorf("unset name_servers loaded as %#v, err %v", out.NameServers, err)
	}

	// A name cannot be resolved by the thing that cannot resolve.
	in.NameServers = []string{"resolver.example.test"}
	if err := in.Validate(); err == nil {
		t.Error("a hostname in name_servers was accepted")
	}
}
