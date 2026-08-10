package state

import "testing"

const testKey = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"

// WriteConfig must emit what parseConfig accepts, or a rewritten config quietly
// unpublishes every service.
func TestServicesRoundTrip(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = testKey
	in.Services = []string{"immich:2283", "jellyfin:8096->8920"}

	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(out.Services) != 2 || out.Services[0] != "immich:2283" {
		t.Fatalf("round trip lost services: %+v", out.Services)
	}
	specs, err := out.ServiceSpecs()
	if err != nil {
		t.Fatalf("ServiceSpecs: %v", err)
	}
	if specs[1].Target != "127.0.0.1:8920" {
		t.Errorf("target = %q", specs[1].Target)
	}
}

// A config with no services is the ordinary case and must stay silent about it.
func TestNoServicesByDefault(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = testKey
	if err := c.Validate(); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if len(c.Services) != 0 {
		t.Errorf("services default to %v", c.Services)
	}
}

// A malformed service is refused at load, not at bind. Otherwise a typo
// surfaces much later as one service missing from a mesh that looks fine.
func TestBadServiceRejectedAtLoad(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = testKey
	c.Services = []string{"immich"}
	if err := c.Validate(); err == nil {
		t.Error("a service with no port was accepted")
	}
}
