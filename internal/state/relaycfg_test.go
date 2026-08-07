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
