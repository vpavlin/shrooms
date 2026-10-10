// Command shrooms-agent serves this machine's Claude Code sessions to the
// owner's other devices over the mesh (docs/agents.md).
//
// It listens only on this device's overlay addresses, so only mesh members can
// reach it, and it names every caller by the overlay address its request came
// from. Run it as the user whose Claude Code it should be — not as root:
// sessions run with that user's settings, permissions and credentials.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms/internal/agent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "shrooms-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "a2a" {
		return a2aMain(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "files" {
		return filesMain(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		return mcpMain(os.Args[2:])
	}
	home, _ := os.UserHomeDir()
	meshes := flag.String("meshes", "", "comma-separated meshes to serve on (default: every mesh this device is in)")
	port := flag.Int("port", agent.Port, "port on each mesh address")
	stateDir := flag.String("state", filepath.Join(home, ".local", "share", "shrooms-agent"), "where sessions and their history are kept")
	claude := flag.String("claude", "claude", "the claude binary")
	sock := flag.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	verbose := flag.Bool("verbose", false, "log Claude Code's stderr")
	sttModel := flag.String("stt-model", filepath.Join(home, ".local", "share", "whisper", "ggml-parakeet-tdt-0.6b-v3-q4_k.bin"),
		"ggml model for voice notes, Parakeet or Whisper (docs/speech-to-text.md); voice notes are off when it is missing")
	sttBin := flag.String("stt-bin", "", "the CLI that runs it (default: parakeet-cli for a Parakeet model, else whisper-cli)")
	cages := flag.Bool("cages", true, "offer cages: sessions in rootless podman containers of their own, when podman is here (docs/agents-in-cages.md)")
	sealedToken := flag.String("sealed-token", "", "the file with the Claude Code token sealed cages run on (default: sealed-claude-token in the state directory; make one with `claude setup-token`)")
	cageImage := flag.String("cage-image", "", "the image cages are made from (default: the workbench, built here when first needed)")
	sttThreads := flag.Int("stt-threads", 0, "threads for transcription (default: this machine's cores, up to 12)")
	piBin := flag.String("pi", "pi", "pi (pi.dev), offered for new sessions when found; \"\" to leave it out")
	piArgs := flag.String("pi-args", "", "extra arguments for every pi session, e.g. \"--provider ollama --model qwen3\"")
	mcp := flag.Bool("mcp", true, "give every session the mesh's agents as MCP tools (shrooms-agent mcp) and tell it how to use them")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if os.Geteuid() == 0 {
		log.Warn("running as root: every session can do anything on this machine",
			"fix", "run it as the user whose Claude Code it should be")
	}

	// The daemon may still be starting; wait for it rather than fail, so a
	// unit ordered after it does not race it.
	var st status
	for {
		var err error
		if st, err = fetchStatus(*sock); err == nil && len(st.Meshes) > 0 {
			break
		} else if err == nil {
			err = errors.New("the daemon reports no meshes yet")
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		// With why: "permission denied" is a socket this user may not read
		// (docs/agents.md, On a server), not a daemon still starting — and
		// the two looked the same in the log after jimmy-crib rebooted.
		log.Info("waiting for the shrooms daemon", "socket", *sock, "why", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	addrs, err := st.addresses(*meshes)
	if err != nil {
		return err
	}

	m, err := agent.NewManager(ctx, log, *stateDir, *claude)
	if err != nil {
		return err
	}
	if *mcp {
		if self, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(self); err == nil {
				self = real
			}
			m.Self = self
		}
	}
	// The mesh's machines, this one first: what a cage's socket offers
	// (ADR-044), and the name a note to a task's asker gives this machine
	// (asker.go) — on every agent, caged or not: without it the notes named
	// tasks with no machine, which task_ack cannot take (2026-10-10).
	m.Machines = func() ([]agent.Machine, error) {
		st, err := fetchStatus(*sock)
		if err != nil {
			return nil, err
		}
		var out []agent.Machine
		for _, x := range st.machines() {
			out = append(out, agent.Machine{Name: x.Name, Addr: x.Addr})
		}
		return out, nil
	}
	// This machine's meshes, by label and address: which mesh a request came
	// over, and what a session may be limited to (meshes.go).
	// Asked of the daemon at most every ten seconds: a local request checks it.
	var meshMu sync.Mutex
	var meshAt time.Time
	var meshKept []agent.MeshInfo
	m.Meshes = func() []agent.MeshInfo {
		meshMu.Lock()
		defer meshMu.Unlock()
		if time.Since(meshAt) < 10*time.Second {
			return meshKept
		}
		st, err := fetchStatus(*sock)
		if err != nil {
			return meshKept
		}
		var out []agent.MeshInfo
		for _, x := range st.Meshes {
			if a, err := netip.ParseAddr(x.Overlay); err == nil {
				out = append(out, agent.MeshInfo{Label: x.Label, Addr: a})
			}
		}
		meshKept, meshAt = out, time.Now()
		return out
	}
	if *cages {
		if m.Cages = agent.NewCages(*cageImage); m.Cages != nil {
			m.Cages.Socket = *sock
			m.Cages.SealedToken = *sealedToken
			if m.Cages.SealedToken == "" {
				m.Cages.SealedToken = filepath.Join(*stateDir, agent.SealedTokenFile)
			}
			log.Info("cages offered", "podman", m.Cages.Podman, "image", m.Cages.Image)
			// The images caged sessions use, built again if this agent's
			// Containerfile is newer than theirs.
			for _, in := range m.List() {
				if in.Cage != nil {
					m.Cages.Prepare(in.Cage.Image, log.Info)
				}
			}
		}
	}
	// Other harnesses, when this machine has them (docs/agents-harnesses.md).
	if *piBin != "" {
		if bin, err := exec.LookPath(*piBin); err == nil {
			m.Register(agent.Pi{Extra: strings.Fields(*piArgs)}, bin)
			log.Info("harness", "name", "pi", "bin", bin)
			m.WatchCredits(ctx)
			if m.Self != "" {
				if err := (agent.Pi{}).EnsureMCP(m.Self); err != nil {
					log.Warn("pi will not have the mesh's agents as tools", "err", err)
				}
			}
		}
	}
	if _, err := os.Stat(*sttModel); err != nil {
		log.Info("voice notes off: no speech-to-text model", "model", *sttModel)
	} else if bin, err := exec.LookPath(sttEngine(*sttBin, *sttModel)); err != nil {
		log.Info("voice notes off: its CLI was not found", "bin", sttEngine(*sttBin, *sttModel))
	} else {
		threads := *sttThreads
		if threads <= 0 {
			threads = min(runtime.NumCPU(), 12)
		}
		m.STT = &agent.Transcriber{Bin: bin, Model: *sttModel, FFmpeg: "ffmpeg", FFprobe: "ffprobe", Threads: threads}
		log.Info("voice notes on", "model", filepath.Base(*sttModel), "bin", bin)
	}

	names := &peerNames{}
	names.update(st)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				if st, err := fetchStatus(*sock); err == nil {
					names.update(st)
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/", agent.Handler(log, m, names.who))
	// The mesh as this machine sees it, so a phone that knows one agent finds
	// the rest without being told (the preview build has no peer list of its
	// own). Names and mesh addresses only: what any member already sees.
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"peers": names.list()})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var wg sync.WaitGroup
	for label, a := range addrs {
		ln, err := net.Listen("tcp", net.JoinHostPort(a.String(), strconv.Itoa(*port)))
		if err != nil {
			return fmt.Errorf("mesh %s: %w", label, err)
		}
		log.Info("serving agents", "mesh", label, "at", ln.Addr().String())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("stopped serving", "mesh", label, "err", err)
			}
		}()
	}
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shut)
	wg.Wait()
	return nil
}

// status is the part of the daemon's /status this needs.
type status struct {
	// Name is this device's name on the mesh ("laptop"), not its hostname.
	Name   string `json:"name"`
	Meshes []struct {
		Label   string `json:"label"`
		Overlay string `json:"overlay"`
	} `json:"meshes"`
	Peers []struct {
		Name      string `json:"name"`
		Mesh      string `json:"mesh"`
		Overlay   string `json:"overlay"`
		OverlayV4 string `json:"overlay_v4"`
	} `json:"peers"`
}

func fetchStatus(sock string) (status, error) {
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := c.Get("http://unix/status")
	if err != nil {
		return status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status{}, fmt.Errorf("status: %s", resp.Status)
	}
	var st status
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// addresses picks the overlay address of each mesh to serve on.
func (st status) addresses(want string) (map[string]netip.Addr, error) {
	only := map[string]bool{}
	for _, l := range strings.Split(want, ",") {
		if l = strings.TrimSpace(l); l != "" {
			only[l] = true
		}
	}
	out := map[string]netip.Addr{}
	for _, m := range st.Meshes {
		if len(only) > 0 && !only[m.Label] {
			continue
		}
		a, err := netip.ParseAddr(m.Overlay)
		if err != nil {
			return nil, fmt.Errorf("mesh %s: overlay %q: %w", m.Label, m.Overlay, err)
		}
		out[m.Label] = a
	}
	for l := range only {
		if _, ok := out[l]; !ok {
			return nil, fmt.Errorf("this device is not in a mesh called %q", l)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no mesh to serve on")
	}
	return out, nil
}

// peerNames maps overlay addresses to device names, refreshed from the
// daemon so a device that joins later is named too.
type peerNames struct {
	mu    sync.Mutex
	by    map[netip.Addr]string
	peers []peer
}

// peer is one member as /v1/peers reports it.
type peer struct {
	Name    string `json:"name"`
	Mesh    string `json:"mesh"`
	Overlay string `json:"overlay"`
}

func (p *peerNames) list() []peer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]peer{}, p.peers...)
}

func (p *peerNames) update(st status) {
	by := map[netip.Addr]string{}
	var peers []peer
	for _, pr := range st.Peers {
		if _, err := netip.ParseAddr(pr.Overlay); err == nil {
			peers = append(peers, peer{Name: pr.Name, Mesh: pr.Mesh, Overlay: pr.Overlay})
		}
	}
	for _, peer := range st.Peers {
		// A daemon of one mesh from before peers carried theirs (pi5's,
		// 0a282c1) leaves it out: the name alone, not "laptop.".
		name := peer.Name
		if peer.Mesh != "" {
			name += "." + peer.Mesh
		}
		for _, s := range []string{peer.Overlay, peer.OverlayV4} {
			if a, err := netip.ParseAddr(s); err == nil {
				by[a] = name
			}
		}
	}
	p.mu.Lock()
	p.by, p.peers = by, peers
	p.mu.Unlock()
}

func (p *peerNames) who(a netip.Addr) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.by[a]
}

// sttEngine is the CLI to run a model with: the one asked for, or the one the
// model takes.
func sttEngine(bin, model string) string {
	if bin != "" {
		return bin
	}
	return agent.EngineFor(model)
}
