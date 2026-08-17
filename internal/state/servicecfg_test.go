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

// A malformed service must NOT stop the config loading — the reverse of what
// this test used to assert, and the reverse for a reason that was paid for.
//
// It was refused at load so a typo surfaced early rather than as one missing
// service on a mesh that looked fine. That reasoning is sound for the machine
// in front of you and wrong for every other one: load failing means the daemon
// does not start, the mesh does not come up, and on a remote machine the tunnel
// that was the way in to fix the typo is gone with it. A service publishes a
// local port under a name. It is the least important thing in the file and it
// took the whole daemon down.
//
// The early warning is kept where it costs nothing: on the way in, by whoever
// sets it (ValidateServices, and the /config/services handler).
func TestBadServiceDoesNotBlockStartup(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = testKey
	c.Services = []string{"immich"} // no port

	if err := c.Validate(); err != nil {
		t.Errorf("a malformed service stopped the config loading: %v", err)
	}
	if err := c.ValidateServices(); err == nil {
		t.Error("a service with no port passed the check meant to catch it")
	}
}

// The things that genuinely prevent a mesh existing are still refused at load,
// so relaxing services did not relax everything.
func TestLoadStillRefusesWhatTheMeshCannotRunWithout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*Config)
	}{
		{"no network key", func(c *Config) { c.NetworkKey = "" }},
		{"no name", func(c *Config) { c.Name = "" }},
		{"no port", func(c *Config) { c.ListenPort = 0 }},
		{"no interface", func(c *Config) { c.Interface = "" }},
	} {
		c := DefaultConfig()
		c.NetworkKey = testKey
		tc.break_(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}
