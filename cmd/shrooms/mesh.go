package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"text/tabwriter"

	"github.com/vpavlin/shrooms/internal/state"
)

// Seeing and switching the meshes this device belongs to.
//
// This exists because of a hole that swallowed a mesh. Switching one off is
// meant to be the reversible half of leaving it — the credentials stay, the key
// stays, it simply does not run (ADR-015). But every way of looking at this
// device listed the meshes that were *running*: `status` reports the daemon's
// instances, and a mesh that is off has none. So switching one off removed it
// from the only list anybody had, and there was no way to switch it back on
// short of opening the config in an editor as root.
//
// A reversible operation you cannot see how to reverse is not reversible. So:
// the config, which is the thing that actually knows, read directly rather than
// through the daemon.
//
// Directly is also deliberate. The daemon may be down — a config that will not
// start it is exactly when you need to see what is in it — and the file is the
// authority either way. It is 0600 and root-owned, so this needs sudo, which is
// the honest cost of reading a file full of network keys.

func cmdMesh(args []string) error {
	if len(args) == 0 {
		return cmdMeshList(nil)
	}
	switch args[0] {
	case "list", "ls":
		return cmdMeshList(args[1:])
	case "enable", "on":
		return cmdMeshSwitch(args[1:], true)
	case "disable", "off":
		return cmdMeshSwitch(args[1:], false)
	default:
		return fmt.Errorf("unknown mesh command %q; try: list, enable, disable", args[0])
	}
}

// cmdMeshList prints every mesh in the config, running or not.
func cmdMeshList(args []string) error {
	fs := flag.NewFlagSet("mesh list", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := state.LoadConfigUnvalidated(*cfgPath)
	if err != nil {
		return hintPermission(*cfgPath, err)
	}
	meshes := cfg.Meshes()
	if len(meshes) == 0 {
		fmt.Println("This device is not in any mesh.")
		fmt.Println("  shrooms join <invite-token>")
		return nil
	}

	// The state directory is opened separately and its absence is not fatal:
	// the question "which meshes am I in" has a useful answer without it, and
	// running this as a diagnostic on a machine whose state is unreadable is a
	// real case.
	st, stErr := state.LoadOrCreateState(*stateDir)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MESH\tSTATE\tPREFIX\tCREDENTIAL\tRELAY\tSERVICES")
	for _, m := range meshes {
		state_ := "on"
		if m.Disabled {
			state_ = "OFF"
		}
		prefix := "?"
		if k, err := m.Key(); err == nil {
			prefix = k.Prefix().String()
		}

		// Whether this device holds a credential for the mesh, which decides
		// whether it can rejoin by simply being switched on. Without one — on a
		// mesh that has admin keys — being in the config is not being a member.
		cred := "—"
		if stErr == nil {
			if have, note := credentialNote(st, m); have != "" {
				cred = have
			} else {
				cred = note
			}
		}
		relay := ""
		if m.Relay {
			relay = "yes"
		}
		svc := ""
		if len(m.Services) > 0 {
			svc = fmt.Sprintf("%d", len(m.Services))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", m.Label, state_, prefix, cred, relay, svc)
	}
	w.Flush()

	// The way back, printed where somebody looking at an OFF row is already
	// looking. This is the whole reason the command exists.
	for _, m := range meshes {
		if m.Disabled {
			fmt.Printf("\n%s is switched off. It keeps its key and credentials:\n", m.Label)
			fmt.Printf("  sudo shrooms mesh enable %s && sudo systemctl restart shrooms\n", m.Label)
			break
		}
	}
	return nil
}

// credentialNote describes what this device holds for a mesh.
//
// Returns (description, "") when there is something to report, or ("", note)
// when there is not — so the caller renders one column either way rather than
// deciding what an empty string meant.
func credentialNote(st *state.State, m state.Mesh) (string, string) {
	k, err := m.Key()
	if err != nil {
		return "", "?"
	}
	legacy := m.Label == state.DefaultLabel
	ms, err := st.MeshState(state.NetworkID(k), legacy)
	if err != nil || len(ms.Credential) == 0 {
		// Not a fault: a mesh with no admin keys admits by the network key
		// alone, and there is no credential to hold.
		if len(m.AdminKeys) == 0 {
			return "not needed", ""
		}
		return "", "MISSING"
	}
	return "held", ""
}

// cmdMeshSwitch turns one mesh on or off.
func cmdMeshSwitch(args []string, on bool) error {
	fs := flag.NewFlagSet("mesh enable", flag.ExitOnError)
	cfgPath, _ := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("which mesh? try: shrooms mesh list")
	}
	label := fs.Arg(0)

	cfg, err := state.LoadConfigUnvalidated(*cfgPath)
	if err != nil {
		return hintPermission(*cfgPath, err)
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		if label == state.DefaultLabel && cfg.NetworkKey != "" {
			// The single-mesh form has nowhere to write "off", and a device
			// with one mesh switched off is a device switched off — which is
			// what stopping the daemon is for.
			return errors.New("the first mesh cannot be switched off; stop the daemon instead")
		}
		return fmt.Errorf("no mesh called %q; try: shrooms mesh list", label)
	}
	if m.Disabled == !on {
		fmt.Printf("%s is already %s.\n", label, stateWord(on))
		return nil
	}
	m.Disabled = !on
	cfg.MeshSet[label] = m
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := state.WriteConfig(*cfgPath, cfg); err != nil {
		return hintPermission(*cfgPath, err)
	}

	fmt.Printf("%s switched %s.\n", label, stateWord(on))
	// A mesh coming or going is a WireGuard device coming or going, which a
	// reload cannot do. Said plainly rather than left to be discovered.
	fmt.Println("It takes effect on the next restart:")
	fmt.Println("  sudo systemctl restart shrooms")
	return nil
}

func stateWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// hintPermission adds the one thing a bare EACCES on this file does not say.
//
// errors.Is rather than os.IsPermission: the loader wraps its errors, and the
// legacy helper does not unwrap, so it answered false on exactly the error this
// exists to catch. Which meant the hint never appeared, and the first run of
// this command printed a bare "permission denied" — the one outcome it was
// written to improve on.
func hintPermission(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s holds this mesh's network keys and is readable by root only: %w\n"+
			"  try: sudo shrooms mesh list", path, err)
	}
	return err
}
