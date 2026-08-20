package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
)

// fetchStatus reads the daemon's status over its unix socket.
func fetchStatus(sock string) (statusPayload, error) {
	// A new CLI against a daemon that predates the rename: the old socket is
	// still there and still serving, so use it rather than reporting nothing.
	//
	// Only when the current one is genuinely absent. "Permission denied" means
	// it is right there and this user may not use it, and falling back then
	// produced an error naming a stale path from a previous install — which
	// sends you looking at the wrong socket, the wrong daemon and the wrong
	// day's files.
	if sock == DefaultSocket {
		if _, err := os.Stat(sock); errors.Is(err, fs.ErrNotExist) {
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
			// Both ways out, because sudo is the answer once and the setting
			// is the answer every time after.
			return statusPayload{}, fmt.Errorf(
				"cannot read %s: permission denied.\n"+
					"The daemon runs as root; try: sudo %s\n"+
					"To stop needing that, set socket_group in the config to a group you are in.",
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
			fmt.Fprintf(head, "mesh %s\t%s\t%s  %s  peers %d%s%s%s\n",
				m.Label, m.Overlay, m.Prefix, m.Iface, m.Peers,
				relayNote(m), expiryNote(m.Expires), enrolNote(m))
		}
	} else if len(st.Meshes) == 1 {
		if relayNote(st.Meshes[0]) != "" {
			fmt.Fprintf(head, "relay\tnone\tpeers on mobile data may reach only public addresses\n")
		}
		if st.Meshes[0].Unenrolled {
			fmt.Fprintf(head, "member\t!! no credential\tpeers will refuse this device — %s\n", enrolFix(""))
		}
		if note := expiryNote(st.Meshes[0].Expires); note != "" {
			fmt.Fprintf(head, "member\t%s\tuntil %s\n",
				strings.TrimSpace(note),
				time.Unix(st.Meshes[0].Expires, 0).Format("2006-01-02"))
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

	// The mesh column appears only when there is more than one, so a node with
	// a single mesh — which is most of them — sees exactly the table it always
	// did rather than a column of the same word.
	multi := len(st.Meshes) > 1

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if multi {
		fmt.Fprintln(w, "MESH\tNAME\tOVERLAY IP\tANNOUNCE\tTUNNEL\tENDPOINT\tRX/TX\tCONNECTED IN")
	} else {
		fmt.Fprintln(w, "NAME\tOVERLAY IP\tANNOUNCE\tTUNNEL\tENDPOINT\tRX/TX\tCONNECTED IN")
	}
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
		if multi {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\n",
				p.Mesh, name, p.Overlay, ann, tun, ep, human(p.RxBytes), human(p.TxBytes), took)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\n",
				name, p.Overlay, ann, tun, ep, human(p.RxBytes), human(p.TxBytes), took)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	printServices(os.Stdout, st.Services, st.NameRouter)
	printPeerServices(os.Stdout, st.Peers)
	return nil
}

// printPeerServices lists what other devices say they offer (ADR-023).
//
// Separate from the section above, and worded differently, because they are
// different kinds of statement. What this device publishes is checked — the
// forwarder knows whether the port answers. What a peer publishes is a claim
// repeated every few minutes, so these are names worth trying rather than
// services known to be up.
func printPeerServices(out io.Writer, peers []peerStatus) {
	any := false
	for _, p := range peers {
		if len(p.Services) > 0 || len(p.Bound) > 0 {
			any = true
			break
		}
	}
	if !any {
		return
	}
	fmt.Fprintf(out, "\nservices offered by peers\n")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MESH\tNAME\tTRY")
	for _, p := range peers {
		// A list outlives reachability on purpose — it is a claim about what a
		// device offers, and a device that is asleep still offers it — but
		// printing it the same way as a reachable one invites somebody to try
		// an address that cannot answer.
		note := ""
		if !p.Live {
			note = "  (unreachable now)"
		}
		// Ports bound to the peer's own mesh address first, because they need
		// no forwarder and no name router — they are simply there.
		for _, b := range p.Bound {
			name, port, ok := strings.Cut(b, ":")
			if !ok {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s:%s%s\n", p.Mesh, name, hostOf(p), port, note)
		}
		for _, svc := range p.Services {
			fmt.Fprintf(w, "%s\t%s\thttp://%s.%s%s\n", p.Mesh, svc, svc, hostOf(p), note)
		}
	}
	w.Flush()
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

	// First, because it decides whether anybody can reach this node at all and
	// is visible nowhere else. Skipped entirely when the daemon predates the
	// field, rather than reported as "nothing" — that would be a false
	// diagnosis pointing at the opposite of the real problem.
	// A relay's own account of itself, on the node that is one.
	if r := st.Relay; r != nil {
		fmt.Printf("this node is a relay:\n")
		fmt.Printf("  %d device(s) registered, %d forwarded\n", r.Peers, r.Forwarded)
		if r.Refused > 0 {
			// The number that answers "why is the relay not carrying my
			// traffic": a device it will not accept a registration from.
			fmt.Printf("  %d registration(s) refused — stale, or a device whose\n", r.Refused)
			fmt.Printf("  announce this relay has not seen, so it cannot confirm\n")
			fmt.Printf("  the tunnel key belongs to it\n")
		}
		if r.Dropped > 0 {
			fmt.Printf("  %d frame(s) dropped as unreadable\n", r.Dropped)
		}
		fmt.Println()
	}

	if st.Announced != nil {
		fmt.Printf("we announce (where peers are told to reach us):\n")
		if len(*st.Announced) == 0 {
			fmt.Printf("  nothing — no peer can dial this node\n")
			fmt.Printf("  it becomes reachable once some peer reaches it first and reports\n")
			fmt.Printf("  the address back, which needs a relay or a peer with a public one\n")
		}
		for _, a := range *st.Announced {
			fmt.Printf("  %s\n", a)
		}
		fmt.Println()
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

// expiryNote says how long this device's membership has left, and only when
// that is worth saying.
//
// Silent while there is plenty of time, because a countdown nobody has to act
// on is noise, and noise is what makes the line invisible on the day it
// matters. A credential lapsing takes the device off the mesh on a known date
// — the one scheduled failure in the system — so the warning starts as soon as
// a renewal sweep would act (cred.RenewBefore) and gets shorter and louder.
func expiryNote(unix int64) string {
	if unix == 0 {
		return ""
	}
	left := time.Until(time.Unix(unix, 0))
	switch {
	case left <= 0:
		return "  !! membership expired — shrooms admin renew"
	case left < 24*time.Hour:
		return fmt.Sprintf("  !! membership ends in %s", left.Round(time.Hour))
	case left < cred.RenewBefore(cred.DefaultLife):
		return fmt.Sprintf("  membership ends in %d days", int(left.Hours()/24))
	}
	return ""
}

// enrolNote says when this device holds no credential for a mesh that admits by
// one.
//
// Loud, and not silenced by anything, because it is the only failure here that
// is invisible from the machine it is on. The node runs, its peers appear in
// its roster, and every one of them refuses its announces — so the symptom
// shows up on other people's machines as a device that never arrives. Without
// this line, the status page of a node nobody will admit reads as healthy.
func enrolNote(m meshStatus) string {
	if !m.Unenrolled {
		return ""
	}
	return "  !! no credential — " + enrolFix(m.Label)
}

// enrolFix is the command that ends it, aimed at the mesh in question.
func enrolFix(label string) string {
	if label == "" || label == "default" {
		return "shrooms admin issue"
	}
	return "shrooms admin issue --mesh " + label
}

// relayNote says when a mesh has nowhere to relay through.
//
// Zero relays is a configuration, not a fault, and it is invisible until
// somebody is on mobile data: a phone behind carrier NAT can open a path to a
// publicly-addressable peer and to nothing else, so the mesh appears to work
// from the sofa and to be broken from the street. Relaying is per mesh
// (ADR-015), so a second mesh joined by invite has none until it is told to —
// which is exactly the case that looked like a regression.
func relayNote(m meshStatus) string {
	if m.Relays > 0 || m.Peers == 0 {
		return ""
	}
	return "  no relay"
}

// hostOf is the name to print for a peer. The daemon already worked out its DNS
// name; re-deriving it here is how a front-end drifts from what resolves.
func hostOf(p peerStatus) string {
	if p.DNSName != "" {
		return p.DNSName
	}
	return p.Name
}
