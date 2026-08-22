package main

import (
	"slices"
	"testing"

	"github.com/vpavlin/shrooms/internal/state"
)

// A change to a per-mesh setting has to be named. needsRestart compared only
// the NUMBER of meshes, so editing mesh.work.relay was applied to nothing and
// reported as nothing: "reloaded: 0 mesh(es) republished services", and the
// edit vanished. Replacing a mesh leaves the count unchanged too.
func TestNeedsRestartNamesPerMeshChanges(t *testing.T) {
	was := state.Config{MeshSet: map[string]state.Mesh{
		"work": {Label: "work", Relay: false},
		"home": {Label: "home"},
	}}
	now := state.Config{MeshSet: map[string]state.Mesh{
		"work": {Label: "work", Relay: true}, // edited
		"home": {Label: "home"},
	}}
	got := needsRestart(was, now)
	if !slices.Contains(got, "mesh.work") {
		t.Errorf("a changed per-mesh setting was not named: %v", got)
	}
	if slices.Contains(got, "mesh.home") {
		t.Errorf("an unchanged mesh was named: %v", got)
	}

	// Replaced rather than added: same count, different mesh.
	swapped := state.Config{MeshSet: map[string]state.Mesh{
		"work":  {Label: "work", Relay: false},
		"other": {Label: "other"},
	}}
	got = needsRestart(was, swapped)
	if !slices.Contains(got, "mesh.home (removed)") || !slices.Contains(got, "mesh.other (added)") {
		t.Errorf("a replaced mesh was not named: %v", got)
	}
}
