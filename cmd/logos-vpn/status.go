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

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", *sock)
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("http://unix/status")
	if err != nil {
		return fmt.Errorf("cannot reach the daemon on %s: %w", *sock, err)
	}
	defer resp.Body.Close()

	var st statusPayload
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return fmt.Errorf("decode status: %w", err)
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
	fmt.Fprintln(w, "NAME\tOVERLAY IP\tSTATE\tSEQ\tENDPOINT")
	for _, p := range st.Peers {
		state := "offline"
		if p.Online {
			state = "online"
		}
		ep := "-"
		if len(p.Endpoints) > 0 {
			ep = p.Endpoints[0]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", p.Name, p.Overlay, state, p.Seq, ep)
	}
	return w.Flush()
}
