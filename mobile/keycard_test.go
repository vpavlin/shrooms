package mobile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ktypes "github.com/keycard-tech/keycard-go/v4/types"
)

// A pairing survives being written and read, because getting this wrong costs a
// pairing slot on a card that has very few and needs the PUK to recover them.
func TestPairingRoundTrip(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	in := ktypes.NewPairing(key, 3)

	out, err := decodePairing(encodePairing(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Index() != 3 {
		t.Errorf("slot index came back as %d, want 3", out.Index())
	}
	if out.Key() != key {
		t.Error("pairing key changed across the round trip")
	}
}

// A corrupt file must say so rather than producing a pairing that fails later
// inside the secure channel, where the error names nothing useful.
func TestCorruptPairingIsRefused(t *testing.T) {
	for _, s := range []string{"", "not base64!", "AAAA"} {
		if _, err := decodePairing(s); err == nil {
			t.Errorf("%q was accepted as a pairing", s)
		}
	}
}

// failing transport: no card in the field.
type deadCard struct{}

func (deadCard) Transmit([]byte) ([]byte, error) { return nil, errors.New("tag lost") }

// Before anything touches the card, an un-enrolled phone must say that plainly.
// The pairing file is what enrolment produces, so its absence is the check.
func TestUnenrolledCardIsReportedBeforeUse(t *testing.T) {
	dir := t.TempDir()
	if _, err := CardPublicKey(deadCard{}, dir, "123456"); err == nil {
		t.Error("an unenrolled card produced a public key")
	}
	// And with a pairing present, the failure comes from the card rather than
	// from the file — a different message, which is the point.
	if err := os.WriteFile(filepath.Join(dir, "keycard-pairing"),
		[]byte(encodePairing(ktypes.NewPairing([32]byte{1}, 0))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CardPublicKey(deadCard{}, dir, "123456"); err == nil {
		t.Error("a dead transport produced a public key")
	}
}

// A nil transport is a programming error on the Kotlin side and must not panic
// inside the card library.
func TestNilTransportIsAnError(t *testing.T) {
	if _, err := CardEnrol(nil, t.TempDir(), "x", "123456"); err == nil {
		t.Error("a nil transport was accepted")
	}
}

// The four walls, in the order they stop you.
//
// This is the table the whole setup flow branches on: whether to ask for a
// pairing password, whether there is any point asking for a PIN, whether
// freeing the other slots would help. A card can fail more than one of these at
// once and only the first is worth saying, so the order is pinned too.
//
// Every one of these was met with a real card on the first evening, and every
// one arrived as a status word — 6a84, 6d00, 6985 — that named the step and not
// the problem.
func TestWhatStopsACard(t *testing.T) {
	full := ktypes.CapabilityAll
	key := []byte{0xab, 0xcd, 0xef, 0x01, 0x02}

	for _, c := range []struct {
		name string
		info ktypes.ApplicationInfo
		want string
	}{
		{
			name: "a card that can be enrolled",
			info: ktypes.ApplicationInfo{
				Initialized: true, KeyUID: key, Version: []byte{3, 1},
				AvailableSlots: []byte{5}, Capabilities: full,
			},
			want: "",
		},
		{
			name: "no secure channel — a Cash card, which answers 6d00 to PAIR",
			info: ktypes.ApplicationInfo{
				Initialized: true, KeyUID: key, Version: []byte{3, 1},
				AvailableSlots: []byte{5},
				Capabilities:   ktypes.CapabilityCredentialsManagement,
			},
			want: "no-secure-channel",
		},
		{
			name: "never initialised — no PIN, PUK or pairing password exists",
			info: ktypes.ApplicationInfo{
				Initialized: false, Version: []byte{3, 1},
				AvailableSlots: []byte{5}, Capabilities: full,
			},
			want: "not-initialised",
		},
		{
			name: "initialised and empty — INIT does not create a key",
			info: ktypes.ApplicationInfo{
				Initialized: true, KeyUID: nil, Version: []byte{3, 1},
				AvailableSlots: []byte{5}, Capabilities: full,
			},
			want: "no-key",
		},
		{
			name: "every pairing slot taken, which is what 6a84 means",
			info: ktypes.ApplicationInfo{
				Initialized: true, KeyUID: key, Version: []byte{3, 1},
				AvailableSlots: []byte{0}, Capabilities: full,
			},
			want: "no-slots",
		},
		{
			// Both true at once. Initialising is the first thing to do and
			// saying "no key" would send somebody to the wrong screen.
			name: "uninitialised and empty reports the initialisation",
			info: ktypes.ApplicationInfo{
				Initialized: false, KeyUID: nil, Version: []byte{3, 1},
				AvailableSlots: []byte{5}, Capabilities: full,
			},
			want: "not-initialised",
		},
		{
			// A card with no secure channel is unusable whatever else is
			// wrong, and it is the only one of these that cannot be fixed.
			name: "no secure channel outranks everything else",
			info: ktypes.ApplicationInfo{
				Initialized: false, KeyUID: nil, Version: []byte{3, 1},
				AvailableSlots: []byte{0}, Capabilities: 0,
			},
			want: "no-secure-channel",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := reportOf(&c.info)
			if got.Problem != c.want {
				t.Errorf("problem is %q, want %q", got.Problem, c.want)
			}
			if got.Summary == "" {
				t.Error("no summary; the screen would show an empty verdict")
			}
		})
	}
}

// Applet 4.0 and later pairs with a certificate, so asking for a pairing
// password there is asking for something the card does not have. Getting this
// wrong showed a field nobody could fill in correctly, because no value was.
func TestPairingPasswordOnlyBelowAppletFour(t *testing.T) {
	for _, c := range []struct {
		version []byte
		want    bool
	}{
		{[]byte{2, 2}, true},
		{[]byte{3, 1}, true},
		{[]byte{4, 0}, false},
		{[]byte{5, 0}, false},
		// An unreadable version is treated as old: asking for a password that
		// turns out to be unnecessary is recoverable, and not asking for one
		// that was needed is a failed pairing and a spent slot.
		{nil, true},
	} {
		info := ktypes.ApplicationInfo{
			Initialized: true, KeyUID: []byte{1}, Version: c.version,
			AvailableSlots: []byte{5}, Capabilities: ktypes.CapabilityAll,
		}
		if got := reportOf(&info).NeedsPassword; got != c.want {
			t.Errorf("applet %v: needsPassword is %v, want %v", c.version, got, c.want)
		}
	}
}

// The screen reads this as JSON, so it has to survive being encoded.
func TestReportEncodesForTheScreen(t *testing.T) {
	info := ktypes.ApplicationInfo{
		Initialized: true, KeyUID: []byte{0xab, 0xcd, 0xef, 0x01},
		Version: []byte{3, 1}, AvailableSlots: []byte{4},
		Capabilities: ktypes.CapabilityAll,
	}
	out, err := reportOf(&info).encode()
	if err != nil {
		t.Fatal(err)
	}
	var back cardReport
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("the screen could not read this: %v", err)
	}
	if back.FreeSlots != 4 || !back.HasKey || back.KeyUID != "abcdef01" {
		t.Errorf("came back as %+v", back)
	}
}

// Enrolment is answered from disk, without a card: the settings screen opens
// knowing whether this phone is set up, rather than showing every action and
// finding out at the tap.
func TestEnrolmentIsAnsweredFromDisk(t *testing.T) {
	dir := t.TempDir()

	var before struct {
		Paired bool   `json:"paired"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal([]byte(CardEnrolment(dir)), &before); err != nil {
		t.Fatal(err)
	}
	if before.Paired {
		t.Error("an empty directory reported a pairing")
	}

	if err := os.WriteFile(filepath.Join(dir, "keycard-pairing"),
		[]byte(encodePairing(ktypes.NewPairing([32]byte{7}, 2))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keycard-key"), []byte("02aabb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Paired bool   `json:"paired"`
		Key    string `json:"key"`
	}
	if err := json.Unmarshal([]byte(CardEnrolment(dir)), &after); err != nil {
		t.Fatal(err)
	}
	if !after.Paired {
		t.Error("a stored pairing was not reported")
	}
	// Trimmed: it is read straight into a field that goes in admin_keys, and a
	// trailing newline there is a key that does not parse.
	if after.Key != "02aabb" {
		t.Errorf("key came back as %q", after.Key)
	}

	// Forgetting removes both, and says nothing about the card — the slot on
	// the card is still taken, which is why the UI has to say so.
	if err := CardForget(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keycard-pairing")); !os.IsNotExist(err) {
		t.Error("the pairing survived being forgotten")
	}
	if _, err := os.Stat(filepath.Join(dir, "keycard-key")); !os.IsNotExist(err) {
		t.Error("the key survived being forgotten")
	}
	// Twice is not an error: the button is there whether or not there is
	// anything behind it.
	if err := CardForget(dir); err != nil {
		t.Errorf("forgetting an already-forgotten card failed: %v", err)
	}
}
