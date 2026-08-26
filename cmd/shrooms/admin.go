package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/keycard"
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

// adminPathFor is where one mesh's admin key lives. A node may hold the
// authority for several meshes (ADR-015), and one file per mesh keeps them
// apart — including their passphrases, since they are different secrets
// protecting different networks.
func adminPathFor(dir, label string) string {
	if label == "" || label == state.DefaultLabel {
		return adminPath(dir)
	}
	return filepath.Join(dir, "admin-"+label+".json")
}

func cmdAdmin(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: shrooms admin {init|issue|renew|revoke|show} [flags]")
	}
	switch args[0] {
	case "init":
		return cmdAdminInit(args[1:])
	case "issue":
		return cmdAdminIssue(args[1:])
	case "renew":
		return cmdAdminRenew(args[1:])
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
	label := fs.String("mesh", "", "which mesh this authority is for (ADR-015)")
	card := fs.Bool("keycard", false, "the authority is a Keycard on a reader; nothing secret is stored here")
	reader := fs.String("reader", "", "which reader, when several are attached")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	// The path for the mesh being minted, not always admin.json. One file per
	// mesh is the whole point of adminPathFor, and checking the default here
	// meant `admin init --mesh foo` refused on any machine that already had a
	// default mesh - the exact machine a second mesh gets added to.
	path := adminPathFor(*dir, *label)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; minting again would create a different mesh",
			path)
	}

	if *card {
		return mintCardAuthorityAt(*dir, *label, *reader)
	}
	return mintAuthorityAt(*dir, *plain, "", "", "", *label)
}

// mintAuthority mints a mesh's authority, writes admin_keys into the config and
// issues this device its own credential — everything `init` needs so that
// creating a mesh is one command.
func mintAuthority(dir, cfgPath, stateDir, name string) error {
	return mintAuthorityAt(dir, false, cfgPath, stateDir, name, "")
}

// mintAuthorityFor does the same for an additional mesh.
func mintAuthorityFor(dir, cfgPath, stateDir, label, name string) error {
	return mintAuthorityAt(dir, false, cfgPath, stateDir, name, label)
}

func mintAuthorityAt(dir string, plain bool, cfgPath, stateDir, name, label string) error {
	// Before anything is generated: a mesh minted onto a filesystem that is
	// about to disappear is a mesh that ends when its credentials expire, and
	// nothing later in this function can detect that it happened.
	if err := refuseEphemeralMint(dir); err != nil {
		return err
	}
	path := adminPathFor(dir, label)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; minting again would create a different mesh", path)
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

	if err := ensureUserDir(dir); err != nil {
		return err
	}
	af := adminFile{
		Keys: []string{b32.EncodeToString(primary.Pub), b32.EncodeToString(recovery.Pub)},
	}
	if plain {
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
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}

	// Write the keys into the config and enrol this device, so a fresh mesh is
	// usable the moment init returns.
	enrolled := false
	if cfgPath != "" {
		if err := addAdminKeysFor(cfgPath, label, af.Keys); err != nil {
			return err
		}
		admin := &cred.Admin{Priv: primary.Priv, Pub: primary.Pub}
		// Which mesh this authority is for, and whether it is the device's
		// first — the one whose identity predates there being more than one.
		if netID, legacy, err := meshIdentityOf(cfgPath, label); err == nil {
			if err := issueLocal(admin, auth, stateDir, name, netID, legacy, 1); err == nil {
				enrolled = true
			}
		}
	}

	fmt.Printf("\nMinted the mesh authority.\n\n")
	fmt.Printf("  mesh id     %s\n", auth.ID())
	fmt.Printf("  admin key   %s\n", path)
	if enrolled {
		fmt.Printf("  this device is enrolled\n")
	}
	fmt.Println()
	if cfgPath == "" {
		fmt.Println("Put this in every node's config — these are public values:")
		fmt.Printf("  admin_keys = [%q, %q]\n", af.Keys[0], af.Keys[1])
		fmt.Println()
	}
	fmt.Println("RECOVERY KEY — written down now or never. It is not saved anywhere,")
	fmt.Println("and it is what lets you keep this mesh if the admin key above is lost:")
	fmt.Printf("\n  %s\n\n", base64.StdEncoding.EncodeToString(recovery.Priv))
	fmt.Println("Store it away from this machine. A password manager, a Keycard, paper.")
	return nil
}

// meshIdentityOf resolves a label to the mesh's network id, and says whether it
// is the mesh this device already belonged to.
//
// Not "the first one in the list": a mesh labelled "aaa" sorts before the
// single-mesh "default", and taking position for history would hand a new mesh
// the identity of the old one — changing this device's address on the mesh it
// was already using.
func meshIdentityOf(cfgPath, label string) (string, bool, error) {
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return "", false, err
	}
	want := label
	if want == "" {
		want = state.DefaultLabel
	}
	for _, m := range cfg.Meshes() {
		if m.Label != want {
			continue
		}
		id, err := m.NetworkID()
		return id, isLegacyMesh(cfg, m), err
	}
	return "", false, fmt.Errorf("no mesh called %q in %s", want, cfgPath)
}

// isLegacyMesh reports whether a mesh is the one written as network_key — the
// device's original mesh, whose identity must not be re-derived.
func isLegacyMesh(cfg state.Config, m state.Mesh) bool {
	return cfg.NetworkKey != "" && m.Label == state.DefaultLabel
}

// addAdminKeysFor appends an authority to a config, for one mesh or for the
// single-mesh form.
func addAdminKeysFor(cfgPath, label string, keys []string) error {
	if label == "" || label == state.DefaultLabel {
		return addAdminKeys(cfgPath, keys)
	}
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "mesh.%s.admin_keys = [%q, %q]\n", label, keys[0], keys[1])
	return err
}

// addAdminKeys appends the authority to a config that already exists.
func addAdminKeys(cfgPath string, keys []string) error {
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# The admin keys this mesh trusts to sign membership. Public values,\n"+
		"# fixed when the mesh was minted: the mesh id commits to the set.\nadmin_keys = [%q, %q]\n",
		keys[0], keys[1])
	return err
}

// issueLocal enrols the device whose state directory this is, on one mesh.
//
// networkID and legacy decide *which identity* is named. A device has one per
// mesh (ADR-015), so issuing against the single-mesh identity — as this did —
// produced a credential naming the wrong keys, stored in the wrong slot. On a
// second mesh that meant announcing with no credential at all, which every
// peer of a mesh with admin keys correctly refuses. The symptom is a peer that
// appears in the roster on one side, never on the other, and whose handshakes
// are dropped by a WireGuard that has never heard of it.
func issueLocal(admin cred.Signer, auth *cred.Authority, stateDir, name, networkID string, legacy bool, serial uint64) error {
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return err
	}
	ms, err := st.MeshState(networkID, legacy)
	if err != nil {
		return err
	}
	raw, err := cred.IssueFor(admin, auth, ms.Identity.DevicePub, ms.Identity.WGPub[:],
		ms.Identity.SealPub[:], name, serial, time.Now(), cred.DefaultLife)
	if err != nil {
		return err
	}
	return st.SetMeshCredentialFor(networkID, legacy, raw)
}

func loadAdmin(dir string) (*cred.Admin, *cred.Authority, error) {
	return loadAdminFor(dir, "")
}

// signerFor returns what will sign, which is either the key in the admin file
// or something outside this process entirely (ADR-022).
//
// The authority comes from the same file either way: it is public, it is what
// every node checks against, and reading it needs no passphrase and no card.
// So a detached signer can verify what it is handed without holding anything
// secret at all.
func signerFor(dir, label, signWith string, external bool) (cred.Signer, *cred.Authority, error) {
	if !external && signWith == "" {
		// A mesh whose authority is a card has no private key in that file and
		// never will, so there is nothing to load and nothing to ask for a
		// passphrase. Reach for the reader instead: it is not a fallback, it is
		// the only thing that can sign for this mesh.
		//
		// CardOnly rather than a flag in the file, because it is the same
		// question every node already answers about the same bytes — every
		// admin key is a compressed secp256k1 point, so no file key can sign.
		if auth, err := authorityFor(dir, label); err == nil && auth.CardOnly() {
			return cardSignerFor(dir, auth)
		}
		return loadAdminFor(dir, label)
	}
	auth, err := authorityFor(dir, label)
	if err != nil {
		return nil, nil, err
	}
	return &externalSigner{auth: auth, command: signWith}, auth, nil
}

// cardSignerFor opens the card this mesh's authority lives on.
//
// The reader connection is left open deliberately. pcscd holds it exclusively
// until the process exits, and the signer is used after this returns — closing
// it here would hand back something that cannot sign. A command-line process
// exits promptly and the connection goes with it; a long-running one would need
// this to return a closer instead.
func cardSignerFor(dir string, auth *cred.Authority) (cred.Signer, *cred.Authority, error) {
	t, _, err := keycard.OpenReader("")
	if err != nil {
		return nil, nil, fmt.Errorf("this mesh's authority is a Keycard, so it has to "+
			"sign: %w", err)
	}
	pin, err := readSecret("Card PIN: ")
	if err != nil {
		return nil, nil, err
	}
	signer, err := keycard.NewSigner(t, keycardDir(dir), strings.TrimSpace(pin))
	if err != nil {
		return nil, nil, err
	}
	if !auth.Has(signer.Public()) {
		return nil, nil, errors.New("that card is not this mesh's authority — the key " +
			"it signs with is not in admin_keys")
	}
	return signer, auth, nil
}

// authorityFor reads the public half of an admin file: the keys this mesh
// trusts, and nothing that needs unlocking.
func authorityFor(dir, label string) (*cred.Authority, error) {
	path := adminPathFor(dir, label)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no admin file at %s: %w", path, err)
	}
	var af adminFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	keys := make([]ed25519.PublicKey, 0, len(af.Keys))
	for _, k := range af.Keys {
		b, err := b32.DecodeString(strings.ToUpper(k))
		if err != nil {
			return nil, fmt.Errorf("admin key %q: %w", k, err)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return cred.NewAuthority(keys...)
}

// loadAdminFor opens one mesh's admin key.
func loadAdminFor(dir, label string) (*cred.Admin, *cred.Authority, error) {
	path := adminPathFor(dir, label)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("no admin key at %s.\n"+
			"It is created by `shrooms init` on the machine that made the mesh, and "+
			"stays there — this command has to run where it lives", path)
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
	cfgPath := fs.String("config", state.DefaultConfigPath, "config file, to resolve --mesh")
	label := fs.String("mesh", "", "which mesh to enrol this device on (ADR-015)")
	name := fs.String("name", "", "the device's name")
	life := fs.Duration("life", cred.DefaultLife, "how long the credential is valid")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	serial := fs.Uint64("serial", 0, "credential serial; must increase per device (default: now)")
	devHex := fs.String("device", "", "the device's public key, hex (for a remote device)")
	wgHex := fs.String("wg", "", "the device's tunnel key, hex (for a remote device)")
	sealHex := fs.String("seal", "", "the device's control-plane sealing key, hex (optional; without it this issues a version 1 credential)")
	write := fs.Bool("write", true, "store the credential in the device's state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remote := *devHex != "" || *wgHex != ""
	if remote && (*devHex == "" || *wgHex == "") {
		return errors.New("--device and --wg go together: both name the same machine")
	}

	admin, auth, err := signerFor(*dir, *label, *signWith, *external)
	if err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required: a credential names the device it admits")
	}

	var devPub, wgPub []byte
	var networkID string
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
		// The identity for *this mesh*. A device has one per mesh, so issuing
		// against the single-mesh identity would name the wrong keys — and a
		// credential naming keys the announce is not signed with is refused by
		// every peer, correctly and silently.
		netID, legacy, idErr := meshIdentityOf(*cfgPath, *label)
		if idErr != nil {
			return idErr
		}
		ms, msErr := st.MeshState(netID, legacy)
		if msErr != nil {
			return msErr
		}
		devPub, wgPub = ms.Identity.DevicePub, ms.Identity.WGPub[:]
		networkID = netID
	}

	now := time.Now()
	sealPub, err := parseOptionalKey(*sealHex)
	if err != nil {
		return fmt.Errorf("sealing key: %w", err)
	}
	raw, err := cred.IssueFor(admin, auth, devPub, wgPub, sealPub, *name, *serial, now, *life)
	if err != nil {
		return err
	}
	c, err := cred.UnmarshalCredential(raw)
	if err != nil {
		return err
	}
	local := !remote && *write
	if local {
		if err := st.SetMeshCredential(networkID, raw); err != nil {
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
	// `issue` and `renew` both take this and `revoke` did not, which made it
	// the one command that could only ever act on the default mesh - signing
	// with the wrong authority, stamping the wrong mesh id, and being told
	// "Published" for a device that stayed a member.
	label := fs.String("mesh", "", "which mesh to revoke this device from (ADR-015)")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	// Zero means "everything issued up to now", which is what revoking a device
	// means. Serials are unix seconds by default (see issueFor), so a
	// timestamp covers every credential that device has and none it cannot yet
	// have been given.
	serial := fs.Uint64("serial", 0, "revoke this serial and everything below it (default: now)")
	// How long peers must keep this. Past it, the credentials it withdraws have
	// expired on their own and the revocation withdraws nothing, so holders may
	// drop it and the list stops growing forever.
	//
	// Zero means "keep it indefinitely", which is what every revocation issued
	// before this flag existed means, and it is the safe direction: too long
	// wastes a few hundred bytes, too short walks a removed device back on.
	// Default is the credential life plus a day of slack, which is right unless
	// this device was issued something longer than the default --life.
	keep := fs.Duration("keep-for", cred.DefaultLife+24*time.Hour,
		"how long peers keep this revocation; 0 keeps it forever. Must outlast "+
			"the longest credential this device holds")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	publish := fs.Bool("publish", true, "hand it to the local daemon to put on the mesh")
	rotate := fs.Bool("rotate", false,
		"also rotate the announce generation, so the revoked device stops being "+
			"able to READ the control plane (see docs/revocation-and-the-network-key.md)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *serial == 0 {
		*serial = uint64(time.Now().Unix())
	}
	admin, auth, err := signerFor(*dir, *label, *signWith, *external)
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

	// Built here and signed through the interface, rather than by a method on
	// the in-memory key: the signer may be a card on the other side of a
	// terminal, and revocation is exactly the operation you want to be able to
	// perform from one.
	serialOf := *serial
	if serialOf == 0 {
		serialOf = uint64(time.Now().Unix())
	}
	r := &cred.Revocation{DevicePub: append([]byte(nil), dev...), Serial: serialOf}
	r.MeshID = auth.ID()
	if *keep > 0 {
		r.NotAfter = time.Now().Add(*keep).Unix()
	}
	if err := cred.SignRevocationWith(admin, r); err != nil {
		return err
	}
	raw, err := r.MarshalBinary()
	if err != nil {
		return err
	}

	fmt.Printf("Revoked %x, serial %d and below.\n", dev[:8], *serial)
	if r.NotAfter > 0 {
		// Said out loud because it is the one way to get this wrong: a device
		// holding a credential issued with a longer --life would come back when
		// peers forget the revocation.
		fmt.Printf("Peers keep this until %s (--keep-for %s).\n",
			time.Unix(r.NotAfter, 0).Format("2006-01-02 15:04"), *keep)
		fmt.Println("Pass --keep-for 0 if this device was ever issued a longer credential.")
	} else {
		fmt.Println("Peers keep this indefinitely (--keep-for 0).")
	}
	fmt.Println()

	if *publish {
		if *rotate {
			if err := rotateAfter(*sock, *label, admin, auth, r.Serial); err != nil {
				fmt.Printf("\nRevoked, but NOT rotated: %v\n", err)
				fmt.Println("The device can no longer join or be peered with, and can")
				fmt.Println("still read announces. Re-run with --rotate when the daemon")
				fmt.Println("is reachable.")
				return nil
			}
			fmt.Println("\nRotated. Members will pick up the new generation within the")
			fmt.Println("hour; a device that is switched off gets it when it wakes.")
		}
		if err := publishRevocation(*sock, *label, raw); err != nil {
			fmt.Printf("Not published: %v\n\n", err)
			fmt.Printf("Hand it to a running node yourself:\n\n  %s\n\n",
				base64.StdEncoding.EncodeToString(raw))
			fmt.Println("Every node that sees it verifies the signature itself and passes")
			fmt.Println("it on, so one node is enough. The device stays out either way")
			fmt.Println("once its credential expires, which is what expiry is for.")
			return nil
		}
		fmt.Println("Published. Every node that sees it verifies the signature itself,")
		fmt.Println("drops the device, and passes it on — so a node that was offline")
		fmt.Println("learns it from whoever is up.")
		return nil
	}
	fmt.Printf("  %s\n\n", base64.StdEncoding.EncodeToString(raw))
	fmt.Println("Hand that to any running node to put it on the mesh.")
	return nil
}

// publishRevocation hands a signed revocation to the local daemon.
func publishRevocation(sock, label string, raw []byte) error {
	if sock == DefaultSocket {
		if _, err := os.Stat(sock); err != nil {
			if _, err := os.Stat(LegacySocket); err == nil {
				sock = LegacySocket
			}
		}
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 10 * time.Second,
	}
	body := strings.NewReader(base64.StdEncoding.EncodeToString(raw))
	url := "http://unix/revoke"
	if label != "" {
		url += "?mesh=" + neturl.QueryEscape(label)
	}
	resp, err := client.Post(url, "text/plain", body)
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the daemon refused it: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

func cmdAdminShow(args []string) error {
	fs := flag.NewFlagSet("admin show", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	label := fs.String("mesh", "", "which mesh's authority to show (ADR-015); omit to show every one held here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *label != "" {
		return showAuthority(*dir, *label)
	}

	// Every mesh this machine holds the authority for, not just the default.
	//
	// This used to show only admin.json, which is what `admin init` writes with
	// no --mesh. ADR-015 gave every mesh its own admin-<label>.json and this
	// command never caught up, so the one question it exists to answer — which
	// meshes am I the admin of — was answerable only for the first one.
	//
	// And it is the question somebody arrives with. A mesh label is a local
	// nickname: the machine that minted a mesh may call it something the other
	// members do not, so "do I have the key for the mesh that box is on" cannot
	// be answered by looking for a matching name. It is answered by comparing
	// admin_keys, which is what this prints.
	labels, err := adminLabelsIn(*dir)
	if err != nil {
		return err
	}
	if len(labels) == 0 {
		return fmt.Errorf("no admin keys in %s.\n"+
			"An admin key is written by `shrooms init` on the machine that minted "+
			"the mesh and stays there, so if a mesh was created elsewhere its key "+
			"is on that machine and not this one", *dir)
	}
	for i, l := range labels {
		if i > 0 {
			fmt.Println()
		}
		name := l
		if name == "" {
			name = state.DefaultLabel
		}
		fmt.Printf("=== %s (%s) ===\n", name, filepath.Base(adminPathFor(*dir, l)))
		if err := showAuthority(*dir, l); err != nil {
			fmt.Printf("unreadable: %v\n", err)
		}
	}
	return nil
}

// adminLabelsIn lists the meshes this directory holds an authority for.
//
// The empty string means the default file, which is what `admin init` with no
// --mesh writes; anything else is the label in admin-<label>.json.
func adminLabelsIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		switch {
		case n == adminFileName:
			out = append(out, "")
		case strings.HasPrefix(n, "admin-") && strings.HasSuffix(n, ".json"):
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(n, "admin-"), ".json"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// showAuthority prints one mesh's authority.
//
// The public half only: this reads the keys, never the private one, so it needs
// no passphrase and can be run to answer "which mesh is this" without unlocking
// anything.
func showAuthority(dir, label string) error {
	auth, err := authorityFor(dir, label)
	if err != nil {
		return err
	}
	fmt.Printf("mesh id  %s\n", auth.ID())
	fmt.Printf("prefix   %s\n", auth.ID().Prefix())
	fmt.Printf("keys     %d trusted\n", len(auth.Keys))
	if auth.CardOnly() {
		fmt.Printf("         every key is a card key\n")
	}
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
	// Under sudo, HOME is root's, and the admin key belongs to the person
	// rather than to the machine. `sudo shrooms invite` — which needs root only
	// to read the config — would otherwise look in /root/.config and report a
	// missing key that is sitting in the user's home.
	if u := os.Getenv("SUDO_USER"); u != "" {
		if home := homeOf(u); home != "" {
			return filepath.Join(home, ".config", "shrooms")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "shrooms")
	}
	return "."
}

// homeOf looks a user's home directory up without shelling out.
func homeOf(name string) string {
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// parseOptionalKey decodes a hex key that may be absent.
//
// Absent is not an error: a device that predates the control-plane sealing key
// has none to give, and the credential it gets is a version 1 credential, which
// is exactly what it would have got before.
func parseOptionalKey(h string) ([]byte, error) {
	if h == "" {
		return nil, nil
	}
	return hex.DecodeString(h)
}

// rotateAfter mints a generation secret, has the admin sign a statement naming
// it, and hands both to the local daemon.
//
// The generation number IS the revocation serial. Serials are unix seconds and
// strictly increasing per device, so this cannot repeat and cannot go backwards
// — and it binds the rotation to the withdrawal that caused it, which is what
// lets a member refuse to serve a generation while its own revocation list is
// still behind.
func rotateAfter(sock, label string, admin cred.Signer, auth *cred.Authority, serial uint64) error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate the generation secret: %w", err)
	}
	rot, err := cred.RotateWith(admin, auth, serial, serial, secret, time.Now())
	if err != nil {
		return err
	}
	raw, err := rot.MarshalBinary()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"rotation": base64.StdEncoding.EncodeToString(raw),
		"secret":   base64.StdEncoding.EncodeToString(secret),
	})
	if err != nil {
		return err
	}
	return postToDaemon(sock, "/rotate", label, body)
}

// postToDaemon sends a JSON body to one of the daemon's root endpoints.
//
// The same socket-finding as publishRevocation, including the fall back to the
// legacy path, so an admin command works on a machine that has not been
// restarted since the socket moved.
func postToDaemon(sock, path, label string, body []byte) error {
	if sock == DefaultSocket {
		if _, err := os.Stat(sock); err != nil {
			if _, err := os.Stat(LegacySocket); err == nil {
				sock = LegacySocket
			}
		}
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 15 * time.Second,
	}
	url := "http://unix" + path
	if label != "" {
		url += "?mesh=" + neturl.QueryEscape(label)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the daemon refused it: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

// mintCardAuthorityAt mints a mesh whose authority is a Keycard.
//
// One admin key, not two. The pair exists because losing the file ends a mesh,
// so a second key is minted and printed once as a paper way back — and a card's
// key is already reconstructible from the mnemonic written down when the card
// was initialised. A second key would be a second thing to lose, and worse than
// redundant: it would not be a card key, and `Authority.CardOnly` is every key
// or none, so one file key would disable the widening ADR-033 built.
//
// Nothing secret is written here. The admin file holds the card's public half,
// which is what every node checks against, and the private half has never
// existed outside the card except as that mnemonic.
func mintCardAuthorityAt(dir, label, readerName string) error {
	t, done, err := keycard.OpenReader(readerName)
	if err != nil {
		return err
	}
	defer done()

	pin, err := readSecret("Card PIN: ")
	if err != nil {
		return err
	}
	// The signer, not just the key: minting ends by issuing this device its own
	// credential, and that needs a signature. Opening it now means a wrong PIN
	// is reported before a mesh id exists rather than after.
	signer, err := keycard.NewSigner(t, keycardDir(dir), strings.TrimSpace(pin))
	if err != nil {
		return err
	}
	auth, err := cred.NewAuthority(signer.Public())
	if err != nil {
		return err
	}

	if err := ensureUserDir(dir); err != nil {
		return err
	}
	af := adminFile{Keys: []string{b32.EncodeToString(signer.Public())}}
	raw, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	path := adminPathFor(dir, label)
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}

	fmt.Printf("\nMinted the mesh authority from the card.\n\n")
	fmt.Printf("  mesh id     %s\n", auth.ID())
	fmt.Printf("  prefix      %s\n", auth.ID().Prefix())
	fmt.Printf("  admin key   %s\n", path)
	fmt.Printf("  authority   %x\n", signer.Public())
	fmt.Println()
	fmt.Println("Nothing secret is stored here: that file holds the card's public half,")
	fmt.Println("which is what every node checks against. The card signs, and its key")
	fmt.Println("comes back only from the mnemonic it was initialised with.")
	fmt.Println()
	fmt.Println("Add it to a config with:")
	fmt.Printf("  admin_keys = [%q]\n", b32.EncodeToString(signer.Public()))
	return nil
}
