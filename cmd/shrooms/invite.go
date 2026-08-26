package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/rendezvous"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/waku"
)

// Enrolment, from both ends.
//
// `invite` on a machine that is already a member, `join --invite` on the one
// that is not. Between them they replace the six steps enrolling a device had
// grown to: mint a token, type it on the other machine, done. The mesh key
// never appears on a screen, and the credential is issued in the same exchange
// rather than copied across afterwards.
//
// Both sides run their own short-lived Logos Delivery node. The joining device
// has no daemon yet by definition, and the inviter's daemon has no reason to
// hold an enrolment topic open — an invite is a thing a human is standing in
// front of for fifteen minutes, not a service.

func cmdInvite(args []string) error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	dir := fs.String("admin-dir", defaultAdminDir(), "where the admin key is kept")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	name := fs.String("name", "", "the joining device's name (it may choose its own)")
	ttl := fs.Duration("ttl", invite.DefaultTTL, "how long the invite stays open")
	life := fs.Duration("life", cred.DefaultLife, "how long the issued credential is valid")
	serial := fs.Uint64("serial", 0, "credential serial; must increase per device (default: now)")
	asQR := fs.Bool("qr", true, "show the token as a QR code")
	label := fs.String("mesh", "", "which mesh to admit the device to (ADR-015)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	// Which mesh this invite admits to. Named explicitly on a node with more
	// than one, because "whichever was first" is how someone ends up in the
	// wrong network.
	meshes := cfg.Meshes()
	target := meshes[0]
	if *label != "" {
		found := false
		for _, m := range meshes {
			if m.Label == *label {
				target, found = m, true
			}
		}
		if !found {
			return fmt.Errorf("this node has no mesh called %q", *label)
		}
	} else if len(meshes) > 1 {
		return fmt.Errorf("this node has %d meshes; say which one with --mesh <name>", len(meshes))
	}

	// Checked before the passphrase prompt. Typing a passphrase and then being
	// told the daemon is down is the wrong order to learn it in.
	client := socketClient(*sock, *ttl+time.Minute)
	st, err := fetchStatus(*sock)
	if err != nil {
		return fmt.Errorf("%w\n\nThe daemon holds the invite open, since it is the node already "+
			"connected to the fleet", err)
	}
	// A daemon with no mesh serves a smaller set of endpoints and /invite/hold
	// is not among them, so without this check the failure arrives as a bare
	// "404 page not found" after the passphrase prompt and the printed token —
	// which says nothing about what is wrong or what to do.
	//
	// It is a reachable state rather than a silly one: `init` writes the config
	// and nudges the daemon, and a nudge that does not land leaves a daemon
	// still waiting on a machine whose mesh plainly exists.
	if st.Waiting {
		return errors.New("this daemon has not joined a mesh yet, so it has nothing to invite anybody to.\n\n" +
			"If you have just created or joined one, the daemon has not picked it up:\n" +
			"  sudo systemctl restart shrooms\n\n" +
			"Then `shrooms status` should name the mesh rather than saying it is waiting.")
	}

	// The admin key only if this mesh has an authority. A mesh minted with
	// --no-admin has none, and an invite there is still worth having: it moves
	// the network key without putting it on a screen.
	var admin cred.Signer
	var auth *cred.Authority
	if len(target.AdminKeys) > 0 {
		admin, auth, err = signerFor(*dir, target.Label, *signWith, *external)
		if err != nil {
			return fmt.Errorf("%w\n\nThe invite has to issue a credential, which needs the admin key. "+
				"Run this on the machine that holds it.", err)
		}
		if cfgAuth, err := target.Authority(); err == nil && cfgAuth != nil && cfgAuth.ID() != auth.ID() {
			return fmt.Errorf("this admin key belongs to mesh %s, but the config is for %s",
				auth.ID(), cfgAuth.ID())
		}
	}

	secret, err := invite.New()
	if err != nil {
		return err
	}

	if len(meshes) > 1 {
		fmt.Printf("Admitting one device to %q.\n", target.Label)
	}
	fmt.Printf("Invite valid for %s. On the joining device:\n\n", *ttl)
	fmt.Printf("  shrooms join --invite %s\n\n", groupToken(secret.String()))
	// Somewhere for the joining device to reach the rendezvous plane, so it
	// does not depend on the public fleet answering (ADR-031). Best effort: an
	// invite without one still works exactly as it did.
	boot := inviteBootAddr(cfg, *stateDir)
	if boot != "" {
		fmt.Printf("  (the QR also carries %s to bootstrap from)\n\n", boot)
	}

	if *asQR {
		art, err := renderQR(secret.URIWithBoot(boot))
		if err != nil {
			fmt.Fprintf(os.Stderr, "(no QR: %v)\n", err)
		} else {
			fmt.Print(art, "\n")
		}
	}
	fmt.Printf("Waiting. An invite is answered only by the node that issued it,\n")
	fmt.Printf("which is what makes it single-use — so leave this running.\n\n")

	req, err := holdInviteOn(client, secret, *ttl, target.Label)
	if err != nil {
		return err
	}
	if req == nil {
		return errors.New("the invite expired before anyone used it")
	}

	joiner := req.Name
	if *name != "" {
		joiner = *name
	}
	if joiner == "" {
		joiner = "device"
	}
	fmt.Printf("Request from %q (%s).\n", joiner, req.DevicePub[:16])

	// Sign here, publish there. The admin key never reaches the daemon and the
	// network key never reaches this process: neither half admits a device on
	// its own, which is the whole point of having an authority at all.
	var credential []byte
	var issued *cred.Credential
	if admin != nil {
		devPub, err := hex.DecodeString(req.DevicePub)
		if err != nil {
			return fmt.Errorf("device key: %w", err)
		}
		wgPub, err := hex.DecodeString(req.WGPub)
		if err != nil {
			return fmt.Errorf("tunnel key: %w", err)
		}
		// Empty from a device that predates the sealing key, which issues a
		// version 1 credential and behaves exactly as before.
		sealPub, err := hex.DecodeString(req.SealPub)
		if err != nil {
			return fmt.Errorf("sealing key: %w", err)
		}
		if credential, err = cred.IssueFor(admin, auth, devPub, wgPub, sealPub, joiner, *serial, time.Now(), *life); err != nil {
			return err
		}
		if issued, err = cred.UnmarshalCredential(credential); err != nil {
			return err
		}
	}

	if err := replyInviteOn(client, secret, req.EphPub, joiner, credential, target.Label); err != nil {
		return err
	}

	fmt.Printf("Admitted %q.\n", joiner)
	if issued != nil {
		fmt.Printf("  credential serial %d, valid %s\n", issued.Serial, *life)
	} else {
		fmt.Printf("  no credential: this mesh has no admin keys\n")
	}
	return nil
}

// inviteRequest is what the daemon reports when someone redeems a token.
type inviteRequest struct {
	DevicePub string `json:"device_pub"`
	WGPub     string `json:"wg_pub"`
	SealPub   string `json:"seal_pub,omitempty"`
	Name      string `json:"name"`
	EphPub    string `json:"eph_pub"`
}

// holdInvite asks the daemon to listen on the invite topic and blocks until
// somebody redeems the token. Returns nil when the invite expires unused.
func holdInvite(client *http.Client, s invite.Secret, ttl time.Duration) (*inviteRequest, error) {
	return holdInviteOn(client, s, ttl, "")
}

// holdInviteOn names which mesh the invite admits to. Empty means the first,
// which is what a single-mesh node has always meant.
func holdInviteOn(client *http.Client, s invite.Secret, ttl time.Duration, meshLabel string) (*inviteRequest, error) {
	body, _ := json.Marshal(map[string]any{
		"token": s.String(), "ttl_s": int(ttl.Seconds()), "mesh": meshLabel,
	})
	resp, err := client.Post("http://unix/invite/hold", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hold the invite: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("the daemon refused the invite: %s", strings.TrimSpace(string(msg)))
	}
	var req inviteRequest
	if err := json.NewDecoder(resp.Body).Decode(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// replyInvite hands the daemon the credential to publish.
func replyInvite(client *http.Client, s invite.Secret, ephPub, name string, credential []byte) error {
	return replyInviteOn(client, s, ephPub, name, credential, "")
}

func replyInviteOn(client *http.Client, s invite.Secret, ephPub, name string, credential []byte, meshLabel string) error {
	body, _ := json.Marshal(map[string]any{
		"token":      s.String(),
		"eph_pub":    ephPub,
		"name":       name,
		"credential": base64.StdEncoding.EncodeToString(credential),
		"mesh":       meshLabel,
	})
	resp, err := client.Post("http://unix/invite/reply", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("publish the response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the daemon refused the response: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

// socketClient dials the daemon's unix socket. The timeout is generous because
// holding an invite open is a long poll by design.
func socketClient(sock string, timeout time.Duration) *http.Client {
	if sock == DefaultSocket {
		if _, err := os.Stat(sock); err != nil {
			if _, err := os.Stat(LegacySocket); err == nil {
				sock = LegacySocket
			}
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
		Timeout: timeout,
	}
}

// cmdJoinInvite is `join --invite`: the joining half.
//
// Two ways to run the exchange, and the protocol itself is neither of them —
// invite.Redeem is shared, so what differs is only where the node comes from.
// A daemon that is waiting for a mesh already has the socket and can bring the
// tunnel up the moment it joins; a machine with no daemon runs a node for the
// length of the exchange and throws it away.
func cmdJoinInvite(token string, args []string) error {
	fs := flag.NewFlagSet("join --invite", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	name := fs.String("name", "", "device name (default: hostname)")
	port := fs.Uint("port", 51820, "UDP listen port")
	advertise := fs.String("advertise", "", "public endpoint, only if it is not on a local interface")
	relay := fs.Bool("relay", false, "forward traffic for peers that cannot reach each other")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to wait for the inviter to answer")
	label := fs.String("mesh", "", "what to call this mesh on this machine (ADR-015)")
	local := fs.Bool("local", false, "do not use a running daemon even if there is one")
	verbose := fs.Bool("v", false, "show the rendezvous library's own logging")
	// Where to look for the mesh, when the preset's fleet is not reachable or
	// not wanted. Comma-separated multiaddrs; they go into the config this
	// writes, so the node keeps using them afterwards.
	entryNodes := fs.String("entry-node", "", "bootstrap addresses to use instead of the preset's")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}

	if _, err := invite.ParseToken(token); err != nil {
		return err
	}
	deviceName := *name
	if deviceName == "" {
		deviceName = state.DefaultConfig().Name
	}

	// A waiting daemon is the better end to do this at: it comes up on the mesh
	// straight away, with no restart and nothing for anyone to remember.
	if !*local {
		st, err := fetchStatus(*sock)
		switch {
		case err == nil && !st.Waiting:
			return errors.New("the daemon on this machine already has a mesh; " +
				"stop it and remove its config to join another one")
		case err == nil:
			return joinViaDaemon(*sock, token, deviceName, *label, uint16(*port), *advertise, *relay, *timeout)
		default:
			// Said out loud. This used to fall through in silence, and the
			// only symptom was a join that succeeded while the daemon carried
			// on waiting — with nothing anywhere to say which path had run.
			fmt.Printf("No daemon on %s (%v).\nJoining directly; "+
				"the daemon will be told when it is done.\n\n", *sock, err)
		}
	}

	if _, err := os.Stat(*cfgPath); err == nil {
		return fmt.Errorf("%s already exists — remove it or use a different --config", *cfgPath)
	}

	// The identity first, because it is what the credential names. Generating
	// it here rather than after the exchange means the keys in the request are
	// the keys this machine keeps.
	st, err := state.LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, *timeout)
	defer cancelTimeout()

	// Anything printed between here and unhush() has to go to `out`: fd 1 is
	// pointed at /dev/null for the length of the exchange.
	var out io.Writer = os.Stdout
	unhush := func() {}
	if !*verbose {
		if w, restore, err := hushStdout(); err == nil {
			out, unhush = w, restore
			defer unhush()
		}
	}

	// No config yet, so the defaults: preset, cluster and mode as shipped.
	//
	// The invite still says nothing about which *fleet* to use — the preset and
	// cluster are ours, because a device that could be told those could be told
	// to use somebody else's network entirely, permanently.
	//
	// It may carry one bootstrap address (ADR-031), which is narrower: a way in
	// to the same fleet, for this enrolment. Worth accepting because the
	// alternative is that a device cannot enrol at all when the public entry
	// nodes are refusing, which happened on 2026-08-20 and is the reason this
	// exists. And the trust it adds is small next to the trust already given:
	// whoever minted this token decides the mesh, hands over the network key,
	// and signs the credential. Somebody able to hand you a hostile invite has
	// no need of a hostile bootstrap address.
	fleet := state.DefaultConfig()
	if boot := invite.BootFromToken(token); boot != "" {
		fleet.EntryNodes = append(fleet.EntryNodes, boot)
		fmt.Fprintf(out, "Connecting to the fleet via %s...\n", boot)
	} else {
		fmt.Fprintln(out, "Connecting to the fleet...")
	}
	node, err := startNode(nodeConfig(fleet))
	if err != nil {
		return err
	}
	defer node.Close()

	secret, err := invite.ParseToken(token)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Asking to join as %q...\n", deviceName)

	base := &invite.Request{
		DevicePub: st.Identity.DevicePub,
		WGPub:     st.Identity.WGPub[:],
		SealPub:   st.Identity.SealPub[:],
		Name:      deviceName,
	}
	tr := rendezvous.InviteTransport(node)
	var resp *invite.Response
	// See perMeshRequest: only an additional mesh derives, because the first
	// one is what the single-mesh config form describes.
	if *label != "" && *label != state.DefaultLabel {
		resp, err = invite.RedeemForMesh(ctx, tr, secret, base,
			func(r *invite.Response) (*invite.Request, error) {
				return perMeshRequest(st, r, deviceName)
			})
	} else {
		resp, err = invite.Redeem(ctx, tr, secret, base)
	}
	if err != nil {
		return errors.New("no answer. Is `shrooms invite` still running on the other machine?")
	}

	// Everything from here is ours to print, and the node has nothing left to
	// say that anyone wants to read.
	unhush()

	nk, err := identity.NetworkKeyFromBytes(resp.NetworkKey)
	if err != nil {
		return err
	}
	if err := setupMeshWith(*cfgPath, *stateDir, nk, deviceName, *label, uint16(*port), *advertise, *relay, false, splitList(*entryNodes)); err != nil {
		return err
	}

	if len(resp.AdminKeys) > 0 {
		encoded := make([]string, 0, len(resp.AdminKeys))
		for _, k := range resp.AdminKeys {
			if !cred.ValidAdminKey(k) {
				return errors.New("the invite carried a malformed admin key")
			}
			encoded = append(encoded, b32.EncodeToString(k))
		}
		if err := addAdminKeys(*cfgPath, encoded); err != nil {
			return err
		}
	}
	if len(resp.Credential) > 0 {
		c, err := cred.UnmarshalCredential(resp.Credential)
		if err != nil {
			return fmt.Errorf("the invite carried a malformed credential: %w", err)
		}
		// Checked before it is stored: a credential that does not verify here
		// would fail silently later, as a mesh where nobody talks to us.
		cfg, err := state.LoadConfig(*cfgPath)
		if err != nil {
			return err
		}
		auth, err := cfg.Authority()
		if err != nil {
			return err
		}
		if auth != nil {
			if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
				return fmt.Errorf("the credential in the invite does not verify: %w", err)
			}
		}
		// Stored against the identity the credential actually names. For the
		// device's first mesh that is the base identity, which is what the
		// single-mesh config form means; for an additional mesh it is the one
		// derived in the second round of the exchange (ADR-017). Storing it
		// with the wrong flag derives a fresh identity beside a credential
		// naming another, and every peer then refuses this device — correctly,
		// and silently.
		legacy := *label == "" || *label == state.DefaultLabel
		if err := st.SetMeshCredentialFor(state.NetworkID(nk), legacy, resp.Credential); err != nil {
			return err
		}
		fmt.Printf("\nEnrolled. Credential serial %d, expires %s.\n",
			c.Serial, time.Unix(c.NotAfter, 0).Format(time.RFC3339))
	}

	// A daemon that was waiting has just had its mesh written out from under
	// it. Telling it is the difference between a node that is up and a node
	// that reports "waiting for a mesh" after a join that plainly worked.
	if nudgeDaemon(*sock) {
		fmt.Println("\nThe daemon was waiting for this and is bringing the mesh up now.")
	}
	return nil
}

// joinViaDaemon hands the token to a daemon that is waiting for a mesh.
func joinViaDaemon(sock, token, name, label string, port uint16, advertise string, relay bool, timeout time.Duration) error {
	fmt.Printf("Asking to join as %q, via the daemon...\n", name)

	body, _ := json.Marshal(map[string]any{
		"token": token, "name": name, "port": port, "mesh": label,
		"advertise": advertise, "relay": relay,
		"wait_s": int(timeout.Seconds()),
	})
	client := socketClient(sock, timeout+time.Minute)
	resp, err := client.Post("http://unix/join", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return errors.New(strings.TrimSpace(string(msg)))
	}

	var res struct {
		Mesh       string `json:"mesh"`
		Name       string `json:"name"`
		Overlay    string `json:"overlay"`
		Prefix     string `json:"prefix"`
		Credential bool   `json:"credential"`
		Serial     uint64 `json:"serial"`
		Expires    string `json:"expires"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}

	fmt.Printf("\nDevice:      %s\n", res.Name)
	if res.Mesh != "" && res.Mesh != state.DefaultLabel {
		fmt.Printf("Mesh:        %s\n", res.Mesh)
	}
	fmt.Printf("Overlay IP:  %s\n", res.Overlay)
	fmt.Printf("Mesh prefix: %s\n", res.Prefix)
	if res.Credential {
		fmt.Printf("Credential:  serial %d, expires %s\n", res.Serial, res.Expires)
	}
	// No restart to ask for: the daemon re-executes itself into the mesh it has
	// just joined, which is the whole reason for doing it at this end.
	fmt.Printf("\nThe daemon is bringing the mesh up now. Check it with:\n  shrooms status\n")
	return nil
}

// nodeConfig builds the rendezvous node's configuration from a config, matching
// what the daemon does. See daemon.go for why clusterId is conditional.
func nodeConfig(cfg state.Config) waku.Config {
	c := waku.Config{"mode": cfg.Mode}
	if cfg.ClusterID != 0 {
		c["clusterId"] = cfg.ClusterID
	}
	if cfg.Preset != "" {
		c["preset"] = cfg.Preset
	}
	if len(cfg.EntryNodes) > 0 {
		c["entryNodes"] = cfg.EntryNodes
	}
	return c
}

func startNode(cfg waku.Config) (*waku.Node, error) {
	node, err := waku.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("rendezvous plane: %w", err)
	}
	if err := node.Start(); err != nil {
		node.Close()
		return nil, fmt.Errorf("start rendezvous plane: %w", err)
	}
	// Nothing arrives until the node has peers, and a token that expires while
	// the node is still dialling wastes the human's fifteen minutes.
	time.Sleep(3 * time.Second)
	return node, nil
}

// hushStdout points fd 1 at /dev/null and returns a writer on the real one.
//
// The rendezvous library logs at INFO from its own threads, to **stdout** —
// checked, because the obvious assumption is stderr and it is wrong — and its
// logLevel setting is accepted and ignored: FATAL, ERROR and the default all
// produce the same 162 lines. That is right for the daemon, whose output the
// journal keeps, and wrong for a command someone is watching a prompt on, where
// a page of dialler output reads as a failure.
//
// Redirected at the descriptor level rather than by reassigning os.Stdout,
// because the writes come from C. The returned writer is the original
// descriptor, so this command's own messages still reach the terminal, and
// restore is idempotent so it can be called early and deferred as well.
func hushStdout() (io.Writer, func(), error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := int(os.Stdout.Fd())
	saved, err := syscall.Dup(fd)
	if err != nil {
		null.Close()
		return nil, nil, err
	}
	if err := syscall.Dup3(int(null.Fd()), fd, 0); err != nil {
		syscall.Close(saved)
		null.Close()
		return nil, nil, err
	}

	real := os.NewFile(uintptr(saved), "stdout")
	var once sync.Once
	return real, func() {
		once.Do(func() {
			syscall.Dup3(saved, fd, 0)
			syscall.Close(saved)
			null.Close()
		})
	}, nil
}

// groupToken breaks a token into fives, which is how people read digits aloud.
func groupToken(s string) string {
	var parts []string
	for i := 0; i < len(s); i += 5 {
		end := i + 5
		if end > len(s) {
			end = len(s)
		}
		parts = append(parts, s[i:end])
	}
	return strings.Join(parts, "-")
}

// perMeshRequest builds the second-round request: the keys this device will use
// on the mesh that just answered (ADR-015, ADR-017).
//
// Deriving here also creates the mesh's state entry, which is the same entry the
// daemon will load when it starts this mesh — so the credential that comes back
// names exactly the keys it will announce with. Getting that pair out of step is
// the failure where every peer refuses a device, correctly and silently.
func perMeshRequest(st *state.State, r *invite.Response, name string) (*invite.Request, error) {
	id, err := state.MeshIDFromInvite(r.MeshID, r.NetworkKey)
	if err != nil {
		return nil, err
	}
	ms, err := st.MeshState(id, false)
	if err != nil {
		return nil, err
	}
	return &invite.Request{
		DevicePub: ms.Identity.DevicePub,
		WGPub:     ms.Identity.WGPub[:],
		SealPub:   ms.Identity.SealPub[:],
		Name:      name,
	}, nil
}

// inviteBootAddr picks the bootstrap address to put in an invite.
//
// The freshest address this node would itself bootstrap from, which is the
// best evidence available that it answers. The daemon records its own
// published address alongside the ones it hears, so this works whether the
// inviter is the bootstrap node or merely knows one.
//
// Read from the state directory rather than asked of the daemon: the CLI
// cannot build a multiaddr for itself, because the peer id needs a running
// delivery node and this process does not have one.
//
// One, not a list: the device needs somewhere to start exactly once, and learns
// the rest from announces the moment it is on the mesh.
func inviteBootAddr(cfg state.Config, stateDir string) string {
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return ""
	}
	if peers := st.BootPeers(time.Now()); len(peers) > 0 {
		return peers[0]
	}
	return ""
}

// splitList turns a comma-separated flag into a slice, ignoring empties so that
// an unset flag is no addresses rather than one empty one.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
