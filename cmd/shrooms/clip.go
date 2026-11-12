package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/vpavlin/shrooms/internal/clip"
)

// clipDirectory is who is where, from the status snapshot: the one place the
// daemon says so, read by the CLI and the apps as well.
func clipDirectory(snapshot func() statusPayload) clip.Directory {
	return func() ([]clip.Mesh, []clip.Peer) {
		st := snapshot()
		var meshes []clip.Mesh
		for _, m := range st.Meshes {
			a, err := netip.ParseAddr(m.Overlay)
			if err != nil {
				continue
			}
			meshes = append(meshes, clip.Mesh{Label: m.Label, Self: st.Name, Addr: a})
		}
		var peers []clip.Peer
		for _, p := range st.Peers {
			a, err := netip.ParseAddr(p.Overlay)
			if err != nil {
				continue
			}
			name := p.Name
			// Two devices claiming one name: each goes by the name its DNS
			// answers to, so a clip for one never lands on the other.
			if p.Contested && p.DNSName != "" {
				name, _, _ = strings.Cut(p.DNSName, ".")
			}
			peers = append(peers, clip.Peer{Mesh: p.Mesh, Name: name, Addr: a, Online: p.Online, Signed: p.NameSigned})
		}
		return meshes, peers
	}
}

// clipServer takes clips on each mesh address, as the metrics server serves
// on them: meshes come and go with reloads, and an address only exists once
// its interface is up.
type clipServer struct {
	store    *clip.Store
	snapshot func() statusPayload
	log      *slog.Logger

	mu sync.Mutex
	ls map[string]net.Listener
}

func newClipServer(stateDir string, snapshot func() statusPayload, log *slog.Logger) *clipServer {
	path := ""
	if stateDir != "" {
		path = filepath.Join(stateDir, "clips.json")
	}
	s := &clipServer{snapshot: snapshot, log: log, ls: map[string]net.Listener{}}
	s.store = clip.New(path, clipDirectory(snapshot), log)
	return s
}

func (c *clipServer) run(ctx context.Context) {
	go c.store.Run(ctx, 15*time.Second)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	online := map[string]bool{}
	for {
		c.listen()
		// A recipient coming online is when what waits for it can go.
		now := map[string]bool{}
		for _, p := range c.snapshot().Peers {
			if p.Online {
				now[p.Mesh+"/"+p.Name] = true
				if !online[p.Mesh+"/"+p.Name] {
					c.store.Online(p.Mesh, p.Name)
				}
			}
		}
		online = now
		select {
		case <-ctx.Done():
			c.mu.Lock()
			for _, l := range c.ls {
				l.Close()
			}
			c.mu.Unlock()
			return
		case <-t.C:
		}
	}
}

func (c *clipServer) listen() {
	want := map[string]string{}
	for _, ms := range c.snapshot().Meshes {
		if ms.Overlay != "" {
			want[net.JoinHostPort(ms.Overlay, strconv.Itoa(clip.Port))] = ms.Label
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for a, l := range c.ls {
		if _, ok := want[a]; !ok {
			l.Close()
			delete(c.ls, a)
		}
	}
	for a, label := range want {
		if _, ok := c.ls[a]; ok {
			continue
		}
		l, err := net.Listen("tcp", a)
		if err != nil {
			continue
		}
		c.ls[a] = l
		c.log.Info("clips taken", "address", a)
		go (&http.Server{Handler: c.store.Handler(label), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}).Serve(l)
	}
}

// clipDevice is a device a clip can go to, for the apps' pickers.
type clipDevice struct {
	Mesh   string `json:"mesh"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

type clipList struct {
	Received []clip.Clip  `json:"received"`
	Sent     []clip.Clip  `json:"sent"`
	Devices  []clipDevice `json:"devices"`
	Meshes   []string     `json:"meshes"`
}

// clipHandlers are the socket's: in the group's tier, like the settings. A
// clip is for the person at this machine, which is who the group is.
func clipHandlers(mux *http.ServeMux, c *clipServer) {
	dir := clipDirectory(c.snapshot)
	mux.HandleFunc("GET /clips", func(w http.ResponseWriter, r *http.Request) {
		var out clipList
		out.Received, out.Sent = c.store.List()
		meshes, peers := dir()
		for _, m := range meshes {
			out.Meshes = append(out.Meshes, m.Label)
		}
		for _, p := range peers {
			out.Devices = append(out.Devices, clipDevice{p.Mesh, p.Name, p.Online})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /clips", func(w http.ResponseWriter, r *http.Request) {
		var req clip.Request
		if err := json.NewDecoder(io.LimitReader(r.Body, clip.MaxText*2)).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		cl, err := c.store.Send(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, cl)
	})
	mux.HandleFunc("POST /clips/{id}/take", func(w http.ResponseWriter, r *http.Request) {
		var cl clip.Clip
		var err error
		if id := r.PathValue("id"); id == "latest" {
			cl, err = c.store.Latest()
		} else {
			cl, err = c.store.Take(id)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, cl)
	})
	mux.HandleFunc("DELETE /clips/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := c.store.Delete(r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// clipCall is one request to the daemon's clip endpoints.
func clipCall(sock, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, "http://shrooms"+path, rd)
	resp, err := controlClient(sock).Do(req)
	if err != nil {
		return fmt.Errorf("is the daemon running? %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && path == "/clips" {
		return errors.New("this daemon has no clips; it predates them")
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// cmdClipboard is `shrooms clipboard`: one command with its verbs, rather than
// three more at the top of a CLI that is about the mesh.
func cmdClipboard(args []string) error {
	if len(args) == 0 {
		return cmdClipList(nil)
	}
	switch args[0] {
	case "copy", "cp":
		return cmdClipCopy(args[1:])
	case "paste":
		return cmdClipPaste(args[1:])
	case "list", "ls":
		return cmdClipList(args[1:])
	case "remove", "rm":
		return cmdClipRemove(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, `usage: shrooms clipboard VERB   (clip for short)
  copy --to DEV[,DEV] [TEXT]   send to your other devices; stdin without TEXT
       --all --mesh M          to every other device on one mesh
       --secret                a key or password: never written down
       --ttl DUR               how long it waits and is kept: 24h by default,
                               1h for a secret, at most 168h
  paste [ID]                   print the newest clip that arrived
  list [--json]                what arrived and what was sent (the default),
                               with each clip's ID
  remove ID                    forget one (its ID from list)`)
		return nil
	default:
		return fmt.Errorf("unknown clipboard command %q; try: copy, paste, list, remove", args[0])
	}
}

// cmdClipCopy sends a clip: `shrooms clipboard copy --to phone "text"`, or
// from stdin.
func cmdClipCopy(args []string) error {
	fs := flag.NewFlagSet("clipboard copy", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	to := fs.String("to", "", "who it is for: device names, comma-separated; name.mesh picks the mesh")
	all := fs.Bool("all", false, "every other device on the mesh (--mesh, when on several)")
	meshName := fs.String("mesh", "", "which mesh it goes on")
	secret := fs.Bool("secret", false, "a key or password: never written down, gone 5 minutes after it is copied")
	ttl := fs.Duration("ttl", 0, "how long it waits and is kept (default 24h, 1h for a secret, at most 168h)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: shrooms clipboard copy --to DEVICE[,DEVICE] | --all [--mesh M] [--secret] [TEXT]\n"+
			"TEXT, or standard input when there is none.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if text == "" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, clip.MaxText+1))
		if err != nil {
			return err
		}
		// What a pipe adds at the end is not part of the URL being copied.
		text = strings.TrimRight(string(b), "\r\n")
	}
	req := clip.Request{Text: text, All: *all, Mesh: *meshName, Secret: *secret, TTL: int64(ttl.Seconds())}
	if *to != "" {
		req.To = strings.Split(*to, ",")
	}
	var c clip.Clip
	if err := clipCall(*sock, http.MethodPost, "/clips", req, &c); err != nil {
		return err
	}
	names := make([]string, len(c.To))
	for i, t := range c.To {
		names[i] = t.Name
	}
	fmt.Fprintf(os.Stderr, "for %s on %s, until %s\n", strings.Join(names, ", "), c.Mesh, c.Expires.Format("Jan 2 15:04"))
	return nil
}

// cmdClipPaste prints a clip that arrived: the newest, or the one named.
func cmdClipPaste(args []string) error {
	fs := flag.NewFlagSet("clipboard paste", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: shrooms clipboard paste [ID]   the newest clip that arrived, or the one with ID (shrooms clipboard list)")
	}
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	id := "latest"
	if fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	var c clip.Clip
	if err := clipCall(*sock, http.MethodPost, "/clips/"+id+"/take", nil, &c); err != nil {
		return err
	}
	fmt.Print(c.Text)
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Println()
	}
	return nil
}

// cmdClipRemove forgets a clip; a sent one still waiting is not sent.
func cmdClipRemove(args []string) error {
	fs := flag.NewFlagSet("clipboard remove", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: shrooms clipboard remove ID   forget it; a sent one still waiting is not sent")
	}
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	return clipCall(*sock, http.MethodDelete, "/clips/"+fs.Arg(0), nil, nil)
}

// cmdClipList lists what arrived and what was sent.
func cmdClipList(args []string) error {
	fs := flag.NewFlagSet("clipboard list", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	asJSON := fs.Bool("json", false, "as JSON")
	if err := fs.Parse(splitArgs(fs, args)); err != nil {
		return err
	}
	var l clipList
	if err := clipCall(*sock, http.MethodGet, "/clips", nil, &l); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(l)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ARRIVED\tFROM\tID\tTEXT")
	for _, c := range l.Received {
		from := c.From + "." + c.Mesh
		if c.Unsigned {
			// A name only announced, not signed: say so where it is read.
			from += " (unsigned)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Created.Format("Jan 2 15:04"), from, c.ID[:8], preview(c))
	}
	fmt.Fprintln(tw, "\nSENT\tTO\tID\tTEXT")
	for _, c := range l.Sent {
		var to []string
		for _, t := range c.To {
			mark := "✓"
			if t.Sent.IsZero() {
				mark = "…"
			}
			to = append(to, t.Name+mark)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Created.Format("Jan 2 15:04"), strings.Join(to, " "), c.ID[:8], preview(c))
	}
	return tw.Flush()
}

// preview is a clip in one line; a secret is not shown.
func preview(c clip.Clip) string {
	if c.Secret {
		return "(secret)"
	}
	s := strings.Join(strings.Fields(c.Text), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return s
}
