package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vpavlin/shrooms/internal/state"
)

// Checking a hand-edited config before it is the daemon's problem.
//
// This exists because of what it costs to find out the other way. A misconfigured
// service used to fail Validate, so the daemon refused to start, so the mesh did
// not come up — and on a machine reached over that mesh, the tunnel that was the
// way in to fix the typo went with it. Services no longer block startup (see
// state.Validate), which removes the trap and leaves a question: where does
// somebody find out they typed it wrong?
//
// Here, before restarting anything. `shrooms config validate` reads the file the
// daemon would read, reports everything wrong with it rather than the first
// thing, and separates what would stop the mesh from what would only cost a
// feature — because those two deserve different reactions.
func cmdConfig(args []string) error {
	if len(args) == 0 {
		configUsage()
		return fmt.Errorf("config needs a subcommand")
	}
	switch args[0] {
	case "validate", "check":
		return cmdConfigValidate(args[1:])
	case "set":
		return cmdConfigSet(args[1:])
	case "settings", "list":
		// --names prints one setting per line and nothing else, for shell
		// completion. Parsing the human list instead means completion breaks
		// the day somebody rewords the help — and breaks quietly, by offering
		// the wrong words.
		if len(args) > 1 && args[1] == "--names" {
			for _, st := range settings() {
				fmt.Println(st.name)
			}
			return nil
		}
		configSetUsage()
		return nil
	case "-h", "--help", "help":
		configUsage()
		return nil
	default:
		configUsage()
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func configUsage() {
	fmt.Fprint(os.Stderr, `Usage:
  shrooms config validate           check the config the daemon would read
  shrooms config set <k> <v>        change one setting, through the daemon
  shrooms config settings           what can be set

`)
}

func cmdConfigValidate(args []string) error {
	fs := flag.NewFlagSet("config validate", flag.ExitOnError)
	cfgPath := fs.String("config", state.DefaultConfigPath, "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := state.ConfigPath(*cfgPath)

	// Unvalidated first: a parse error is a different kind of wrong from a
	// value that is out of range, and saying "network_key is not set" about a
	// file with an unbalanced quote sends somebody looking in the wrong place.
	cfg, err := state.LoadConfigUnvalidated(path)
	if err != nil {
		fmt.Printf("%s\n\n  the file could not be read: %v\n\n", path, err)
		return fmt.Errorf("config unreadable")
	}

	var fatal, cosmetic []string

	if err := cfg.Validate(); err != nil {
		fatal = append(fatal, err.Error())
	}
	// Everything below is a feature failing, not a mesh failing. Reported
	// separately and deliberately not as an error, because the daemon will
	// start and the operator should know what they will not get.
	if err := cfg.ValidateServices(); err != nil {
		cosmetic = append(cosmetic, err.Error())
	}
	// MeshSet, not Meshes(): the latter synthesises the default mesh from the
	// top-level fields, so a single-mesh config would have its one service list
	// reported twice under two names.
	for label, m := range cfg.MeshSet {
		mc := cfg
		mc.Services = m.Services
		if err := mc.ValidateServices(); err != nil {
			cosmetic = append(cosmetic, fmt.Sprintf("mesh %s: %v", label, err))
		}
	}
	sort.Strings(cosmetic)

	fmt.Printf("%s\n\n", path)
	labels := make([]string, 0, len(cfg.Meshes()))
	for _, m := range cfg.Meshes() {
		labels = append(labels, m.Label)
	}
	sort.Strings(labels)
	fmt.Printf("  meshes      %s\n", strings.Join(labels, ", "))
	fmt.Printf("  name        %s\n", cfg.Name)
	fmt.Printf("  interface   %s, port %d\n\n", cfg.Interface, cfg.ListenPort)

	if len(fatal) == 0 && len(cosmetic) == 0 {
		fmt.Println("  looks good; the daemon would start on this.")
		return nil
	}
	for _, f := range fatal {
		fmt.Printf("  !! %s\n", f)
	}
	if len(fatal) > 0 {
		fmt.Println("\n  The daemon would NOT start. Fix these before restarting it —")
		fmt.Println("  a daemon that will not start takes the mesh with it, and on a")
		fmt.Println("  remote machine the mesh is how you would get back in.")
	}
	for _, c := range cosmetic {
		fmt.Printf("  -- %s\n", c)
	}
	if len(cosmetic) > 0 && len(fatal) == 0 {
		fmt.Println("\n  The daemon would start, and would not publish the service(s)")
		fmt.Println("  above. Nothing else is affected.")
	}
	fmt.Println()

	if len(fatal) > 0 {
		return fmt.Errorf("config would not load")
	}
	return nil
}
