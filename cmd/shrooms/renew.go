package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
)

// Renewal (ADR-018).
//
// A credential lasts thirty days, which is what makes losing a device
// survivable without every node having to hear a revocation: an unrenewed
// device falls off the mesh on its own. The cost of that guarantee is that
// somebody has to renew, and a system where the answer to "what happens on day
// thirty" is "everything stops and nobody knows why" is worse than one with no
// expiry at all.
//
// So renewal is a sweep, not a per-device ceremony: the admin asks a running
// node who is on the mesh, signs a fresh credential for each, and hands them
// back to that node to deliver. One command, run occasionally, from the machine
// that already holds the admin key.

// renewWindow is how close to expiry a credential has to be before a sweep
// reissues it. cred.RenewBefore of the default life — ten days — so a sweep
// run once a week always has two chances before anything lapses.
var renewWindow = cred.RenewBefore(cred.DefaultLife)

type memberJSON struct {
	DevicePub string `json:"device_pub"`
	WGPub     string `json:"wg_pub"`
	Name      string `json:"name"`
	NotAfter  int64  `json:"not_after,omitempty"`
}

func cmdAdminRenew(args []string) error {
	fs := flag.NewFlagSet("admin renew", flag.ExitOnError)
	dir := fs.String("dir", defaultAdminDir(), "where the admin key is kept")
	signWith := fs.String("sign-with", "", "a command that signs a digest, instead of the admin key file")
	external := fs.Bool("external-signer", false, "print the digest and read the signature back (ADR-022)")
	label := fs.String("mesh", "", "which mesh, when this node is on several")
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	within := fs.Duration("within", renewWindow, "renew credentials expiring within this")
	all := fs.Bool("all", false, "renew every member, however long they have left")
	life := fs.Duration("life", cred.DefaultLife, "how long the new credentials last")
	dry := fs.Bool("dry-run", false, "say what would be renewed and stop")
	if err := fs.Parse(args); err != nil {
		return err
	}

	admin, auth, err := signerFor(*dir, *label, *signWith, *external)
	if err != nil {
		return err
	}

	members, err := fetchMembers(*sock, *label)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return errors.New("the daemon knows of no members, which cannot include itself — is it running?")
	}

	now := time.Now()
	var renewed, skipped int
	for _, mem := range members {
		left := time.Unix(mem.NotAfter, 0).Sub(now)
		switch {
		case mem.NotAfter == 0:
			// Never seen a credential from them: either they hold none, or
			// they have not announced since this node started. Renewing is
			// harmless — a device keeps whichever credential lasts longer —
			// and not renewing is how a silent device quietly lapses.
			fmt.Printf("  %-16s unknown expiry\n", nameOf(mem))
		case !*all && left > *within:
			fmt.Printf("  %-16s %s left\n", nameOf(mem), left.Round(time.Hour))
			skipped++
			continue
		default:
			fmt.Printf("  %-16s %s left\n", nameOf(mem), left.Round(time.Hour))
		}
		if *dry {
			renewed++
			continue
		}

		devPub, err := parseKey(mem.DevicePub, 32)
		if err != nil {
			fmt.Printf("  %-16s skipped: %v\n", nameOf(mem), err)
			continue
		}
		wgPub, err := parseKey(mem.WGPub, 32)
		if err != nil {
			fmt.Printf("  %-16s skipped: %v\n", nameOf(mem), err)
			continue
		}
		raw, err := cred.IssueFor(admin, auth, devPub, wgPub, mem.Name, 0, now, *life)
		if err != nil {
			return fmt.Errorf("%s: %w", nameOf(mem), err)
		}
		if err := publishGrant(*sock, *label, raw); err != nil {
			// Print what could not be delivered rather than dropping it: a
			// credential is public, and handing it over by any other means
			// works exactly as well.
			fmt.Printf("  %-16s not delivered: %v\n", nameOf(mem), err)
			fmt.Printf("      %s\n", base64.StdEncoding.EncodeToString(raw))
			continue
		}
		renewed++
	}

	fmt.Println()
	switch {
	case *dry:
		fmt.Printf("%d would be renewed, %d left alone.\n", renewed, skipped)
	default:
		fmt.Printf("Renewed %d, left %d alone.\n\n", renewed, skipped)
		fmt.Println("Each one travels to the device it names, verified there against")
		fmt.Println("the same admin keys that admitted it. A device that is offline")
		fmt.Println("now picks its own up from the next sweep, which is why renewal")
		fmt.Printf("starts %s before expiry rather than on the day.\n", renewWindow)
	}
	return nil
}

func nameOf(m memberJSON) string {
	if m.Name != "" {
		return m.Name
	}
	if len(m.DevicePub) >= 16 {
		return m.DevicePub[:16]
	}
	return "(unnamed)"
}

// fetchMembers asks the local daemon who is on the mesh.
func fetchMembers(sock, label string) ([]memberJSON, error) {
	q := ""
	if label != "" {
		q = "?mesh=" + url.QueryEscape(label)
	}
	resp, err := controlClient(sock).Get("http://unix/members" + q)
	if err != nil {
		return nil, fmt.Errorf("no daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("the daemon refused: %s", strings.TrimSpace(string(msg)))
	}
	var out []memberJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// publishGrant hands one renewed credential to the local daemon.
func publishGrant(sock, label string, raw []byte) error {
	q := ""
	if label != "" {
		q = "?mesh=" + url.QueryEscape(label)
	}
	body := strings.NewReader(base64.StdEncoding.EncodeToString(raw))
	resp, err := controlClient(sock).Post("http://unix/grant"+q, "text/plain", body)
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("refused: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

// controlClient dials the daemon's unix socket, falling back to the older path
// so an upgraded CLI still finds a daemon that has not been restarted.
func controlClient(sock string) *http.Client {
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
		Timeout: 10 * time.Second,
	}
}
