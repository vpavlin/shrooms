package mobile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

// A device that has never joined produced no diagnostics at all: the log is
// written by the session logger, and there is no session before the first mesh.
// So a tablet stuck on the join screen on 2026-09-25 could say nothing about
// which fleet it was on — the one fact that decides whether an invite can ever
// be heard, since two devices on different fleets both work and never meet.
func TestAFailedJoinLeavesSomethingToRead(t *testing.T) {
	dir := t.TempDir()

	// Not a token, so this fails immediately and never touches the network —
	// which is the case that used to leave no trace whatsoever.
	if err := JoinWithInvite("obviously-not-a-token", "tablet", dir, 1); err == nil {
		t.Fatal("a junk token was accepted")
	}

	report := Diagnostics(dir)
	for _, want := range []string{
		"redeeming an invite",
		"preset=" + state.DefaultPreset, // the fleet, which is the point
		"cluster=0",
		"the invite was not accepted",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("diagnostics do not mention %q:\n%s", want, report)
		}
	}
}

// The fleet reported must be the fleet used, or the log hides the mismatch it
// exists to reveal: this device's own config wins over the shipped defaults.
func TestTheLoggedFleetIsTheDevicesOwn(t *testing.T) {
	dir := t.TempDir()
	cfgPath, _ := paths(dir)

	cfg := phoneDefaults()
	cfg.Name = "tablet"
	cfg.Preset = "logos.other"
	cfg.ClusterID = 42
	cfg.NetworkKey = state.KeyPlaceholder
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	got := fleetFor(cfgPath)
	if got.Preset != "logos.other" || got.ClusterID != 42 {
		t.Fatalf("fleetFor = %s/%d, want the config's logos.other/42", got.Preset, got.ClusterID)
	}
	noteJoinAttempt(dir, got, false)
	report := Diagnostics(dir)
	if !strings.Contains(report, "preset=logos.other") || !strings.Contains(report, "cluster=42") {
		t.Errorf("the log reported a different fleet than the node would use:\n%s", report)
	}
	if _, err := state.LoadConfigUnvalidated(filepath.Join(dir, "config.toml")); err != nil {
		t.Fatalf("test wrote the config somewhere unexpected: %v", err)
	}
}
