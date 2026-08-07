package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

// fetchStatus reads the daemon's status over its unix socket.
func fetchStatus(sock string) (statusPayload, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("http://unix/status")
	if err != nil {
		return statusPayload{}, fmt.Errorf("cannot reach the daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()

	var st statusPayload
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return statusPayload{}, fmt.Errorf("decode status: %w", err)
	}
	return st, nil
}

func human(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d", n)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}

	online := 0
	for _, p := range st.Peers {
		if p.Online {
			online++
		}
	}

	fmt.Printf("network  %s          peers %d (%d up)\n", st.Prefix, len(st.Peers), online)
	fmt.Printf("self     %s  %s\n\n", st.Name, st.Overlay)

	if len(st.Peers) == 0 {
		fmt.Println("no peers seen yet")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tOVERLAY IP\tANNOUNCE\tTUNNEL\tENDPOINT\tRX/TX")
	for _, p := range st.Peers {
		ann := "offline"
		if p.Online {
			ann = "online"
		}
		tun := "no handshake"
		if p.Handshaked {
			tun = "up"
		}
		ep := p.Endpoint
		if ep == "" && len(p.Endpoints) > 0 {
			ep = p.Endpoints[0] + " (announced)"
		}
		if ep == "" {
			ep = "-"
		}
		// Marked on the name rather than given a column: it is rarely true and
		// a mostly-empty column costs width on every row.
		name := p.Name
		if p.Relay {
			name += " (relay)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s/%s\n",
			name, p.Overlay, ann, tun, ep, human(p.RxBytes), human(p.TxBytes))
	}
	return w.Flush()
}

// cmdPaths shows candidate-level detail: which endpoints answered a probe,
// their RTT, and which one is in use. This is what tells you whether a peer is
// direct or falling back, and why.
func cmdPaths(args []string) error {
	fs := flag.NewFlagSet("paths", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	want := ""
	if fs.NArg() > 0 {
		want = fs.Arg(0)
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	if len(st.Reflexive) > 0 {
		fmt.Printf("reflexive addresses (as peers observe us):\n")
		for _, r := range st.Reflexive {
			fmt.Printf("  %s\n", r)
		}
		if len(st.Reflexive) > 1 {
			fmt.Printf("  note: %d distinct addresses suggests endpoint-dependent NAT,\n", len(st.Reflexive))
			fmt.Printf("        where hole punching fails and a relay is needed.\n")
		}
		fmt.Println()
	}

	shown := 0
	for _, p := range st.Peers {
		if want != "" && p.Name != want {
			continue
		}
		shown++
		fmt.Printf("%s  %s\n", p.Name, p.Overlay)
		if len(p.Paths) == 0 {
			fmt.Printf("  no candidate has answered a probe yet\n")
			if len(p.Endpoints) > 0 {
				fmt.Printf("  announced: %v\n", p.Endpoints)
			}
			fmt.Println()
			continue
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  CANDIDATE\tRTT\tLAST PONG\t")
		for _, path := range p.Paths {
			mark := ""
			if path.Selected {
				mark = "  <- in use"
			}
			fmt.Fprintf(w, "  %s\t%dms\t%s\t%s\n", path.Addr, path.RTTMs, path.LastPong, mark)
		}
		w.Flush()
		fmt.Println()
	}

	if shown == 0 {
		if want != "" {
			return fmt.Errorf("no peer named %q", want)
		}
		fmt.Println("no peers known yet")
	}
	return nil
}
