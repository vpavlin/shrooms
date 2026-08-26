package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/vpavlin/shrooms/internal/state"
)

// Moving a config to one shape (docs/one-kind-of-mesh.md).
//
// A command rather than something the daemon does at startup. The daemon runs
// as root, holds every mesh key, and starts when nobody is watching — rewriting
// its own config there is the kind of thing that is fine ninety-nine times.
// This way somebody is present, the old file is kept, and the result is checked
// before anything is replaced.
//
// It changes no behaviour: the same meshes come back on the same interfaces and
// the same ports, because Flatten pins what the resolved values already were.
// What it removes is the config having two shapes at once, which is what most
// of the mesh bugs this year have had in common.

const flattenBackupSuffix = ".pre-flatten"

func cmdConfigFlatten(args []string) error {
	fs := flag.NewFlagSet("config flatten", flag.ExitOnError)
	cfgPath := fs.String("config", state.DefaultConfigPath, "config file")
	dry := fs.Bool("dry-run", false, "print the result and change nothing")
	yes := fs.Bool("yes", false, "do not ask")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	before, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if before.NetworkKey == "" {
		fmt.Printf("%s already describes every mesh the same way. Nothing to do.\n", *cfgPath)
		return nil
	}

	after, err := before.Flatten()
	if err != nil {
		return err
	}
	if err := after.Validate(); err != nil {
		return fmt.Errorf("the flattened config does not validate, so nothing was "+
			"written: %w", err)
	}

	// What moves, before anything is touched. The interesting line is the one
	// that says nothing moves.
	fmt.Printf("Flattening %s\n\n", *cfgPath)
	fmt.Printf("  %-10s %-9s %-6s %s\n", "MESH", "IFACE", "PORT", "")
	for _, m := range after.Meshes() {
		note := ""
		if m.InheritsIdentity {
			note = "keeps this device's original keys"
		}
		fmt.Printf("  %-10s %-9s %-6d %s\n", m.Label, m.Interface, m.ListenPort, note)
	}
	fmt.Println()
	fmt.Printf("Every mesh keeps the interface and port it has now, written down\n")
	fmt.Printf("explicitly instead of worked out from its position. The daemon will\n")
	fmt.Printf("bring up exactly what it brings up today.\n\n")

	if *dry {
		fmt.Printf("--- %s would become ---\n\n", *cfgPath)
		out, err := state.RenderConfig(after)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}

	if !*yes {
		ans, err := readSecret(fmt.Sprintf("Write it, keeping the old file as %s? [y/N] ",
			*cfgPath+flattenBackupSuffix))
		if err != nil {
			return err
		}
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("stopped, and nothing was changed")
		}
	}

	// The backup first, and refused rather than overwritten: a second run
	// would otherwise replace the original with the already-flattened one,
	// which is the copy nobody needs.
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	backup := *cfgPath + flattenBackupSuffix
	if _, err := os.Stat(backup); err == nil {
		return fmt.Errorf("%s already exists; move it aside first, because "+
			"overwriting it would lose the only copy of the original", backup)
	}
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return fmt.Errorf("could not keep a copy of the original, so nothing "+
			"was changed: %w", err)
	}

	if err := state.WriteConfig(*cfgPath, after); err != nil {
		return fmt.Errorf("writing failed; the original is at %s: %w", backup, err)
	}

	// Read it back rather than trusting the write. This is somebody's only
	// config, and a field the writer forgets is a setting silently dropped.
	back, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("the written config does not load; put %s back: %w", backup, err)
	}
	if got, want := len(back.Meshes()), len(after.Meshes()); got != want {
		return fmt.Errorf("the written config has %d meshes and should have %d; "+
			"put %s back", got, want, backup)
	}

	fmt.Printf("\nDone. The original is at %s.\n", backup)
	fmt.Printf("Nothing is running differently — restart when convenient.\n")
	return nil
}
