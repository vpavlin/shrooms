package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// fetchStatus reads the daemon's status over its unix socket.
func fetchStatus(sock string) (statusPayload, error) {
	// A new CLI against a daemon that predates the rename: the old socket is
	// still there and still serving, so use it rather than reporting nothing.
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
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("http://unix/status")
	if err != nil {
		// The daemon needs CAP_NET_ADMIN, so it usually runs as root and the
		// socket is root:root 0660. "permission denied" then means the daemon
		// is fine and you are not root — a completely different problem from
		// the daemon being down, and worth saying so rather than making
		// everyone work it out from errno.
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
			return statusPayload{}, fmt.Errorf(
				"cannot read %s: permission denied.\nThe daemon runs as root; try: sudo %s",
				sock, strings.Join(os.Args, " "))
		}
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return statusPayload{}, fmt.Errorf(
				"no daemon listening on %s — is `shrooms daemon` running?", sock)
		}
		return statusPayload{}, fmt.Errorf("cannot reach the daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()

	var st statusPayload
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return statusPayload{}, fmt.Errorf("decode status: %w", err)
	}
	return st, nil
}

// shortDur renders an age compactly enough for a table column.
func shortDur(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%dh", sec/3600)
	}
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

	// A daemon that has not joined anything has no roster, no addresses and no
	// rendezvous plane to report. Saying so beats an empty table with a
	// stalled-discovery warning underneath it.
	if st.Waiting {
		fmt.Println("waiting for a mesh — this machine has not joined one yet")
		fmt.Println()
		fmt.Println("Get an invite from a machine that is already on one:")
		fmt.Println("  there $ shrooms invite")
		fmt.Println("  here  $ sudo shrooms join --invite <TOKEN>")
		return nil
	}

	online := 0
	for _, p := range st.Peers {
		if p.Online {
			online++
		}
	}

	// tabwriter, not padding: the prefix and name are variable width, so
	// hardcoded spaces line up for one mesh and not the next.
	head := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(head, "network\t%s\tpeers %d (%d up)\n", st.Prefix, len(st.Peers), online)
	fmt.Fprintf(head, "self\t%s  %s\t\n", st.Name, st.Overlay)
	// More than one mesh: show them before the roster, because "which network
	// is this peer on" is the first question a split raises.
	if len(st.Meshes) > 1 {
		for _, m := range st.Meshes {
			fmt.Fprintf(head, "mesh %s\t%s\t%s  %s  peers %d\n",
				m.Label, m.Overlay, m.Prefix, m.Iface, m.Peers)
		}
	}
	if st.OverlayV4 != "" {
		// Said plainly, because the second address is the one people ask about:
		// it exists so browsers can use mesh names on a network with no IPv6.
		fmt.Fprintf(head, "ipv4\t%s\tfor clients that ask only for A records\n", st.OverlayV4)
	}
	head.Flush()

	// The rendezvous plane is reported whenever it is unhealthy, and quietly
	// otherwise. When the fleet is unreachable, tunnels keep working and the
	// roster simply stops changing — which looks exactly like "nobody else is
	// online", and sends you debugging the wrong plane. This is the line that
	// distinguishes them.
	if !st.Rendezvous.OK {
		fmt.Printf("\n!! rendezvous: %s", st.Rendezvous.Problem)
		if st.Rendezvous.Detail != "" {
			fmt.Printf(" (%s)", st.Rendezvous.Detail)
		}
		fmt.Println()
		fmt.Println("   Peer discovery is stalled; established tunnels are unaffected.")
	}
	fmt.Println()

	if len(st.Peers) == 0 {
		if st.Rendezvous.OK {
			fmt.Println("no peers seen yet")
		} else {
			fmt.Println("no peers seen yet — expected while rendezvous is down, see above")
		}
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tOVERLAY IP\tANNOUNCE\tTUNNEL\tENDPOINT\tRX/TX\tCONNECTED IN")
	for _, p := range st.Peers {
		ann := "offline"
		if p.Online {
			ann = "online"
		}
		// "up" only while the session is actually usable. A handshake that has
		// gone stale means the peer is gone — reporting that as up is worse
		// than reporting nothing, because status is what you check precisely
		// when you suspect something is wrong.
		// The handshake age is always shown, never just "up".
		//
		// A peer that restarts leaves the other side holding a session that
		// stays valid for REJECT_AFTER_TIME, so "up" can be true of a peer that
		// has already gone. The age is what distinguishes a tunnel rekeying
		// every ~165s from one frozen since the peer vanished, and hiding it
		// meant status asserted a connection that was not there.
		tun := "no handshake"
		switch {
		case p.Live:
			tun = fmt.Sprintf("up %s", shortDur(p.HandshakeAgeS))
		case p.Handshaked:
			tun = fmt.Sprintf("stale %s", shortDur(p.HandshakeAgeS))
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
		// How long this peer took to become usable, since the daemon started.
		// Blank rather than "0s" when it never has: an absent measurement and a
		// fast one must not look the same.
		took := "-"
		if p.TunnelAfterS > 0 {
			took = shortDur(int64(p.TunnelAfterS))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\n",
			name, p.Overlay, ann, tun, ep, human(p.RxBytes), human(p.TxBytes), took)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	printServices(os.Stdout, st.Services, st.NameRouter)
	return nil
}

// printServices lists what this device publishes, and only this device: nothing
// is announced, so no node knows what any other one runs. The names printed are
// the ones to type on another machine, which is the only reason to print them.
func printServices(out io.Writer, svcs []serviceStatus, router []routerStatus) {
	if len(svcs) == 0 {
		return
	}
	fmt.Fprintf(out, "\nservices published here\n")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tREACHABLE AS\tFORWARDS TO\tCONNS")
	// The bare name only works while the router holds port 80, so what is
	// printed as "reachable as" has to depend on whether it does. Printing a
	// URL that does not work is worse than printing a longer one that does.
	bare := false
	for _, r := range router {
		if r.Port == 80 && (r.Listening || r.Direct) {
			bare = true
		}
	}
	for _, s := range svcs {
		addr := fmt.Sprintf("%s.%s:%d", s.Name, s.DNSName, s.Port)
		if bare {
			addr = fmt.Sprintf("http://%s.%s", s.Name, s.DNSName)
		}
		state := s.Target
		switch {
		case s.Err != "":
			// The service is declared and not working. Say why on the row
			// rather than in a log the user is not reading.
			state = "unavailable: " + s.Err
		case s.Direct:
			// Something else holds the port on the overlay address, which
			// means the application binds it itself and is already reachable.
			state = "the application itself"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", s.Name, addr, state, s.Conns)
	}
	w.Flush()

	// Only when it is broken. Working infrastructure does not need a line, but
	// "why do I still need the port?" needs an answer in the place the question
	// is asked.
	for _, r := range router {
		if r.Err != "" {
			fmt.Fprintf(out, "  note: port %d is not held (%s), so names need their port\n", r.Port, r.Err)
		}
	}
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
