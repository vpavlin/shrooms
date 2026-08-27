package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/keycard"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
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
	advertise := fs.String("advertise", "", "public endpoint, only if it is not on a local interface")
	relay := fs.Bool("relay", false, "forward traffic for peers that cannot reach each other")
	adminDir := fs.String("admin-dir", defaultAdminDir(), "where to keep the admin key")
	noAdmin := fs.Bool("no-admin", false, "do not mint an authority; membership is the network key alone")
	sock := fs.String("socket", DefaultSocket, "control socket, so a waiting daemon picks this up")
	label := fs.String("mesh", "", "create an additional mesh with this name (ADR-015)")
	// The authority is a Keycard on a reader rather than a key in a file, so
	// nothing secret is written here and admitting a device needs the card.
	card := fs.Bool("keycard", false, "the authority is a Keycard on a reader")
	reader := fs.String("reader", "", "which reader, when several are attached")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	// A second mesh on a machine that already has one. Everything else about
	// the config stays as it is: this adds a network, it does not replace one.
	if *label != "" {
		return addMeshWith(*cfgPath, *stateDir, *adminDir, *label, *relay, *noAdmin, *sock, *card, *reader)
	}

	if _, err := os.Stat(*cfgPath); err == nil {
		return fmt.Errorf("%s already exists — remove it, use a different --config, "+
			"or add a second mesh with --mesh <name>", *cfgPath)
	}

	// Same for a first mesh: nothing is written until the card answers.
	if *card && !*noAdmin {
		if err := cardIsReachable(*reader); err != nil {
			return err
		}
	}

	nk, err := identity.NewNetworkKey()
	if err != nil {
		return err
	}
	if err := setup(*cfgPath, *stateDir, nk, *name, uint16(*port), *advertise, *relay, true); err != nil {
		return err
	}
	if *noAdmin {
		reportNext(*sock)
		return nil
	}

	// Minting the authority here rather than in a second command. Creating a
	// mesh is one act, and asking for two was the first half of why enrolling a
	// device had grown to six steps.
	if *card {
		if err := mintCardAuthorityFull(*adminDir, *cfgPath, *stateDir, *name, "", *reader); err != nil {
			return err
		}
		reportNext(*sock)
		return nil
	}
	if err := mintAuthority(*adminDir, *cfgPath, *stateDir, *name); err != nil {
		return err
	}
	reportNext(*sock)
	return nil
}

func cmdJoin(args []string) error {
	// One way in: an invite.
	//
	// `join <KEY>` is gone, and with it set-key — the whole path where a raw
	// network key makes somebody a member. That was the prototype: the key WAS
	// the membership, so everyone holding it was a member, nobody could be
	// removed without changing it for everybody, and it travelled by whatever
	// means somebody had to hand.
	//
	// Credentials replaced it (ADR-018) and invites carry the key sealed to one
	// device for fifteen minutes (ADR-017). Keeping a hand-pasted key beside
	// them left the weakest way in permanently available, which is not what a
	// superseded mechanism should be.
	if tok, rest, ok := inviteFlag(args); ok {
		return cmdJoinInvite(tok, rest)
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		// Almost certainly somebody pasting a network key, from habit or an old
		// note. Say what replaced it rather than "unknown flag".
		return errors.New("`shrooms join <KEY>` has been removed.\n\n" +
			"Joining with the network key was how this worked before credentials " +
			"existed: the key was the membership, so anybody holding it was a " +
			"member and nobody could be removed. An invite carries the same key " +
			"sealed to ONE device for fifteen minutes, and what makes that device " +
			"a member is an admin-signed credential that can be revoked.\n\n" +
			"On a machine already in the mesh:\n" +
			"    shrooms invite\n\n" +
			"and here, with the token it prints:\n" +
			"    sudo shrooms join --invite <TOKEN>")
	}
	return errors.New("usage: shrooms join --invite <TOKEN> [flags]\n\n" +
		"An invite comes from `shrooms invite` on a machine already in the mesh.")
}

// addMesh appends a mesh to a config that already names one (ADR-015).
//
// Deliberately not a variant of join: this mints a network rather than being
// admitted to one, and the difference matters — whoever runs this holds the new
// mesh's admin key and can enrol into it.
func addMesh(cfgPath, stateDir, adminDir, label string, relay, noAdmin bool, sock string) error {
	return addMeshWith(cfgPath, stateDir, adminDir, label, relay, noAdmin, sock, false, "")
}

// addMeshWith is addMesh, optionally with a Keycard as the new mesh's authority.
//
// Adding a mesh took the file path whatever was asked for, so `init --mesh work
// --keycard` silently minted a file authority — the flag was read and dropped.
// Which is the case a person actually has: a machine already on a mesh or three,
// making one more.
func addMeshWith(cfgPath, stateDir, adminDir, label string, relay, noAdmin bool, sock string, card bool, reader string) error {
	// The card, before a single byte is written.
	//
	// This used to mint the network key, write the mesh into the config, and
	// only then reach for the reader — so a build with no reader support, or a
	// card not on it, left a mesh in the config with no authority and no way to
	// give it one. A half-made mesh nothing can finish, and no command to
	// remove it.
	if card && !noAdmin {
		if err := cardIsReachable(reader); err != nil {
			return err
		}
	}
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("%w\n\n--mesh adds a network to an existing config; "+
			"run plain `shrooms init` first", err)
	}
	for _, m := range cfg.Meshes() {
		if m.Label == label {
			return fmt.Errorf("this node already has a mesh called %q", label)
		}
	}

	nk, err := identity.NewNetworkKey()
	if err != nil {
		return err
	}
	if err := appendMesh(cfgPath, label, nk.String(), relay); err != nil {
		return err
	}
	// Give the new mesh an interface and a port nothing else is using, and
	// write them down.
	//
	// Without this it took whatever its POSITION in the label-sorted list
	// implied — and a mesh that has been renamed carries a PINNED interface,
	// which the new one then collided with. `init --mesh kc` on a machine
	// where "home" was pinned to logos02 gave kc logos02 as well, and the
	// daemon crash-looped on "create tun logos02: device or resource busy",
	// taking every other mesh down with it.
	if err := pinNewMesh(cfgPath, label); err != nil {
		return err
	}

	fmt.Printf("Created the mesh %q.\n\n", label)
	fmt.Printf("  network key  %s\n", nk)
	fmt.Printf("  prefix       %s\n", nk.Prefix())
	fmt.Println()

	if !noAdmin && card {
		if err := mintCardAuthorityFull(adminDir, cfgPath, stateDir, cfg.Name, label, reader); err != nil {
			return err
		}
		reportNext(sock)
		return nil
	}
	if !noAdmin {
		if err := mintAuthorityFor(adminDir, cfgPath, stateDir, label, cfg.Name); err != nil {
			return err
		}
	}
	fmt.Printf("Restart the daemon to bring it up, then admit a device:\n")
	fmt.Printf("  shrooms invite --mesh %s\n", label)
	return nil
}

// appendMesh writes the prefixed keys for one mesh.
func appendMesh(cfgPath, label, key string, relay bool) error {
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# An additional mesh (ADR-015). Its own key, its own identity,\n"+
		"# its own interface and port.\nmesh.%s.key   = %q\n", label, key); err != nil {
		return err
	}
	if relay {
		_, err = fmt.Fprintf(f, "mesh.%s.relay = \"true\"\n", label)
	}
	return err
}

// inviteFlag pulls --invite out of the arguments, wherever it appears.
//
// Its own parse rather than a flag on the join set, because the network key is
// positional and Go's flag package stops at the first positional — so
// `join --invite X` and `join KEY` cannot share one FlagSet.
func inviteFlag(args []string) (token string, rest []string, ok bool) {
	for i, a := range args {
		switch {
		case a == "--invite" || a == "-invite":
			if i+1 >= len(args) {
				return "", nil, false
			}
			return args[i+1], append(append([]string{}, args[:i]...), args[i+2:]...), true
		case strings.HasPrefix(a, "--invite="), strings.HasPrefix(a, "-invite="):
			_, v, _ := strings.Cut(a, "=")
			return v, append(append([]string{}, args[:i]...), args[i+1:]...), true
		}
	}
	return "", nil, false
}

// setup writes the config and generates the device identity.
func setup(cfgPath, stateDir string, nk identity.NetworkKey, name string, port uint16, advertise string, relay, fresh bool) error {
	return setupMesh(cfgPath, stateDir, nk, name, "", port, advertise, relay, fresh)
}

// setupMesh is setup with a local name for the mesh (ADR-015). An empty label
// writes the single-mesh form, which is what init and `join <KEY>` want.
func setupMesh(cfgPath, stateDir string, nk identity.NetworkKey, name, label string, port uint16, advertise string, relay, fresh bool) error {
	return setupMeshWith(cfgPath, stateDir, nk, name, label, port, advertise, relay, fresh, nil)
}

// setupMeshWith is setupMesh with explicit bootstrap addresses.
//
// A joining device has to reach the rendezvous plane to redeem its invite, and
// with no addresses it uses the preset's — the public fleet. On a network that
// cannot reach it, or that deliberately does not, there was no way to say where
// to look: `join` writes the config, so there was nowhere to put the answer
// before it was needed.
func setupMeshWith(cfgPath, stateDir string, nk identity.NetworkKey, name, label string, port uint16, advertise string, relay, fresh bool, entry []string) error {
	cfg := state.DefaultConfig()
	cfg.NetworkKey = nk.String()
	cfg.ListenPort = port
	if len(entry) > 0 {
		cfg.EntryNodes = entry
	}
	if name != "" {
		cfg.Name = name
	}
	if advertise != "" {
		cfg.Advertise = []string{advertise}
	}
	cfg.Relay = relay
	if label != "" && label != state.DefaultLabel {
		cfg.NetworkKey, cfg.Relay = "", false
		cfg.MeshSet = map[string]state.Mesh{label: {
			Label: label, NetworkKey: nk.String(), Relay: relay,
		}}
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

	// Only suggest --advertise when it would actually help. A node with a
	// globally routable interface address announces it automatically, and a
	// NATed node learns its public address from the first peer that answers a
	// probe, so demanding one up front is wrong in both common cases.
	if cfg.Relay {
		fmt.Printf("\nThis node will relay for peers that cannot reach each other.\n")
	} else if mesh.HasGlobalAddr() {
		fmt.Printf("\nThis machine has a globally routable address, so it can relay for\n")
		fmt.Printf("peers that cannot reach each other directly. Not enabled by default,\n")
		fmt.Printf("since relaying spends this node's bandwidth. To turn it on:\n")
		fmt.Printf("  relay = \"true\"   in %s\n", cfgPath)
	}

	if len(cfg.Advertise) == 0 && !mesh.HasGlobalAddr() {
		fmt.Printf("\nThis machine has no globally routable address, so peers cannot\n")
		fmt.Printf("dial it until it learns its public address from one of them.\n")
		fmt.Printf("That happens automatically once any peer is reachable.\n\n")
		fmt.Printf("If it is reachable via a port forward, tell it so:\n")
		fmt.Printf("  advertise = [\"<public-ip>:%d\"]   in %s\n", cfg.ListenPort, cfgPath)
	}
	return nil
}

// reportNext nudges a waiting daemon and says what to do about the result.
//
// Printed after the nudge rather than before, and phrased from its outcome,
// because the three cases need three different instructions and the wrong one
// is worse than none:
//
//   - The nudge landed. Nothing to run; saying "enable --now" here invites
//     somebody to go looking for a problem that does not exist.
//   - A daemon is running and did not take it. It needs a *restart*, and this
//     is the case that used to print "enable --now" — which on an
//     already-running service does nothing at all. Somebody following that
//     instruction to the letter is left with a config, a daemon still waiting,
//     and no sign of which of the two is wrong. That is what happened to the
//     first person outside this project to try it.
//   - No daemon. Start one, which is the only case the old message fitted.
func reportNext(sock string) {
	if nudgeDaemon(sock) {
		fmt.Printf("\nThe daemon was waiting for this and is bringing the mesh up now.\n")
		fmt.Printf("Check it with:\n  shrooms status\n")
		return
	}

	unit := false
	if _, err := os.Stat("/etc/systemd/system/shrooms.service"); err == nil {
		unit = true
	}
	// Running, but did not take the nudge. Distinguished from "not running" so
	// the instruction matches: one needs starting, the other needs restarting.
	//
	// Whether or not it is WAITING. A daemon already carrying meshes cannot
	// adopt a new one either — it reads its mesh set at startup, and reload
	// updates what is running rather than starting anything — and this used to
	// fall through to "enable --now", which is a no-op on a service that is
	// already enabled and running. So the mesh was created, the advice did
	// nothing, and `invite --mesh` then said the mesh did not exist.
	if _, err := fetchStatus(sock); err == nil {
		// Ask it to restart itself rather than telling somebody to.
		//
		// A daemon reads its mesh set at startup, so a new mesh needs one — and
		// leaving that to the reader meant `init --mesh x` followed by `invite
		// --mesh x` failed with "no mesh called x", which reads as the mesh
		// never having been created.
		//
		// The endpoint refuses if nothing would start the daemon again, so this
		// cannot leave a mesh down by exiting; and it validates the config
		// first, so it cannot restart into one that will not load.
		if askRestart(sock) {
			fmt.Printf("\nThe daemon is restarting to bring it up — a new mesh is a new\n")
			fmt.Printf("interface, and those are created at startup. The other meshes\n")
			fmt.Printf("reconnect in a few seconds.\n\n")
			fmt.Printf("Then:\n  shrooms status\n")
			return
		}
		fmt.Printf("\nA daemon is running and reads its meshes at startup, so it has\n")
		fmt.Printf("not picked this up. Restart it:\n")
		if unit {
			fmt.Printf("  sudo systemctl restart shrooms\n")
		} else {
			fmt.Printf("  stop the running daemon and start it again\n")
		}
		fmt.Printf("\nThen `shrooms status` should name the mesh.\n")
		return
	}

	if unit {
		fmt.Printf("\nNext:\n")
		fmt.Printf("  sudo systemctl enable --now shrooms\n")
		return
	}
	fmt.Printf("\nNext, run it:\n")
	fmt.Printf("  sudo shrooms daemon -v\n")
	fmt.Printf("\nOr install it as a service first:\n")
	fmt.Printf("  sudo make install && sudo systemctl enable --now shrooms\n")
}

// cmdPrepare writes everything except the key.
//
// For setting a machine up without the key passing through whoever — or
// whatever — is doing the setting up. The device identity is generated here, so
// the machine has its own keys from the start; only the mesh key is left blank.
func cmdPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	name := fs.String("name", "", "device name (default: hostname)")
	port := fs.Uint("port", 51820, "UDP listen port")
	advertise := fs.String("advertise", "", "public endpoint, only if it is not on a local interface")
	relay := fs.Bool("relay", false, "forward traffic for peers that cannot reach each other")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := state.DefaultConfig()
	if *name != "" {
		cfg.Name = *name
	}
	cfg.ListenPort = uint16(*port)
	cfg.Relay = *relay
	if *advertise != "" {
		cfg.Advertise = []string{*advertise}
	}
	cfg.NetworkKey = state.KeyPlaceholder

	if err := state.WriteConfig(*cfgPath, cfg); err != nil {
		return err
	}
	// Generated now so the identity — and the overlay address derived from it —
	// is settled before the key arrives, and does not change when it does.
	if _, err := state.LoadOrCreateState(*stateDir); err != nil {
		return err
	}

	fmt.Printf("Prepared %s for %q.\n\n", *cfgPath, cfg.Name)
	fmt.Println("This device has its keys and its name. It is not on a mesh yet.")
	fmt.Println()
	fmt.Println("Start it, so it is listening when the invite arrives:")
	fmt.Println("  sudo systemctl enable --now shrooms")
	fmt.Println()
	fmt.Println("Then, on a machine already in the mesh:")
	fmt.Println("  shrooms invite")
	fmt.Println()
	fmt.Println("and back here, with the token it prints:")
	fmt.Println("  sudo shrooms join --invite <TOKEN>")
	return nil
}

func nudgeDaemon(sock string) bool {
	st, err := fetchStatus(sock)
	if err != nil || !st.Waiting {
		return false
	}
	resp, err := socketClient(sock, 10*time.Second).Post("http://unix/reload", "application/json", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode/100 == 2
}

// askRestart asks a running daemon to restart itself, and reports whether it
// agreed.
//
// Deliberately a request rather than a kill: the daemon refuses when nothing
// would start it again — exiting would then leave every mesh down to add one —
// and it checks the config loads before going. Both of those are judgements
// only it can make.
func askRestart(sock string) bool {
	resp, err := socketClient(sock, 15*time.Second).Post("http://unix/restart", "application/json", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode/100 == 2
}

// stdinReader is shared, because a bufio.Reader reads ahead: a second one
// would find the buffer already drained and report EOF. That is exactly what
// happened confirming a passphrase from a pipe — the first read swallowed both
// lines and the confirmation failed with "EOF".
var stdinReader *bufio.Reader

func stdin() *bufio.Reader {
	if stdinReader == nil {
		stdinReader = bufio.NewReader(os.Stdin)
	}
	return stdinReader
}

// readSecret reads one line without echoing it, falling back to plain input
// when there is no terminal — which is what makes `... | shrooms set-key`
// work in a script.
func readSecret(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := stdin().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

func cmdKey(args []string) error {
	// Strip the sub-subcommand before parsing: Go's flag package stops at the
	// first positional argument, so `key show --config X` would otherwise leave
	// --config unparsed.
	if len(args) < 1 {
		return fmt.Errorf("usage: shrooms key {show|rotate} [--config PATH]")
	}
	sub := args[0]

	fs := flag.NewFlagSet("key "+sub, flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	yes := fs.Bool("yes", false, "skip the confirmation prompt (rotate only)")
	asQR := fs.Bool("qr", false, "show the key as a QR code, for scanning from a phone")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}

	switch sub {
	case "show":
		if *asQR {
			return showKeyQR(cfg)
		}
		fmt.Println(cfg.NetworkKey)
		return nil
	case "rotate":
		return rotateKey(*cfgPath, *stateDir, cfg, *yes)
	default:
		return fmt.Errorf("usage: shrooms key {show|rotate} [--config PATH]")
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
	fmt.Printf("  shrooms join %s --name <NAME>\n", newKey)
	fmt.Printf("  systemctl restart shrooms\n\n")
	fmt.Printf("Then restart this one:  systemctl restart shrooms\n")
	return nil
}

// cardIsReachable checks a card can be talked to, before anything commits to
// there being one.
//
// Cheap and read-only: SELECT costs no pairing slot, no PIN attempt and no
// password. The point is the order — minting a mesh writes a network key into a
// config, and discovering afterwards that the reader is missing leaves a mesh
// that cannot be finished and cannot be removed.
func cardIsReachable(reader string) error {
	t, done, err := keycard.OpenReader(reader)
	if err != nil {
		return err
	}
	defer done()
	if _, err := keycard.Status(t); err != nil {
		return fmt.Errorf("a card is on the reader but did not answer: %w", err)
	}
	return nil
}

// pinNewMesh assigns a mesh an interface and port nothing else has, and records
// them.
//
// Derived names only work while every mesh derives one: as soon as any of them
// is pinned — which renaming and removing both do, so that they do not move
// other meshes' interfaces — a newly derived name can land on top of a pinned
// one. The collision is not detected anywhere; it surfaces as the daemon
// failing to create a tun device that already exists, which takes down every
// mesh on the node rather than the new one.
func pinNewMesh(cfgPath, label string) error {
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		return nil // nothing written, nothing to pin
	}

	taken := map[string]bool{}
	ports := map[uint16]bool{}
	for _, other := range cfg.Meshes() {
		if other.Label == label {
			continue
		}
		taken[other.Interface], ports[other.ListenPort] = true, true
	}

	// Start from what position would have given it, then step past anything
	// already spoken for — so an untouched config keeps the names it had.
	iface, port := "", uint16(0)
	for _, cand := range cfg.Meshes() {
		if cand.Label == label {
			iface, port = cand.Interface, cand.ListenPort
			break
		}
	}
	for n := 1; taken[iface] || ports[port]; n++ {
		iface = fmt.Sprintf("%s%d", cfg.Interface, n)
		port = cfg.ListenPort + uint16(n)
		if n > 64 {
			return fmt.Errorf("no free interface for %q; the config has too many meshes", label)
		}
	}

	m.Interface, m.ListenPort = iface, port
	cfg.MeshSet[label] = m
	if err := cfg.Validate(); err != nil {
		return err
	}
	return state.WriteConfig(cfgPath, cfg)
}
