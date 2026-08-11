package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
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
	cfgPath, _ := commonFlags(fs)
	dir := fs.String("admin-dir", defaultAdminDir(), "where the admin key is kept")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	name := fs.String("name", "", "the joining device's name (it may choose its own)")
	ttl := fs.Duration("ttl", invite.DefaultTTL, "how long the invite stays open")
	life := fs.Duration("life", cred.DefaultLife, "how long the issued credential is valid")
	serial := fs.Uint64("serial", 0, "credential serial; must increase per device (default: now)")
	asQR := fs.Bool("qr", true, "show the token as a QR code")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}

	// Checked before the passphrase prompt. Typing a passphrase and then being
	// told the daemon is down is the wrong order to learn it in.
	client := socketClient(*sock, *ttl+time.Minute)
	if _, err := fetchStatus(*sock); err != nil {
		return fmt.Errorf("%w\n\nThe daemon holds the invite open, since it is the node already "+
			"connected to the fleet", err)
	}

	// The admin key only if this mesh has an authority. A mesh minted with
	// --no-admin has none, and an invite there is still worth having: it moves
	// the network key without putting it on a screen.
	var admin *cred.Admin
	var auth *cred.Authority
	if len(cfg.AdminKeys) > 0 {
		admin, auth, err = loadAdmin(*dir)
		if err != nil {
			return fmt.Errorf("%w\n\nThe invite has to issue a credential, which needs the admin key. "+
				"Run this on the machine that holds it.", err)
		}
		if cfgAuth, err := cfg.Authority(); err == nil && cfgAuth != nil && cfgAuth.ID() != auth.ID() {
			return fmt.Errorf("this admin key belongs to mesh %s, but the config is for %s",
				auth.ID(), cfgAuth.ID())
		}
	}

	secret, err := invite.New()
	if err != nil {
		return err
	}

	fmt.Printf("Invite valid for %s. On the joining device:\n\n", *ttl)
	fmt.Printf("  shrooms join --invite %s\n\n", groupToken(secret.String()))
	if *asQR {
		art, err := renderQR(secret.URI())
		if err != nil {
			fmt.Fprintf(os.Stderr, "(no QR: %v)\n", err)
		} else {
			fmt.Print(art, "\n")
		}
	}
	fmt.Printf("Waiting. An invite is answered only by the node that issued it,\n")
	fmt.Printf("which is what makes it single-use — so leave this running.\n\n")

	req, err := holdInvite(client, secret, *ttl)
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
		if credential, err = issueFor(admin, auth, devPub, wgPub, joiner, *serial, time.Now(), *life); err != nil {
			return err
		}
		if issued, err = cred.UnmarshalCredential(credential); err != nil {
			return err
		}
	}

	if err := replyInvite(client, secret, req.EphPub, joiner, credential); err != nil {
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
	Name      string `json:"name"`
	EphPub    string `json:"eph_pub"`
}

// holdInvite asks the daemon to listen on the invite topic and blocks until
// somebody redeems the token. Returns nil when the invite expires unused.
func holdInvite(client *http.Client, s invite.Secret, ttl time.Duration) (*inviteRequest, error) {
	body, _ := json.Marshal(map[string]any{"token": s.String(), "ttl_s": int(ttl.Seconds())})
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
	body, _ := json.Marshal(map[string]any{
		"token":      s.String(),
		"eph_pub":    ephPub,
		"name":       name,
		"credential": base64.StdEncoding.EncodeToString(credential),
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
	local := fs.Bool("local", false, "do not use a running daemon even if there is one")
	verbose := fs.Bool("v", false, "show the rendezvous library's own logging")
	if err := fs.Parse(args); err != nil {
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
		if st, err := fetchStatus(*sock); err == nil {
			if !st.Waiting {
				return errors.New("the daemon on this machine already has a mesh; " +
					"stop it and remove its config to join another one")
			}
			return joinViaDaemon(*sock, token, deviceName, uint16(*port), *advertise, *relay, *timeout)
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

	// No config yet, so the defaults: preset, cluster and mode as shipped. The
	// invite says nothing about which fleet to use because a device that could
	// be told that could be told to use somebody else's.
	fmt.Fprintln(out, "Connecting to the fleet...")
	node, err := startNode(nodeConfig(state.DefaultConfig()))
	if err != nil {
		return err
	}
	defer node.Close()

	secret, err := invite.ParseToken(token)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Asking to join as %q...\n", deviceName)

	resp, err := invite.Redeem(ctx, rendezvous.InviteTransport(node), secret, &invite.Request{
		DevicePub: st.Identity.DevicePub,
		WGPub:     st.Identity.WGPub[:],
		Name:      deviceName,
	})
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
	if err := setup(*cfgPath, *stateDir, nk, deviceName, uint16(*port), *advertise, *relay, false); err != nil {
		return err
	}

	if len(resp.AdminKeys) > 0 {
		encoded := make([]string, 0, len(resp.AdminKeys))
		for _, k := range resp.AdminKeys {
			if len(k) != ed25519.PublicKeySize {
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
		if err := st.SetCredential(resp.Credential); err != nil {
			return err
		}
		fmt.Printf("\nEnrolled. Credential serial %d, expires %s.\n",
			c.Serial, time.Unix(c.NotAfter, 0).Format(time.RFC3339))
	}
	return nil
}

// joinViaDaemon hands the token to a daemon that is waiting for a mesh.
func joinViaDaemon(sock, token, name string, port uint16, advertise string, relay bool, timeout time.Duration) error {
	fmt.Printf("Asking to join as %q, via the daemon...\n", name)

	body, _ := json.Marshal(map[string]any{
		"token": token, "name": name, "port": port,
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
