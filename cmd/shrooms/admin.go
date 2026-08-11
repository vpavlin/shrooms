package main

import (
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/state"
)

// The admin keys are the authority for a mesh, and they are the one thing here
// that is worth stealing: whoever holds them can admit a device. They live
// wherever you put them, deliberately — not on a participating node, which is
// the entire point of separating authority from participation (ADR-018).
//
// This file is the "somewhere" of last resort: a file you keep. It is written
// 0600, and the recovery key is printed once and not stored, so losing the file
// is survivable and finding the file is not sufficient.
const adminFileName = "admin.json"

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type adminFile struct {
	// Priv is the day-to-day signing key: enrolling and revoking. Base64 when
	// plain, or a sealed blob when Encrypted is set.
	Priv string `json:"priv"`

	// Encrypted says Priv is sealed under a passphrase (scrypt + XChaCha20).
	Encrypted bool `json:"encrypted,omitempty"`
	// Keys are every public key the mesh trusts, including ones whose private
	// halves are elsewhere — the recovery key, and later a renewal key.
	Keys []string `json:"keys"`
}

func adminPath(dir string) string { return filepath.Join(dir, adminFileName) }

func cmdAdmin(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: shrooms admin {init|issue|revoke|show} [flags]")
	}
	switch args[0] {
	case "init":
		return cmdAdminInit(args[1:])
	case "issue":
		return cmdAdminIssue(args[1:])
	case "revoke":
		return cmdAdminRevoke(args[1:])
	case "show":
		return cmdAdminShow(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}

// cmdAdminInit mints a mesh: two admin keys, of which only one is kept here.
//
// Two, always, because the set is fixed at mint — the mesh id commits to it and
// the address prefix derives from the id, so adding a key later re-addresses
// every node. One is for use; the other is recovery, printed once and never
// written, so that losing this file is not the end of the mesh.
func cmdAdminInit(args []string) error {
	fs := flag.NewFlagSet("admin init", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where to keep the admin key")
	plain := fs.Bool("no-passphrase", false, "store the admin key unencrypted")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(adminPath(*dir)); err == nil {
		return fmt.Errorf("%s already exists; minting again would create a different mesh",
			adminPath(*dir))
	}

	primary, err := cred.NewAdmin()
	if err != nil {
		return err
	}
	recovery, err := cred.NewAdmin()
	if err != nil {
		return err
	}
	auth, err := cred.NewAuthority(primary.Pub, recovery.Pub)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	af := adminFile{
		Keys: []string{b32.EncodeToString(primary.Pub), b32.EncodeToString(recovery.Pub)},
	}
	if *plain {
		af.Priv = base64.StdEncoding.EncodeToString(primary.Priv)
	} else {
		pass, err := readPassphraseTwice()
		if err != nil {
			return err
		}
		sealed, err := encryptAdminKey(primary.Priv, pass)
		if err != nil {
			return err
		}
		af.Priv, af.Encrypted = sealed, true
	}
	raw, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(adminPath(*dir), append(raw, '\n'), 0o600); err != nil {
		return err
	}

	fmt.Printf("Minted a mesh.\n\n")
	fmt.Printf("  mesh id     %s\n", auth.ID())
	fmt.Printf("  prefix      %s\n", auth.ID().Prefix())
	fmt.Printf("  admin key   %s\n", adminPath(*dir))
	fmt.Println()
	fmt.Println("Put this in every node's config — these are public values:")
	fmt.Printf("  admin_keys = [%q, %q]\n", af.Keys[0], af.Keys[1])
	fmt.Println()
	fmt.Println("RECOVERY KEY — written down now or never. It is not saved anywhere,")
	fmt.Println("and it is what lets you keep this mesh if the admin key above is lost:")
	fmt.Printf("\n  %s\n\n", base64.StdEncoding.EncodeToString(recovery.Priv))
	fmt.Println("Store it away from this machine. A password manager, a Keycard, paper.")
	return nil
}

func loadAdmin(dir string) (*cred.Admin, *cred.Authority, error) {
	raw, err := os.ReadFile(adminPath(dir))
	if err != nil {
		return nil, nil, fmt.Errorf("no admin key at %s — run `shrooms admin init`", adminPath(dir))
	}
	var af adminFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", adminPath(dir), err)
	}
	var priv []byte
	if af.Encrypted {
		pass, err := readSecret("Passphrase for the admin key: ")
		if err != nil {
			return nil, nil, err
		}
		priv, err = decryptAdminKey(af.Priv, strings.TrimRight(pass, "\r\n"))
		if err != nil {
			return nil, nil, err
		}
	} else {
		priv, err = base64.StdEncoding.DecodeString(af.Priv)
		if err != nil {
			return nil, nil, errors.New("admin key is unreadable")
		}
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("admin key is the wrong size")
	}
	keys := make([]ed25519.PublicKey, 0, len(af.Keys))
	for _, k := range af.Keys {
		b, err := b32.DecodeString(strings.ToUpper(k))
		if err != nil {
			return nil, nil, fmt.Errorf("admin key %q: %w", k, err)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	auth, err := cred.NewAuthority(keys...)
	if err != nil {
		return nil, nil, err
	}
	a := &cred.Admin{Priv: ed25519.PrivateKey(priv)}
	a.Pub = a.Priv.Public().(ed25519.PublicKey)
	if !auth.Trusts(a.Pub) {
		return nil, nil, errors.New("the stored admin key is not one this mesh trusts")
	}
	return a, auth, nil
}

// cmdAdminIssue signs a credential for one device.
//
// Two ways to name the device. Locally, --state reads its keys straight out of
// its state directory. Remotely, --device and --wg take the keys as printed by
// `shrooms keys` on that machine, and the credential comes back on stdout for
// `shrooms credential set` there.
//
// The remote form exists so the admin key never has to travel. Enrolling a VPS
// by copying the admin key to it would defeat the entire point of separating
// authority from participation.
func cmdAdminIssue(args []string) error {
	fs := flag.NewFlagSet("admin issue", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	stateDir := fs.String("state", state.DefaultStateDir, "the device's state directory")
	name := fs.String("name", "", "the device's name")
	life := fs.Duration("life", cred.DefaultLife, "how long the credential is valid")
	serial := fs.Uint64("serial", 0, "credential serial; must increase per device")
	devHex := fs.String("device", "", "the device's public key, hex (for a remote device)")
	wgHex := fs.String("wg", "", "the device's tunnel key, hex (for a remote device)")
	write := fs.Bool("write", true, "store the credential in the device's state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remote := *devHex != "" || *wgHex != ""
	if remote && (*devHex == "" || *wgHex == "") {
		return errors.New("--device and --wg go together: both name the same machine")
	}

	admin, auth, err := loadAdmin(*dir)
	if err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required: a credential names the device it admits")
	}

	var devPub, wgPub []byte
	var st *state.State
	if remote {
		if devPub, err = parseKey(*devHex, ed25519.PublicKeySize); err != nil {
			return fmt.Errorf("--device: %w", err)
		}
		if wgPub, err = parseKey(*wgHex, 32); err != nil {
			return fmt.Errorf("--wg: %w", err)
		}
	} else {
		st, err = state.LoadOrCreateState(*stateDir)
		if err != nil {
			return err
		}
		devPub, wgPub = st.Identity.DevicePub, st.Identity.WGPub[:]
	}

	now := time.Now()
	c, err := admin.Issue(devPub, wgPub, *name, *serial, now, *life)
	if err != nil {
		return err
	}
	// The mesh id is the authority's, not the signer's: a credential says which
	// mesh it admits you to, and this admin is one key among that mesh's set.
	c.MeshID = auth.ID()
	d, err := c.Digest()
	if err != nil {
		return err
	}
	c.Sig = ed25519.Sign(admin.Priv, d[:])

	raw, err := c.MarshalBinary()
	if err != nil {
		return err
	}
	local := !remote && *write
	if local {
		if err := st.SetCredential(raw); err != nil {
			return err
		}
	}
	fmt.Printf("Issued a credential for %q.\n\n", *name)
	fmt.Printf("  mesh     %s\n", auth.ID())
	fmt.Printf("  device   %x\n", devPub[:8])
	fmt.Printf("  serial   %d\n", c.Serial)
	fmt.Printf("  expires  %s (in %s)\n", time.Unix(c.NotAfter, 0).Format(time.RFC3339), *life)
	if local {
		fmt.Printf("\nStored in %s. Restart the daemon to announce it.\n", *stateDir)
		return nil
	}
	fmt.Printf("\nInstall it on that machine:\n\n  shrooms credential set %s\n",
		base64.StdEncoding.EncodeToString(raw))
	return nil
}

func parseKey(s string, want int) ([]byte, error) {
	var b []byte
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%x", &b); err != nil {
		return nil, fmt.Errorf("not hex: %w", err)
	}
	if len(b) != want {
		return nil, fmt.Errorf("is %d bytes, want %d", len(b), want)
	}
	return b, nil
}

// cmdAdminRevoke withdraws a device before its credential expires.
//
// Revocation is a signed statement that peers keep and check themselves, so it
// cannot be undone by a compromised node. Expiry remains the backstop: anyone
// can suppress a message on a gossip bus, and nobody can suppress a clock.
func cmdAdminRevoke(args []string) error {
	fs := flag.NewFlagSet("admin revoke", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	devHex := fs.String("device", "", "the device's public key, hex")
	serial := fs.Uint64("serial", 0, "revoke this serial and everything below it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	admin, auth, err := loadAdmin(*dir)
	if err != nil {
		return err
	}
	if *devHex == "" {
		return errors.New("--device is required")
	}
	var dev []byte
	if _, err := fmt.Sscanf(*devHex, "%x", &dev); err != nil || len(dev) != ed25519.PublicKeySize {
		return fmt.Errorf("--device must be a %d-byte hex key", ed25519.PublicKeySize)
	}

	r, err := admin.Revoke(dev, *serial, time.Now())
	if err != nil {
		return err
	}
	r.MeshID = auth.ID()
	d, err := r.Digest()
	if err != nil {
		return err
	}
	r.Sig = ed25519.Sign(admin.Priv, d[:])
	raw, err := r.MarshalBinary()
	if err != nil {
		return err
	}

	fmt.Printf("Revoked %x, serial %d and below.\n\n", dev[:8], *serial)
	fmt.Printf("  %s\n\n", base64.StdEncoding.EncodeToString(raw))
	fmt.Println("Distribution over the mesh is not built yet, so this has to be")
	fmt.Println("carried by hand for now. The device stays out either way once its")
	fmt.Println("credential expires, which is what expiry is for.")
	return nil
}

func cmdAdminShow(args []string) error {
	fs := flag.NewFlagSet("admin show", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, auth, err := loadAdmin(*dir)
	if err != nil {
		return err
	}
	fmt.Printf("mesh id  %s\n", auth.ID())
	fmt.Printf("prefix   %s\n", auth.ID().Prefix())
	fmt.Printf("keys     %d trusted\n", len(auth.Keys))
	fmt.Println()
	fmt.Println("admin_keys = [")
	for _, k := range auth.Keys {
		fmt.Printf("  %q,\n", b32.EncodeToString(k))
	}
	fmt.Println("]")
	return nil
}

// readPassphraseTwice asks once and confirms, because a mistyped passphrase on
// a key that is only used twice a year is discovered far too late to fix.
func readPassphraseTwice() (string, error) {
	first, err := readSecret("Passphrase for the admin key: ")
	if err != nil {
		return "", err
	}
	first = strings.TrimRight(first, "\r\n")
	if first == "" {
		return "", errors.New("an empty passphrase is not encryption; use --no-passphrase if that is what you want")
	}
	second, err := readSecret("Again: ")
	if err != nil {
		return "", err
	}
	if first != strings.TrimRight(second, "\r\n") {
		return "", errors.New("the two passphrases differ")
	}
	return first, nil
}

func defaultAdminDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "shrooms")
	}
	return "."
}
