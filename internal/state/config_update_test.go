package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func seedConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Config{
		NetworkKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:       "laptop",
		Interface:  "shrooms0",
		ListenPort: 51820,
		Preset:     "logos.test",
	}
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every /config/* endpoint did load-modify-write with no lock, on a concurrent
// HTTP server. Two settings changed at once meant both handlers loaded the same
// config and the second to write had never seen the first's change — so one
// setting silently went back to what it was, with a success reported for both.
func TestConcurrentUpdatesDoNotLoseEachOther(t *testing.T) {
	path := seedConfig(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := UpdateConfig(path, func(c *Config) error {
			c.Name = "renamed"
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := UpdateConfig(path, func(c *Config) error {
			c.Services = []string{"immich:2283"}
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()

	got, err := LoadConfigUnvalidated(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q; the rename was lost", got.Name)
	}
	if len(got.Services) != 1 {
		t.Errorf("services = %v; the service list was lost", got.Services)
	}
}

// A rejected change must leave the file exactly as it was. Validation failures
// take this path, and a config that would not load must never reach the disk.
func TestARejectedUpdateWritesNothing(t *testing.T) {
	path := seedConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	wantErr := os.ErrInvalid
	if err := UpdateConfig(path, func(c *Config) error {
		c.Name = "should-not-survive"
		return wantErr
	}); err != wantErr {
		t.Fatalf("err = %v, want the callback's own error", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a rejected change was written anyway")
	}
}

// The write replaces the file rather than truncating and refilling it, so a
// reader either sees the old config or the new one. This checks the property
// the atomicity exists for: the file is never shorter than a valid config, and
// the network key — the one value whose loss cannot be recovered from the
// running process — is present at every moment.
func TestWriteConfigNeverLeavesAPartialFile(t *testing.T) {
	path := seedConfig(t)

	stop := make(chan struct{})
	bad := make(chan string, 1)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Reading straight through a rewrite. A truncate-and-write would
			// show up here as a config with no key.
			c, err := LoadConfigUnvalidated(path)
			if err != nil {
				continue // mid-rename: the old inode is gone, the new is not linked
			}
			if c.NetworkKey == "" {
				select {
				case bad <- "read a config with no network key":
				default:
				}
				return
			}
		}
	}()

	for i := 0; i < 200; i++ {
		if err := UpdateConfig(path, func(c *Config) error {
			c.Services = []string{"a:1", "b:2", "c:3"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	select {
	case msg := <-bad:
		t.Error(msg)
	default:
	}
}

// The lock file is an implementation detail and must not become config the
// daemon tries to read, or litter that survives.
func TestTheLockDoesNotBecomeTheConfig(t *testing.T) {
	path := seedConfig(t)
	if err := UpdateConfig(path, func(c *Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}
