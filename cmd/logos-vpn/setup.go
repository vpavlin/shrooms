package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/state"
)

// commonFlags registers --config and --state on a flag set.
func commonFlags(fs *flag.FlagSet) (cfgPath, stateDir *string) {
	cfgPath = fs.String("config", state.DefaultConfigPath, "config file")
	stateDir = fs.String("state", state.DefaultStateDir, "state directory")
	return
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	name := fs.String("name", "", "device name (default: hostname)")
	port := fs.Uint("port", 51820, "UDP listen port")
	advertise := fs.String("advertise", "", "public endpoint to announce, e.g. 203.0.113.4:51820")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(*cfgPath); err == nil {
		return fmt.Errorf("%s already exists — remove it or use a different --config", *cfgPath)
	}

	nk, err := identity.NewNetworkKey()
	if err != nil {
		return err
	}
	return setup(*cfgPath, *stateDir, nk, *name, uint16(*port), *advertise, true)
}

func cmdJoin(args []string) error {
	// The key is positional and comes first, which is the natural way to type
	// it. Go's flag package stops parsing at the first positional, so pull the
	// key off before parsing the flags.
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: logos-vpn join <NETWORK-KEY> [flags]")
	}
	keyArg := args[0]

	fs := flag.NewFlagSet("join", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	name := fs.String("name", "", "device name (default: hostname)")
	port := fs.Uint("port", 51820, "UDP listen port")
	advertise := fs.String("advertise", "", "public endpoint to announce, e.g. 203.0.113.4:51820")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	nk, err := identity.ParseNetworkKey(keyArg)
	if err != nil {
		return err
	}
	if _, err := os.Stat(*cfgPath); err == nil {
		return fmt.Errorf("%s already exists — remove it or use a different --config", *cfgPath)
	}
	return setup(*cfgPath, *stateDir, nk, *name, uint16(*port), *advertise, false)
}

// setup writes the config and generates the device identity.
func setup(cfgPath, stateDir string, nk identity.NetworkKey, name string, port uint16, advertise string, fresh bool) error {
	cfg := state.DefaultConfig()
	cfg.NetworkKey = nk.String()
	cfg.ListenPort = port
	if name != "" {
		cfg.Name = name
	}
	if advertise != "" {
		cfg.Advertise = []string{advertise}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return err
	}
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return err
	}

	addr := identity.OverlayAddr(nk, st.Identity.DevicePub)

	if fresh {
		fmt.Printf("Network key: %s\n", nk)
		fmt.Printf("  copy this to your other machines — it is the only secret\n\n")
	}
	fmt.Printf("Device:      %s\n", cfg.Name)
	fmt.Printf("Overlay IP:  %s\n", addr)
	fmt.Printf("Mesh prefix: %s\n", nk.Prefix())
	fmt.Printf("Wrote %s\n", cfgPath)
	if len(cfg.Advertise) == 0 {
		fmt.Printf("\nNote: no --advertise set. A publicly reachable node should set one,\n")
		fmt.Printf("or peers will have no dialable endpoint for it.\n")
	}
	fmt.Printf("\nNext: systemctl enable --now logos-vpn\n")
	return nil
}

func cmdKey(args []string) error {
	// Strip the sub-subcommand before parsing: Go's flag package stops at the
	// first positional argument, so `key show --config X` would otherwise leave
	// --config unparsed.
	if len(args) < 1 {
		return fmt.Errorf("usage: logos-vpn key {show|rotate} [--config PATH]")
	}
	sub := args[0]

	fs := flag.NewFlagSet("key "+sub, flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	yes := fs.Bool("yes", false, "skip the confirmation prompt (rotate only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}

	switch sub {
	case "show":
		fmt.Println(cfg.NetworkKey)
		return nil
	case "rotate":
		return rotateKey(*cfgPath, *stateDir, cfg, *yes)
	default:
		return fmt.Errorf("usage: logos-vpn key {show|rotate} [--config PATH]")
	}
}

// rotateKey replaces the network key.
//
// Until M5 this is the *only* revocation available, and it is blunt: the
// network key derives the mesh prefix, so every overlay address changes and
// every other device must re-join. It is closer to creating a new mesh than to
// rotating a credential — which is precisely the weakness credentials fix.
func rotateKey(cfgPath, stateDir string, cfg state.Config, yes bool) error {
	oldKey, err := cfg.Key()
	if err != nil {
		return err
	}
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return err
	}

	newKey, err := identity.NewNetworkKey()
	if err != nil {
		return err
	}

	fmt.Printf("Rotating the network key will:\n")
	fmt.Printf("  - change the mesh prefix   %s  ->  %s\n", oldKey.Prefix(), newKey.Prefix())
	fmt.Printf("  - change every overlay address, including this device's\n")
	fmt.Printf("  - disconnect every other device until it re-joins with the new key\n")
	fmt.Printf("  - invalidate every tunnel, since the per-pair PSKs derive from it\n\n")
	fmt.Printf("This is the only revocation available before M5. There is no way to\n")
	fmt.Printf("remove one device without re-enrolling all of them.\n\n")

	if !yes {
		fmt.Printf("Type the device name (%s) to confirm: ", cfg.Name)
		var typed string
		if _, err := fmt.Scanln(&typed); err != nil || typed != cfg.Name {
			return fmt.Errorf("aborted")
		}
		fmt.Println()
	}

	cfg.NetworkKey = newKey.String()
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return err
	}

	fmt.Printf("New network key: %s\n\n", newKey)
	fmt.Printf("This device:  %s\n", identity.OverlayAddr(newKey, st.Identity.DevicePub))
	fmt.Printf("Mesh prefix:  %s\n\n", newKey.Prefix())
	fmt.Printf("On every other device:\n")
	fmt.Printf("  logos-vpn join %s --name <NAME>\n", newKey)
	fmt.Printf("  systemctl restart logos-vpn\n\n")
	fmt.Printf("Then restart this one:  systemctl restart logos-vpn\n")
	return nil
}
