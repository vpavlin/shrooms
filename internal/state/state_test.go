package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rendezvous port can be pinned, which is what lets a node publish an
// address others bootstrap from (ADR-031). Zero means "the library chooses",
// which is what every node did before this existed and stays the default.
func TestDeliveryPortRoundTrips(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = testKey
	in.DeliveryPort = 39777

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.DeliveryPort != 39777 {
		t.Errorf("delivery_port came back as %d, want 39777", out.DeliveryPort)
	}

	// An unset port must stay unset rather than becoming a real one: a node
	// nobody dials should keep letting the library choose.
	in.DeliveryPort = 0
	path2 := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path2, in); err != nil {
		t.Fatal(err)
	}
	out2, err := LoadConfig(path2)
	if err != nil {
		t.Fatal(err)
	}
	if out2.DeliveryPort != 0 {
		t.Errorf("an unset delivery_port became %d", out2.DeliveryPort)
	}

	// And the written form must be commented out, not absent, so somebody
	// reading the file learns the setting exists.
	body, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "delivery_port") {
		t.Error("an unset delivery_port left no trace in the config")
	}
}
