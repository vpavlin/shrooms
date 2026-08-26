package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	case "rename":
		return cmdMeshRename(args[1:])
	case "remove", "rm", "leave":
		return cmdMeshRemove(args[1:])
	default:
		return fmt.Errorf("unknown mesh command %q; try: list, enable, disable, rename", args[0])
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
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
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

// cmdMeshRename changes what this device calls a mesh.
//
// A mesh label is local and nothing on the wire carries it, so the same mesh is
// routinely called different things on different devices — which is fine until
// you are trying to work out whether two machines are on the same mesh, and
// then it is the whole problem. Renaming is how you make them agree.
//
// It cannot be a sed on the config, which is what it looks like. The interface
// name and UDP port come from a mesh's position in a label-sorted list, so a
// rename re-sorts that list and silently moves the interface and port of every
// mesh at or after the new position — firewall rules, port forwards and every
// peer's cached endpoint left pointing at the wrong mesh, for a change that was
// supposed to be cosmetic.
//
// So this pins what every mesh currently has before renaming anything. After
// it, the config says outright which interface and port each mesh uses, and
// nothing is deciding that from a nickname any more.
func cmdMeshRename(args []string) error {
	fs := flag.NewFlagSet("mesh rename", flag.ExitOnError)
	cfgPath := fs.String("config", state.DefaultConfigPath, "config file")
	adminDir := fs.String("admin-dir", defaultAdminDir(), "where admin keys are kept")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: shrooms mesh rename <old> <new>")
	}
	from, to := fs.Arg(0), fs.Arg(1)
	if from == to {
		return fmt.Errorf("%q is already what it is called", from)
	}
	if err := state.ValidMeshLabel(to); err != nil {
		return err
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if _, taken := cfg.MeshSet[to]; taken {
		return fmt.Errorf("this device already has a mesh called %q", to)
	}
	if to == state.DefaultLabel {
		return fmt.Errorf("%q names the mesh this device was built around, which is "+
			"written as the config's top-level key rather than as a mesh entry",
			state.DefaultLabel)
	}
	if _, ok := cfg.MeshSet[from]; !ok {
		if from == state.DefaultLabel || cfg.NetworkKey != "" && len(cfg.MeshSet) == 0 {
			return fmt.Errorf("%q is this config's top-level mesh, not a named entry, "+
				"so there is no label to change. Every other device may call it "+
				"whatever it likes; this one does not name it at all", from)
		}
		return fmt.Errorf("no mesh called %q; try: shrooms mesh list", from)
	}

	pinInterfacesAndPorts(&cfg)

	moved := cfg.MeshSet[from]
	moved.Label = to
	delete(cfg.MeshSet, from)
	cfg.MeshSet[to] = moved

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := state.WriteConfig(*cfgPath, cfg); err != nil {
		return err
	}

	// The admin key file is named after the label too, and a mesh whose
	// authority this device holds would otherwise stop finding it.
	oldAdmin := adminPathFor(*adminDir, from)
	if _, err := os.Stat(oldAdmin); err == nil {
		newAdmin := adminPathFor(*adminDir, to)
		if _, err := os.Stat(newAdmin); err == nil {
			fmt.Printf("!! %s already exists; left %s alone\n", newAdmin, oldAdmin)
		} else if err := os.Rename(oldAdmin, newAdmin); err != nil {
			fmt.Printf("!! renamed the mesh but not its admin key: %v\n", err)
			fmt.Printf("   move %s to %s by hand\n", oldAdmin, newAdmin)
		} else {
			fmt.Printf("admin key   %s\n", filepath.Base(newAdmin))
		}
	}

	fmt.Printf("renamed     %s -> %s\n", from, to)
	fmt.Printf("interface   %s, port %d (pinned, so the rename moved nothing)\n",
		moved.Interface, moved.ListenPort)
	fmt.Println()
	fmt.Println("Restart the daemon for it to take effect.")
	fmt.Println("Other devices keep their own name for this mesh; labels are local.")
	return nil
}

// pinInterfacesAndPorts writes down what every mesh currently uses.
//
// Interface names and ports come from a mesh's position in a label-sorted list,
// so anything that changes that list — renaming a mesh, removing one — moves
// them for every mesh at or after the change. Firewall rules, port forwards and
// every peer's cached endpoint would then point at the wrong mesh, for an
// operation that was supposed to be about a name.
//
// Called before the list changes, never after.
func pinInterfacesAndPorts(cfg *state.Config) {
	pinned := map[string]state.Mesh{}
	for i, m := range cfg.Meshes() {
		if m.Label == state.DefaultLabel {
			continue // the top-level form is always position zero
		}
		iface, port := ifaceAndPort(*cfg, m, i)
		e := cfg.MeshSet[m.Label]
		e.Interface, e.ListenPort = iface, port
		pinned[m.Label] = e
	}
	for label, m := range pinned {
		cfg.MeshSet[label] = m
	}
}

// cmdMeshRemove takes this device off a mesh.
//
// The network key IS the membership. Removing the entry that holds it is how
// you leave, and it is also how you lose the ability to come back — so the key
// is printed before anything is written, because it is the one thing here that
// cannot be recovered from somewhere else.
//
// What it deliberately does NOT touch:
//
//   - the admin key, if this device holds the mesh's authority. That is the
//     mesh's, not this device's, and deleting it would end the mesh for
//     everybody rather than removing one member from it.
//   - this mesh's state — its identity, credential and replay marks. Rejoining
//     with the same key then comes back as the same device rather than a
//     stranger, which is usually what somebody leaving and returning wants.
func cmdMeshRemove(args []string) error {
	fs := flag.NewFlagSet("mesh remove", flag.ExitOnError)
	cfgPath := fs.String("config", state.DefaultConfigPath, "config file")
	sock := fs.String("socket", DefaultSocket, "control socket, so a running daemon drops the interface")
	yes := fs.Bool("yes", false, "do not ask")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("which mesh? try: shrooms mesh list")
	}
	label := fs.Arg(0)

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		if label == state.DefaultLabel || cfg.NetworkKey != "" && len(cfg.MeshSet) == 0 {
			return fmt.Errorf("%q is this config's top-level mesh, which is the one "+
				"this device was built around rather than one it joined. Removing it "+
				"is removing the device's whole configuration; delete %s instead, "+
				"deliberately", label, *cfgPath)
		}
		return fmt.Errorf("no mesh called %q; try: shrooms mesh list", label)
	}

	fmt.Printf("Leaving %q.\n\n", label)
	fmt.Printf("  network key  %s\n", m.NetworkKey)
	if len(m.AdminKeys) > 0 {
		fmt.Printf("  admin keys   %d — this mesh admits by credential\n", len(m.AdminKeys))
	}
	fmt.Println()
	fmt.Println("That key is the membership. Save it if there is any chance you want")
	fmt.Println("back in: it is not written anywhere else once this is done, and no")
	fmt.Println("admin can reissue it — it is the mesh, not a permission.")
	fmt.Println()
	fmt.Println("The mesh's admin key, if this device holds it, is left alone: that")
	fmt.Println("belongs to the mesh rather than to this device. So is this mesh's")
	fmt.Println("state, so rejoining comes back as the same device.")

	if !*yes {
		fmt.Println()
		ans, err := readSecret("Type the mesh's name to confirm: ")
		if err != nil {
			return err
		}
		if strings.TrimSpace(ans) != label {
			return errors.New("stopped, and nothing was changed")
		}
	}

	// Before the set changes, or every mesh after this one in label order takes
	// a different interface and a different port.
	pinInterfacesAndPorts(&cfg)
	iface := cfg.MeshSet[label].Interface
	delete(cfg.MeshSet, label)

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := state.WriteConfig(*cfgPath, cfg); err != nil {
		return err
	}

	// The same treatment init got, for the same reason and the opposite
	// direction: a daemon reads its mesh set at startup, so removing an entry
	// leaves the interface up and the mesh running until something restarts it.
	// Telling somebody to do that by hand is how `init` used to end, and it
	// produced a config and a daemon that disagreed about which meshes exist.
	//
	// Only when one is actually running — writing a config on a machine with no
	// daemon is normal, and this is a convenience rather than a step.
	if _, err := fetchStatus(*sock); err == nil {
		if askRestart(*sock) {
			fmt.Printf("\nRemoved, and the daemon is restarting to drop %s.\n", ifaceName(iface))
			fmt.Printf("The other meshes reconnect in a few seconds.\n")
			return nil
		}
		fmt.Printf("\nRemoved, but the daemon would not restart itself, so %s is\n", ifaceName(iface))
		fmt.Printf("still up. Restart it:\n  sudo systemctl restart shrooms\n")
		return nil
	}
	fmt.Printf("\nRemoved. Restart the daemon to drop the interface.\n")
	return nil
}

// ifaceName describes the interface a removed mesh was using, for a device that
// never pinned one and has nothing to name.
func ifaceName(iface string) string {
	if iface == "" {
		return "its interface"
	}
	return iface
}
