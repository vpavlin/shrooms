package state

import "testing"

const validKey = "P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A"

func TestInviteRoundTrip(t *testing.T) {
	uri := InviteURI(validKey, "home")
	key, mesh, err := ParseInvite(uri)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if key != validKey {
		t.Errorf("key = %q", key)
	}
	if mesh != "home" {
		t.Errorf("mesh = %q, want home", mesh)
	}
}

// People paste bare keys; refusing them would be pedantry.
func TestBareKeyAccepted(t *testing.T) {
	key, _, err := ParseInvite("  " + validKey + "\n")
	if err != nil {
		t.Fatalf("bare key rejected: %v", err)
	}
	if key != validKey {
		t.Errorf("key = %q", key)
	}
}

// A wrong scan must say so here, not surface later as "config invalid" — which
// tells the user nothing about what they actually scanned.
func TestRubbishRejected(t *testing.T) {
	for _, s := range []string{
		"", "hello", "https://example.com",
		"logosvpn://join?mesh=home",
		"logosvpn://join?key=notakey",
	} {
		if _, _, err := ParseInvite(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
