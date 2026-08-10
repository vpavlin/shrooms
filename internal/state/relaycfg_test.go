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
