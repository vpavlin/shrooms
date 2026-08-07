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

// A config written before cluster_id existed must still load. The fleet moved
// clusters, so a node that silently kept the old default would connect to every
// peer and be hung up on — the exact failure this field exists to prevent.
func TestConfigWithoutClusterIDGetsDefault(t *testing.T) {
	c, err := parseConfig(`
network_key = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"
name        = "old"
interface   = "logos0"
listen_port = 51820
preset      = "logos.dev"
mode        = "Core"
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.ClusterID != DefaultClusterID {
		t.Errorf("cluster_id = %d, want the default %d", c.ClusterID, DefaultClusterID)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a pre-cluster_id config failed validation: %v", err)
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
