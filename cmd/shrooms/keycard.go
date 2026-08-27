package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
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
		return errors.New("usage: shrooms keycard {status|init|pair|readers|free-slots|forget|reset}")
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
	case "reset":
		return cmdKeycardReset(args[1:])
	case "init":
		return cmdKeycardInit(args[1:])
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

	// Before pairing, not after: pairing consumes one of five slots on the
	// card and cannot be undone, so a directory that turns out to be
	// unwritable must fail while that is still free.
	if err := ensureUserDir(keycardDir(*dir)); err != nil {
		return err
	}
	// Account 0. Pairing is per CARD, not per mesh — the key it reports is
	// "this card is set up on this machine", and the first authority is what
	// answers that. A mesh minted later gets its own account and derives its
	// own key over the same pairing.
	key, err := keycard.Enrol(t, keycardDir(*dir), pass, pin, 0)
	if err != nil {
		return err
	}
	// Under sudo the enrolment lands in the invoking user's config directory
	// owned by root, and every later command that runs without sudo cannot
	// read its own pairing. Best effort: the pairing is already on the card
	// and a slot is already spent, so a failed chown is not worth undoing it
	// over.
	for _, f := range keycard.Files(keycardDir(*dir)) {
		if err := giveToUser(f); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
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

// cmdKeycardReset wipes a card, which is the only way back from a full set of
// pairing slots when no device holds one.
//
// Guarded by typing the words rather than a flag, because the cost is not
// obvious from the name: "reset" sounds like clearing settings and means
// destroying a key.
func cmdKeycardReset(args []string) error {
	fs := flag.NewFlagSet("keycard reset", flag.ExitOnError)
	readerName := fs.String("reader", "", "which reader, when several are attached")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	t, done, err := keycard.OpenReader(*readerName)
	if err != nil {
		return err
	}
	defer done()

	// What is on the card now, so the warning is about this card rather than
	// cards in general.
	if raw, err := keycard.Status(t); err == nil {
		var rep struct {
			HasKey bool   `json:"hasKey"`
			KeyUID string `json:"keyUID"`
		}
		_ = json.Unmarshal([]byte(raw), &rep)
		if rep.HasKey {
			fmt.Printf("This card holds a key (%s).\n\n", rep.KeyUID)
		}
	}
	fmt.Println("A factory reset destroys everything on it: the key, the PIN, the PUK")
	fmt.Println("and every pairing. The card goes back to uninitialised.")
	fmt.Println()
	fmt.Println("The key is recoverable ONLY from the mnemonic written down when the")
	fmt.Println("card was initialised. Without that it is gone, and any mesh minted")
	fmt.Println("against it can never admit another device.")
	fmt.Println()
	ans, err := readSecret("Type: wipe this card\n> ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(ans) != "wipe this card" {
		return errors.New("stopped, and nothing was sent to the card")
	}

	if err := keycard.Reset(t); err != nil {
		return err
	}
	fmt.Println("\nDone. Initialise it again with the Keycard app or keycard-cli,")
	fmt.Println("loading the same mnemonic to get the same key back.")
	return nil
}

// cmdKeycardInit sets up a blank card so shrooms needs no second tool.
//
// ADR-022 put this in the Keycard app on purpose — it is irreversible and
// decides what a card IS. That was about a phone settings screen and an
// accidental tap. On a command line, against a card somebody has physically
// inserted, the cost of NOT having it is a flow that stops halfway and says
// "now go and find another program".
func cmdKeycardInit(args []string) error {
	fs := flag.NewFlagSet("keycard init", flag.ExitOnError)
	readerName := fs.String("reader", "", "which reader, when several are attached")
	restore := fs.Bool("restore", false, "load an existing mnemonic instead of making one")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	t, done, err := keycard.OpenReader(*readerName)
	if err != nil {
		return err
	}
	defer done()

	pin, err := readSecret("New PIN (6 digits): ")
	if err != nil {
		return err
	}
	pin = strings.TrimSpace(pin)
	if len(pin) != 6 || strings.Trim(pin, "0123456789") != "" {
		return errors.New("a Keycard PIN is exactly six digits")
	}
	puk, err := readSecret("New PUK (12 digits, unblocks a locked PIN): ")
	if err != nil {
		return err
	}
	puk = strings.TrimSpace(puk)
	if len(puk) != 12 || strings.Trim(puk, "0123456789") != "" {
		return errors.New("a Keycard PUK is exactly twelve digits")
	}
	pass, err := readSecret("Pairing password (empty for the factory default): ")
	if err != nil {
		return err
	}
	pass = strings.TrimSpace(pass)
	if pass == "" {
		pass = keycard.DefaultPairingPassword
	}

	phrase := ""
	if *restore {
		typed, err := readSecret("Mnemonic phrase: ")
		if err != nil {
			return err
		}
		phrase = strings.Join(strings.Fields(typed), " ")
		if phrase == "" {
			return errors.New("no phrase given")
		}
	}

	got, err := keycard.Init(t, pin, puk, pass, phrase)
	if err != nil {
		return err
	}

	fmt.Println("\nInitialised.")
	if *restore {
		fmt.Println("The key was restored from the phrase you gave.")
	} else {
		fmt.Println("\nWrite this down. It is the ONLY way back to this key, and a mesh")
		fmt.Println("minted against a key that exists nowhere else dies with the card:")
		fmt.Printf("\n  %s\n\n", got)
		fmt.Println("It is a BIP-39 phrase, so it also restores into any wallet — which")
		fmt.Println("means storing it beside crypto backups is storing your mesh's root")
		fmt.Println("key there too.")
	}
	fmt.Println("\nNext:\n  shrooms keycard pair")
	return nil
}
