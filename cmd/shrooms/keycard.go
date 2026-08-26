package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/vpavlin/shrooms/internal/keycard"
)

// Driving a Keycard from a machine with a reader.
//
// The same ceremony the phone screen runs, in the same order and for the same
// reasons (docs/keycard-on-mobile.md): look at the card first, which costs no
// pairing slot, no PIN attempt and no password; then ask only for what that
// particular card needs.
//
// A card has five pairing slots and they are consumed permanently, so a command
// that pairs is one somebody should have decided to run. `status` is free and
// is what `admin init --keycard` will send you to when anything is wrong.

func cmdKeycard(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: shrooms keycard {status|readers|pair|free-slots|forget}")
	}
	switch args[0] {
	case "status":
		return cmdKeycardStatus(args[1:])
	case "readers":
		return cmdKeycardReaders(args[1:])
	case "pair":
		return cmdKeycardPair(args[1:])
	case "free-slots":
		return cmdKeycardFreeSlots(args[1:])
	case "forget":
		return cmdKeycardForget(args[1:])
	default:
		return fmt.Errorf("unknown keycard command %q; try: status, readers, pair, free-slots, forget", args[0])
	}
}

// keycardDir is where this machine keeps its pairing, beside the admin key it
// is the authority for. Not the state directory: a pairing belongs to the
// person, like the admin key, and not to the machine.
func keycardDir(adminDir string) string { return adminDir }

func cmdKeycardReaders(args []string) error {
	fs := flag.NewFlagSet("keycard readers", flag.ExitOnError)
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	rs, err := keycard.Readers()
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return errors.New("no smartcard reader found — is one plugged in?")
	}
	for _, r := range rs {
		fmt.Println(r)
	}
	return nil
}

// cmdKeycardStatus asks a card what it is, and costs nothing to run.
//
// SELECT and nothing else: no pairing slot, no PIN attempt, no password, and
// nothing on the card changes. It answers three of the four ways setting one up
// can fail before anything has been spent, which is why the phone's flow starts
// here and why this exists before anything that pairs.
func cmdKeycardStatus(args []string) error {
	fs := flag.NewFlagSet("keycard status", flag.ExitOnError)
	readerName := fs.String("reader", "", "which reader, when several are attached")
	asJSON := fs.Bool("json", false, "print the raw report")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	t, done, err := keycard.OpenReader(*readerName)
	if err != nil {
		return err
	}
	defer done()

	raw, err := keycard.Status(t)
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(raw)
		return nil
	}

	var rep struct {
		Applet        string `json:"applet"`
		Initialised   bool   `json:"initialised"`
		HasKey        bool   `json:"hasKey"`
		KeyUID        string `json:"keyUID"`
		FreeSlots     int    `json:"freeSlots"`
		MaxSlots      int    `json:"maxSlots"`
		Capabilities  string `json:"capabilities"`
		NeedsPassword bool   `json:"needsPassword"`
		Problem       string `json:"problem"`
		Summary       string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		fmt.Println(raw)
		return nil
	}

	fmt.Printf("applet       %s\n", rep.Applet)
	fmt.Printf("initialised  %v\n", rep.Initialised)
	if rep.HasKey {
		fmt.Printf("key          yes (%s)\n", rep.KeyUID)
	} else {
		fmt.Printf("key          none\n")
	}
	if rep.FreeSlots >= 0 {
		fmt.Printf("pairing      %d of %d slots free\n", rep.FreeSlots, rep.MaxSlots)
	}
	fmt.Printf("can do       %s\n", rep.Capabilities)
	if rep.NeedsPassword {
		fmt.Printf("pairing needs a password (applet below 4.0)\n")
	} else {
		fmt.Printf("pairing uses a certificate, so there is no password to type\n")
	}
	if rep.Problem != "" {
		fmt.Printf("\n!! %s\n", wrapAt(rep.Summary, 72))
		return nil
	}
	fmt.Printf("\n%s\n", wrapAt(rep.Summary, 72))
	return nil
}

// wrapAt breaks a sentence so a terminal does not, because these are long and
// the useful half is usually at the end.
func wrapAt(s string, n int) string {
	var b strings.Builder
	line := 0
	for _, w := range strings.Fields(s) {
		if line > 0 && line+1+len(w) > n {
			b.WriteString("\n   ")
			line = 3
		} else if line > 0 {
			b.WriteString(" ")
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}

// cmdKeycardPair pairs this machine with a card, once, and reads back the key
// it signs with.
//
// A card has five pairing slots and they are consumed permanently, so this is a
// command somebody has to decide to run rather than something that happens on
// the way to something else. `keycard status` first: it costs nothing and
// answers three of the four ways this fails before a slot is spent.
//
// Secrets are prompted for rather than taken as flags. A pairing password and a
// PIN in a shell history or a process list are worth exactly as much as they
// are in the file this writes.
func cmdKeycardPair(args []string) error {
	fs := flag.NewFlagSet("keycard pair", flag.ExitOnError)
	readerName := fs.String("reader", "", "which reader, when several are attached")
	dir := fs.String("dir", defaultAdminDir(), "where to keep the pairing")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	t, done, err := keycard.OpenReader(*readerName)
	if err != nil {
		return err
	}
	defer done()

	// Looked at first, always. Pairing a card that cannot be paired spends an
	// attempt to learn what SELECT would have said for nothing.
	raw, err := keycard.Status(t)
	if err != nil {
		return err
	}
	var rep struct {
		FreeSlots     int    `json:"freeSlots"`
		NeedsPassword bool   `json:"needsPassword"`
		Problem       string `json:"problem"`
		Summary       string `json:"summary"`
	}
	_ = json.Unmarshal([]byte(raw), &rep)
	switch rep.Problem {
	case "":
	case "no-slots":
		return fmt.Errorf("%s\n\nFreeing a slot needs a device that already holds "+
			"one — this machine does not, so it has to be done from one that does: "+
			"the phone's Keycard screen, or `shrooms keycard free-slots` on a "+
			"machine that is already paired", wrapAt(rep.Summary, 72))
	default:
		return errors.New(wrapAt(rep.Summary, 72))
	}

	pass := keycard.DefaultPairingPassword
	if rep.NeedsPassword {
		typed, err := readSecret("Pairing password (empty for the factory default): ")
		if err != nil {
			return err
		}
		if t := strings.TrimSpace(typed); t != "" {
			pass = t
		}
	} else {
		pass = "" // applet 4.0 and later pairs with a certificate
	}
	pin, err := readSecret("PIN: ")
	if err != nil {
		return err
	}
	pin = strings.TrimSpace(pin)

	key, err := keycard.Enrol(t, keycardDir(*dir), pass, pin)
	if err != nil {
		return err
	}
	fmt.Printf("\nPaired, and this card signs with:\n\n  %s\n\n", key)
	fmt.Printf("The pairing is in %s.\n", keycardDir(*dir))
	fmt.Printf("Mint a mesh whose authority is this card with:\n")
	fmt.Printf("  shrooms admin init --keycard\n")
	return nil
}

// cmdKeycardFreeSlots releases every pairing slot except this machine's.
//
// Only possible from a machine that already holds one, which is the whole
// difficulty when a card is full: the slots can only be freed from inside a
// channel one of them opened.
func cmdKeycardFreeSlots(args []string) error {
	fs := flag.NewFlagSet("keycard free-slots", flag.ExitOnError)
	readerName := fs.String("reader", "", "which reader, when several are attached")
	dir := fs.String("dir", defaultAdminDir(), "where the pairing is kept")
	yes := fs.Bool("yes", false, "do it without asking")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if !*yes {
		fmt.Println("Every other device paired with this card stops being able to use it.")
		fmt.Println("That cannot be undone; they would each have to pair again.")
		ans, err := readSecret("Type yes to continue: ")
		if err != nil {
			return err
		}
		if strings.TrimSpace(ans) != "yes" {
			return errors.New("stopped")
		}
	}

	t, done, err := keycard.OpenReader(*readerName)
	if err != nil {
		return err
	}
	defer done()

	msg, err := keycard.UnpairOthers(t, keycardDir(*dir))
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// cmdKeycardForget deletes this machine's pairing, and says what it does not do.
func cmdKeycardForget(args []string) error {
	fs := flag.NewFlagSet("keycard forget", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the pairing is kept")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if err := keycard.Forget(keycardDir(*dir)); err != nil {
		return err
	}
	fmt.Println("Forgotten here. The card still counts this machine among the five")
	fmt.Println("devices it is paired with, so pairing again would take another slot —")
	fmt.Println("free it first with `shrooms keycard free-slots` if they are scarce.")
	return nil
}
