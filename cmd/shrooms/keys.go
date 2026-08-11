package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/state"
)

// cmdKeys prints this device's public keys, so an admin somewhere else can
// enrol it.
//
// The point is that nothing secret moves. These are the two public halves the
// credential names, and the admin key stays where it is — enrolling a VPS by
// copying the admin key onto it would defeat the whole separation.
func cmdKeys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	_, stateDir := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := state.LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}

	fmt.Printf("device  %x\n", st.Identity.DevicePub)
	fmt.Printf("tunnel  %x\n", st.Identity.WGPub[:])
	fmt.Println()
	fmt.Println("Enrol this machine from wherever the admin key is:")
	fmt.Printf("  shrooms admin issue --name <name> --serial 1 \\\n")
	fmt.Printf("      --device %x \\\n", st.Identity.DevicePub)
	fmt.Printf("      --wg %x\n", st.Identity.WGPub[:])

	if len(st.Credential) > 0 {
		c, err := cred.UnmarshalCredential(st.Credential)
		if err != nil {
			fmt.Printf("\ncredential: unreadable (%v)\n", err)
			return nil
		}
		fmt.Printf("\ncredential  %s, serial %d, expires %s\n",
			c.Name, c.Serial, time.Unix(c.NotAfter, 0).Format(time.RFC3339))
		if time.Now().After(time.Unix(c.NotAfter, 0)) {
			fmt.Println("            EXPIRED — ask the admin to issue another")
		}
	} else {
		fmt.Println("\ncredential  none yet")
	}
	return nil
}

// cmdCredential installs a credential an admin issued elsewhere.
// isFlagValue reports whether the previous token was a flag expecting a value,
// so `--state /tmp/x` does not have its path mistaken for the blob.
func isFlagValue(seen []string) bool {
	if len(seen) == 0 {
		return false
	}
	last := seen[len(seen)-1]
	return strings.HasPrefix(last, "-") && !strings.Contains(last, "=")
}

func cmdCredential(args []string) error {
	if len(args) < 1 || args[0] != "set" {
		return errors.New("usage: shrooms credential set <blob>")
	}
	// Go's flag package stops at the first positional, so `credential set BLOB
	// --state X` would parse no flags at all and quietly use the default state
	// directory. Pull the blob out first and let flags appear on either side of
	// it, because insisting they come first is a rule nobody remembers at the
	// moment they are pasting a long string.
	blob, rest := "", []string{}
	for _, a := range args[1:] {
		if blob == "" && !strings.HasPrefix(a, "-") && !isFlagValue(rest) {
			blob = a
			continue
		}
		rest = append(rest, a)
	}

	fs := flag.NewFlagSet("credential set", flag.ExitOnError)
	_, stateDir := commonFlags(fs)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if blob == "" {
		blob = fs.Arg(0)
	}
	if blob == "" {
		return errors.New("usage: shrooms credential set <blob> [--state PATH]")
	}

	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return fmt.Errorf("that is not a credential: %w", err)
	}
	// Parsed before it is stored, so a mistyped blob is refused here rather
	// than becoming a node that silently cannot prove membership.
	c, err := cred.UnmarshalCredential(raw)
	if err != nil {
		return fmt.Errorf("that is not a credential: %w", err)
	}

	st, err := state.LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}
	// It must name THIS device, or it will be refused by every peer and the
	// mistake would only show up as a mesh that ignores you.
	if fmt.Sprintf("%x", c.DevicePub) != fmt.Sprintf("%x", st.Identity.DevicePub) {
		return fmt.Errorf("this credential names device %x, but this machine is %x",
			c.DevicePub[:8], st.Identity.DevicePub[:8])
	}
	if err := st.SetCredential(raw); err != nil {
		return err
	}

	fmt.Printf("Installed a credential for %q, serial %d.\n", c.Name, c.Serial)
	fmt.Printf("Expires %s.\n\nRestart the daemon to announce it.\n",
		time.Unix(c.NotAfter, 0).Format(time.RFC3339))
	return nil
}
