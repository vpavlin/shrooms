package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/invite"
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
	name := fs.String("name", "", "the joining device's name (it may choose its own)")
	ttl := fs.Duration("ttl", invite.DefaultTTL, "how long the invite stays open")
	life := fs.Duration("life", cred.DefaultLife, "how long the issued credential is valid")
	serial := fs.Uint64("serial", 0, "credential serial; must increase per device (default: now)")
	asQR := fs.Bool("qr", true, "show the token as a QR code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = stateDir

	cfg, err := state.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	nk, err := cfg.Key()
	if err != nil {
		return err
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
		art, err := renderQR(inviteURI(secret))
		if err != nil {
			fmt.Fprintf(os.Stderr, "(no QR: %v)\n", err)
		} else {
			fmt.Print(art, "\n")
		}
	}
	fmt.Printf("Waiting. Keep this running — an invite is answered only by the\n")
	fmt.Printf("machine that issued it, which is what makes it single-use.\n\n")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTTL := context.WithTimeout(ctx, *ttl)
	defer cancelTTL()

	node, err := startNode(nodeConfig(cfg))
	if err != nil {
		return err
	}
	defer node.Close()

	topicName := secret.Topic()
	if err := node.Subscribe(topicName); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("the invite expired before anyone used it")
			}
			return nil
		case ev, ok := <-node.Events():
			if !ok {
				return errors.New("the rendezvous node stopped")
			}
			msg, _, ok := waku.ParseMessage(ev.JSON)
			if !ok || msg.ContentTopic != topicName {
				continue
			}
			req, err := invite.OpenRequest(secret, msg.Payload, time.Now())
			if err != nil {
				// Our own published response comes back to us, and other
				// applications share the shard. Neither is worth a word.
				continue
			}

			joiner := req.Name
			if *name != "" {
				joiner = *name
			}
			if joiner == "" {
				joiner = "device"
			}
			fmt.Printf("Request from %q (%x).\n", joiner, req.DevicePub[:8])

			resp := &invite.Response{
				NetworkKey: nk[:],
				Suffix:     cfg.HostsSuffix,
				Timestamp:  time.Now().Unix(),
			}
			for _, k := range cfg.AdminKeys {
				raw, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(k)))
				if err != nil {
					return fmt.Errorf("admin_keys in the config are unreadable: %w", err)
				}
				resp.AdminKeys = append(resp.AdminKeys, raw)
			}
			var issued *cred.Credential
			if admin != nil {
				raw, err := issueFor(admin, auth, req.DevicePub, req.WGPub, joiner, *serial, time.Now(), *life)
				if err != nil {
					return err
				}
				if issued, err = cred.UnmarshalCredential(raw); err != nil {
					return err
				}
				resp.Credential = raw
			}

			blob, err := invite.SealResponse(secret, req.EphPub, resp)
			if err != nil {
				return err
			}
			if _, err := node.Send(topicName, blob, true); err != nil {
				return fmt.Errorf("send: %w", err)
			}

			fmt.Printf("Admitted %q.\n", joiner)
			if issued != nil {
				fmt.Printf("  credential serial %d, valid %s\n", issued.Serial, *life)
			} else {
				fmt.Printf("  no credential: this mesh has no admin keys\n")
			}
			// One invite, one device. Anything else that arrives on this topic
			// is either a retransmission or somebody else holding the token,
			// and neither should get a second answer.
			//
			// The response is sent once and Waku may lose it. That failure is
			// visible and cheap — the joining device says so and you run
			// `shrooms invite` again — where answering repeatedly would quietly
			// turn a single-use token into a reusable one.
			return nil
		}
	}
}

// cmdJoinInvite is `join --invite`: the joining half.
func cmdJoinInvite(token string, args []string) error {
	fs := flag.NewFlagSet("join --invite", flag.ExitOnError)
	cfgPath, stateDir := commonFlags(fs)
	name := fs.String("name", "", "device name (default: hostname)")
	port := fs.Uint("port", 51820, "UDP listen port")
	advertise := fs.String("advertise", "", "public endpoint, only if it is not on a local interface")
	relay := fs.Bool("relay", false, "forward traffic for peers that cannot reach each other")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to wait for the inviter to answer")
	if err := fs.Parse(args); err != nil {
		return err
	}

	secret, err := parseInviteToken(token)
	if err != nil {
		return err
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
	deviceName := *name
	if deviceName == "" {
		deviceName = state.DefaultConfig().Name
	}

	ephPriv, ephPub, err := invite.NewEphemeral()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, *timeout)
	defer cancelTimeout()

	// No config yet, so the defaults: preset, cluster and mode as shipped. The
	// invite says nothing about which fleet to use because a device that could
	// be told that could be told to use somebody else's.
	node, err := startNode(nodeConfig(state.DefaultConfig()))
	if err != nil {
		return err
	}
	defer node.Close()

	topicName := secret.Topic()
	if err := node.Subscribe(topicName); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	req := &invite.Request{
		DevicePub: st.Identity.DevicePub,
		WGPub:     st.Identity.WGPub[:],
		Name:      deviceName,
		EphPub:    ephPub,
		Timestamp: time.Now().Unix(),
	}
	blob, err := invite.SealRequest(secret, req)
	if err != nil {
		return err
	}

	fmt.Printf("Asking to join as %q...\n", deviceName)

	// Retried, because the joining device is usually the one on the worse
	// network and the first publish often lands before the subscription has
	// propagated. The inviter answers at most once regardless.
	send := func() error {
		_, err := node.Send(topicName, blob, true)
		return err
	}
	if err := send(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()

	var resp *invite.Response
	for resp == nil {
		select {
		case <-ctx.Done():
			return errors.New("no answer. Is `shrooms invite` still running on the other machine?")
		case <-retry.C:
			if err := send(); err != nil {
				return fmt.Errorf("send: %w", err)
			}
		case ev, ok := <-node.Events():
			if !ok {
				return errors.New("the rendezvous node stopped")
			}
			msg, _, ok := waku.ParseMessage(ev.JSON)
			if !ok || msg.ContentTopic != topicName {
				continue
			}
			r, err := invite.OpenResponse(secret, ephPriv, msg.Payload, time.Now())
			if err != nil {
				// Our own request comes back to us, as does anyone else's.
				continue
			}
			resp = r
		}
	}

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
				return fmt.Errorf("the invite carried a malformed admin key")
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

// inviteURI is what the QR code carries: a URI rather than a bare token, so the
// phone can tell an invite from any other text it might scan, and from the
// network-key invites that `key show --qr` still produces.
func inviteURI(s invite.Secret) string {
	return state.InviteScheme + "://enrol?token=" + s.String()
}

// parseInviteToken accepts the URI, the grouped form, or the bare token.
func parseInviteToken(text string) (invite.Secret, error) {
	text = strings.TrimSpace(text)
	if u, err := url.Parse(text); err == nil && u.Scheme == state.InviteScheme {
		if tok := u.Query().Get("token"); tok != "" {
			return invite.Parse(tok)
		}
	}
	return invite.Parse(text)
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
