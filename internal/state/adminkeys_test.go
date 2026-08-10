package state

import (
	"strings"
	"testing"
)

// A mesh with no admin keys is the world every node lives in today, and it must
// stay that way until one is minted: nil authority, no behaviour change.
func TestNoAdminKeysMeansNoAuthority(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = testKey
	auth, err := c.Authority()
	if err != nil {
		t.Fatal(err)
	}
	if auth != nil {
		t.Error("a config without admin keys produced an authority")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a config without admin keys was rejected: %v", err)
	}
}

// A mistyped admin key must fail at load. Otherwise it surfaces much later as a
// mesh where every peer is refused, which looks like a network fault.
func TestBadAdminKeyIsRejectedAtLoad(t *testing.T) {
	c := DefaultConfig()
	c.NetworkKey = testKey
	c.AdminKeys = []string{"NOT-BASE32!!"}
	if err := c.Validate(); err == nil {
		t.Fatal("a malformed admin key was accepted")
	}

	// Right alphabet, wrong length: still not a key.
	c.AdminKeys = []string{"AAAAAAAA"}
	if err := c.Validate(); err == nil {
		t.Error("an admin key of the wrong length was accepted")
	}
}

// The admin keys round-trip through a written config, or a node would lose its
// authority the first time anything rewrote the file.
func TestAdminKeysSurviveAConfigRoundTrip(t *testing.T) {
	in := DefaultConfig()
	in.NetworkKey = testKey
	in.AdminKeys = []string{
		"EGRWTGUFM63FDC6EX5ZZFRILX3XHUXXNN27CLRYOJ6EFBJORTXYQ",
		"3Y5HMGWBFHU7XTZTQW3IXVWSHUDI5OKPQZDJFOIRS5IBNBHD6CGQ",
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("a config with admin keys did not load: %v", err)
	}
	if len(out.AdminKeys) != 2 {
		t.Fatalf("admin keys came back as %v", out.AdminKeys)
	}
	auth, err := out.Authority()
	if err != nil || auth == nil {
		t.Fatalf("authority: %v", err)
	}
	if len(auth.Keys) != 2 {
		t.Errorf("authority holds %d keys", len(auth.Keys))
	}
	// The mesh id must not depend on the order they were written.
	swapped := out
	swapped.AdminKeys = []string{out.AdminKeys[1], out.AdminKeys[0]}
	other, err := swapped.Authority()
	if err != nil {
		t.Fatal(err)
	}
	if other.ID() != auth.ID() {
		t.Error("the mesh id changed when the keys were listed in the other order")
	}
	if !strings.Contains(strings.ToUpper(out.AdminKeys[0]), "EGRW") {
		t.Errorf("key text changed across the round trip: %q", out.AdminKeys[0])
	}
}
