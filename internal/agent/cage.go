package agent

// Cages (ADR-037, docs/agents-in-cages.md): a session whose process runs in a
// rootless podman container of its own instead of as the owner's user on the
// machine. Root inside, so the agent can install what its work needs; the
// owner's user outside, so the project's files stay the owner's and the rest
// of the machine is out of reach.
//
// The container is made with the session's first start and kept: it runs
// `sleep infinity`, the harness is run in it with `podman exec`, and it is
// stopped when the harness's process ends — which also ends anything the
// session left running — and started again with the next. What the agent
// installed stays until the session is deleted, and the container with it.
//
// Inside, the paths are the machine's: the project at its own path, the
// harness's settings and transcripts (~/.claude, ~/.pi/agent) at theirs, the
// files sent to sessions, shrooms-agent and the daemon's socket (the shrooms
// MCP server). So a caged conversation resumes, is read and searched exactly
// as one that is not. Nothing else of the owner's home is there.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed workbench.Containerfile
var workbenchContainerfile []byte

// WorkbenchImage is the image cages are made from unless the machine or the
// session names another: built here from workbench.Containerfile. And
// DesktopImage is the workbench with an X server, to run, drive and record
// apps in.
const (
	WorkbenchImage = "localhost/shrooms-workbench:latest"
	DesktopImage   = "localhost/shrooms-workbench:desktop"
)

// builtin are the images built here, from workbench.Containerfile's stages.
var builtin = map[string]string{WorkbenchImage: "workbench", DesktopImage: "desktop"}

// Cage is how a session is caged; nil for a session that is not.
type Cage struct {
	// Image is the session's own; "" for the machine's (Cages.Image).
	Image string `json:"image,omitempty"`
	// Container is podman's name for it, given when the session is made:
	// the session's name then, and a few random letters, so a rename leaves
	// it be and a later session of the same name gets its own.
	Container string `json:"container,omitempty"`
	// Nix gives the cage the machine's nix: its store and profile, and
	// builds through its daemon where it has one. A single-user nix has
	// none, and the cage then writes the store itself — as the owner would.
	Nix bool `json:"nix,omitempty"`
	// GitHub gives it the owner's GitHub CLI login (~/.config/gh), read-only.
	GitHub bool `json:"github,omitempty"`
	// Sealed: for code nobody vouches for (sealed.go, ADR-045) — the
	// internet and nothing local, a credential of its own, no agents to
	// ask, results through Outbox (a directory on the machine).
	Sealed bool   `json:"sealed,omitempty"`
	Outbox string `json:"outbox,omitempty"`
}

// newCage is the cage a session is given from what was asked: its own
// container name, and for a sealed one its outbox and none of the options
// that widen a cage.
func newCage(asked *Cage, session string) *Cage {
	c := &Cage{Image: asked.Image, Nix: asked.Nix, GitHub: asked.GitHub, Sealed: asked.Sealed, Container: newContainerName(session)}
	if c.Sealed {
		c.Nix, c.GitHub = false, false
		c.Outbox = sealedOutbox(session)
	}
	return c
}

// cageNetwork gives a cage an address of its own and a default route through
// pasta, which carries what the cage sends out on the machine's own sockets.
// Without it pasta copies the addresses and routes of one of the machine's
// interfaces — a mesh's, on a machine with no IPv6 default route — and the
// other meshes, and this machine's own agent on that one, are not reachable.
const cageNetwork = "pasta:-a,fd5e:ca9e:1::2,-g,fd5e:ca9e:1::1"

// Cages is what this machine makes cages with.
type Cages struct {
	Podman string // the podman binary
	Image  string // the machine's image; WorkbenchImage when ""
	// Limits for each cage; "" or 0 for none.
	Memory string
	CPUs   string
	Pids   int
	// Socket is the shrooms daemon's, which the shrooms MCP server reads.
	Socket string
	// Nft is the nft program closing the agent port in cages; "" finds it.
	Nft string
	// SealedToken is the file with the Claude Code token sealed cages run
	// on; "" for sealed-claude-token in the state directory.
	SealedToken string

	mu       sync.Mutex
	building bool
	buildErr string
}

// CageInfo is a session's cage as the list shows it.
type CageInfo struct {
	Image  string `json:"image"`
	Nix    bool   `json:"nix,omitempty"`
	GitHub bool   `json:"github,omitempty"`
	Sealed bool   `json:"sealed,omitempty"`
	Outbox string `json:"outbox,omitempty"`
}

// CageStatus is what the apps are told: whether sessions can be caged here.
type CageStatus struct {
	Available bool   `json:"available"`
	Image     string `json:"image,omitempty"`
	// Images are those offered: the machine's, and the ones built here.
	Images []string `json:"images,omitempty"`
	// Nix: the machine has nix to give a cage.
	Nix bool `json:"nix,omitempty"`
	// Sealed: the machine has a token for sealed cages (sealed.go).
	Sealed   bool   `json:"sealed,omitempty"`
	Ready    bool   `json:"ready"`              // the image is there
	Building bool   `json:"building,omitempty"` // it is being built
	Error    string `json:"error,omitempty"`    // why the last build failed
}

// NewCages finds podman; nil when the machine has none.
func NewCages(image string) *Cages {
	p, err := exec.LookPath("podman")
	if err != nil {
		return nil
	}
	if image == "" {
		image = WorkbenchImage
	}
	return &Cages{Podman: p, Image: image, Memory: "4g", CPUs: "2", Pids: 2048, Socket: "/run/shrooms/shrooms.sock"}
}

func (c *Cages) run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Podman, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("podman %s: %s", args[0], lastLine(msg))
	}
	return out.String(), nil
}

func (c *Cages) image(cg *Cage) string {
	if cg != nil && cg.Image != "" {
		return cg.Image
	}
	return c.Image
}

func (c *Cages) imageExists(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := c.run(ctx, "image", "exists", image)
	return err == nil
}

// workbenchLabel carries a hash of the Containerfile an image of ours was
// built from: an agent with a newer one builds it again.
const workbenchLabel = "xyz.vpavlin.shrooms.containerfile"

func workbenchHash() string {
	sum := sha256.Sum256(workbenchContainerfile)
	return hex.EncodeToString(sum[:8])
}

// current: an image of ours built from this agent's Containerfile.
func (c *Cages) current(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := c.run(ctx, "image", "inspect", "--format", "{{index .Labels \""+workbenchLabel+"\"}}", image)
	return err == nil && strings.TrimSpace(out) == workbenchHash()
}

// Status says whether the machine's image is ready.
func (c *Cages) Status() CageStatus {
	if c == nil {
		return CageStatus{}
	}
	c.mu.Lock()
	st := CageStatus{Available: true, Image: c.Image, Building: c.building, Error: c.buildErr,
		Images: []string{c.Image}, Nix: nixHere(), Sealed: tokenIn(c.SealedToken)}
	c.mu.Unlock()
	for _, im := range []string{WorkbenchImage, DesktopImage} {
		if im != c.Image {
			st.Images = append(st.Images, im)
		}
	}
	st.Ready = !st.Building && c.imageExists(c.Image)
	return st
}

// Prepare builds an image of ours, in the background, if it is the one a
// cage needs and it is not there, or was built from an older Containerfile
// (the old one serves until then). Another image is the owner's to provide.
func (c *Cages) Prepare(image string, log func(msg string, args ...any)) {
	target, ours := builtin[image]
	if !ours || c.current(image) {
		return
	}
	c.mu.Lock()
	if c.building {
		c.mu.Unlock()
		return
	}
	c.building, c.buildErr = true, ""
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		log("building the workbench image for cages", "image", image)
		// On the machine's network: a build's own, through pasta, timed
		// out fetching from npm on the laptop (2026-10-08).
		cmd := exec.CommandContext(ctx, c.Podman, "build", "--network", "host", "--target", target,
			"--label", workbenchLabel+"="+workbenchHash(), "-t", image, "-f", "-", os.TempDir())
		cmd.Stdin = bytes.NewReader(workbenchContainerfile)
		out, err := cmd.CombinedOutput()
		c.mu.Lock()
		c.building = false
		if err != nil {
			c.buildErr = "building the workbench image: " + lastLine(strings.TrimSpace(string(out)))
		}
		c.mu.Unlock()
		log("workbench image", "image", image, "err", err)
	}()
}

// newContainerName is a new session's container's name.
func newContainerName(session string) string {
	b := make([]byte, 3)
	rand.Read(b)
	return "shrooms-" + session + "-" + hex.EncodeToString(b)
}

// mount is one path of the machine's in a cage, at the same path.
type mount struct {
	path string
	ro   bool
	at   string // where in the cage, when not at the same path
}

// cageRun is what one start of a caged session's process needs.
type cageRun struct {
	c         *Cages
	container string
	dir       string
	envFile   string
	bin       string // the harness's program inside
}

// cageMounts are the machine's paths a session's cage sees.
func (m *Manager) cageMounts(s *Session, harness string) []mount {
	home, _ := os.UserHomeDir()
	// The files sent to sessions, read-only: all of them, since a session
	// renamed keeps finding what was sent before under its old name.
	up := filepath.Join(m.dir, "uploads")
	os.MkdirAll(up, 0o700)
	ms := []mount{{path: s.dir}, {path: up, ro: true}}
	switch harness {
	case "claude":
		dir := filepath.Join(home, ".claude")
		ms = append(ms, mount{path: dir})
		ms = append(ms, readOnlyIn(dir, claudeHostCode)...)
		if bin, err := claudeProgram(m.binOf("claude")); err == nil {
			ms = append(ms, mount{path: filepath.Dir(bin), ro: true})
		}
	case "pi":
		dir := piAgentDir()
		ms = append(ms, mount{path: dir})
		ms = append(ms, readOnlyIn(dir, piHostCode)...)
	}
	if s.cage.Nix {
		ms = append(ms, nixMounts(home)...)
	}
	// Its own agent's socket, as a directory: one made again after the agent
	// restarts is the one the cage sees.
	pd := m.proxyDir(s.cage.Container)
	os.MkdirAll(pd, 0o700)
	ms = append(ms, mount{path: pd, at: CageProxyDir})
	if s.cage.GitHub {
		ms = append(ms, mount{path: filepath.Join(home, ".config", "gh"), ro: true})
	}
	if m.Self != "" {
		if self, err := filepath.EvalSymlinks(m.Self); err == nil {
			ms = append(ms, mount{path: self, ro: true})
			if self != m.Self {
				ms = append(ms, mount{path: m.Self, ro: true})
			}
		}
	}
	// Not the shrooms daemon's control socket. It was mounted for the
	// shrooms tools to find the machines, but the agent's user's tier on it
	// can leave or join a mesh, change the relay, the services and what is
	// announced, and restart the daemon — the machine, from a cage (found
	// 2026-10-09). The tools find the machines through the cage's own socket
	// to its agent now (cageproxy.go).
	var out []mount
	for _, x := range ms {
		if _, err := os.Stat(x.path); err == nil {
			out = append(out, x)
		}
	}
	return out
}

// The harness's settings directory is the cage's to write — its login is
// refreshed there, its transcripts kept — but not what the harness runs or
// obeys on the machine: a caged agent that could write a hook into
// ~/.claude/settings.json, or an extension into ~/.pi/agent, would have it run
// on the host by the next session outside a cage. Those paths are mounted
// again, read-only, over the writable directory.
var (
	claudeHostCode = []string{"settings.json", "settings.local.json", "CLAUDE.md", "hooks", "skills", "plugins",
		"commands", "agents", "output-styles", "bin"}
	piHostCode = []string{"settings.json", "models.json", "mcp.json", "extensions", "skills", "prompts", "themes",
		"AGENTS.md", "SYSTEM.md", "APPEND_SYSTEM.md", "bin"}
)

// readOnlyIn is each of names under dir, read-only (cageMounts keeps only
// those that exist).
func readOnlyIn(dir string, names []string) []mount {
	out := make([]mount, len(names))
	for i, n := range names {
		out[i] = mount{path: filepath.Join(dir, n), ro: true}
	}
	return out
}

// claudeProgram is Claude Code's program itself, symlinks followed: the
// native one, or npm's cli.js, which the workbench's node runs.
func claudeProgram(bin string) (string, error) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// nixDaemonSocket is where a multi-user nix's daemon listens.
const nixDaemonSocket = "/nix/var/nix/daemon-socket"

func nixHere() bool {
	_, err := os.Stat("/nix/store")
	return err == nil
}

// nixMounts give a cage the machine's nix. With a daemon, the store is read
// and builds go to the daemon, as for any user; a single-user nix is the
// owner's own, and the cage writes it as the owner.
func nixMounts(home string) []mount {
	if !nixHere() {
		return nil
	}
	ms := []mount{{path: filepath.Join(home, ".local", "state", "nix")}, {path: filepath.Join(home, ".config", "nix"), ro: true}}
	if _, err := os.Stat(nixDaemonSocket); err == nil {
		return append(ms, mount{path: "/nix", ro: true}, mount{path: nixDaemonSocket})
	}
	return append(ms, mount{path: "/nix"})
}

// cagePath is the PATH of a cage's processes: the image's, after nix's
// profile when the cage has nix.
func cagePath(home string, nix bool) string {
	p := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if nix {
		p = filepath.Join(home, ".local", "state", "nix", "profiles", "profile", "bin") + ":/nix/var/nix/profiles/default/bin:" + p
	}
	return p
}

func piAgentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

func (m *Manager) binOf(harness string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bins[harness]
}

// createArgs makes a session's container.
func (c *Cages) createArgs(name, session, image string, ms []mount, extra ...string) []string {
	args := []string{"create", "--name", name, "--label", "xyz.vpavlin.shrooms.session=" + session,
		// A cage made with its own agent's socket (ADR-044); one without is
		// made again.
		"--label", proxyLabel + "=" + cageGeneration,
		"--network", cageNetwork, "--security-opt", "label=disable",
		// Not in podman's defaults either; said so, since the rule that
		// closes the agent port depends on it.
		"--cap-drop", "NET_ADMIN"}
	if c.Memory != "" {
		args = append(args, "--memory", c.Memory)
	}
	if c.CPUs != "" {
		args = append(args, "--cpus", c.CPUs)
	}
	if c.Pids > 0 {
		args = append(args, "--pids-limit", fmt.Sprint(c.Pids))
	}
	for _, x := range ms {
		at := x.path
		if x.at != "" {
			at = x.at
		}
		v := x.path + ":" + at
		if x.ro {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	args = append(args, extra...)
	return append(args, image, "sleep", "infinity")
}

// prepare makes the session's container if it has none, starts it, and says
// how to run the harness in it. Called with s.mu held.
func (m *Manager) prepareCage(s *Session, bin string) (*cageRun, error) {
	c := m.Cages
	if c == nil {
		return nil, errors.New("this session is caged, and this machine has no podman to cage it with")
	}
	image := c.image(s.cage)
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	name := s.cage.Container
	if _, err := c.run(ctx, "container", "exists", name); err == nil {
		// Made before cages had their own agent's socket: made again, and
		// what was installed in it goes (ADR-044).
		out, _ := c.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+proxyLabel+"\"}}", name)
		sealedOut := sealedGeneration
		if s.cage.Sealed {
			sealedOut, _ = c.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+sealedLabel+"\"}}", name)
		}
		if strings.TrimSpace(out) != cageGeneration || strings.TrimSpace(sealedOut) != sealedGeneration {
			c.run(ctx, "rm", "-f", "-t", "3", name)
			s.record("caged", "shrooms", map[string]any{"caged": true, "image": image, "nix": s.cage.Nix, "github": s.cage.GitHub,
				"remade": "made again, to reach agents only through its own; what was installed in it is gone"})
		}
	}
	if _, err := c.run(ctx, "container", "exists", name); err != nil {
		if !c.imageExists(image) {
			c.Prepare(image, m.log.Info)
			if st := c.Status(); st.Error != "" {
				return nil, errors.New(st.Error)
			}
			if _, ours := builtin[image]; ours {
				return nil, errors.New("the image for this session's cage is being built (a few minutes, the first time); send again then")
			}
			return nil, fmt.Errorf("there is no image %s on this machine for this session's cage", image)
		}
		ms := m.cageMounts(s, s.harness.Name())
		var extra []string
		if s.cage.Sealed {
			var err error
			if ms, err = m.sealedMounts(s); err != nil {
				return nil, err
			}
			prof, err := m.sealedSeccomp()
			if err != nil {
				return nil, err
			}
			extra = []string{"--security-opt", "seccomp=" + prof, "--label", sealedLabel + "=" + sealedGeneration}
		}
		if _, err := c.run(ctx, c.createArgs(name, s.Name(), image, ms, extra...)...); err != nil {
			return nil, err
		}
	}
	if err := m.startCageProxy(s, name); err != nil {
		return nil, fmt.Errorf("the cage's socket to its agent: %w", err)
	}
	if _, err := c.run(ctx, "start", name); err != nil {
		return nil, err
	}
	if err := c.closeAgentPort(ctx, name, s.cage.Sealed); err != nil {
		// Not run with the port open: it would reach agents as this machine.
		c.run(ctx, "stop", "-t", "3", name)
		return nil, err
	}
	env, err := m.cageEnv(s)
	if err != nil {
		return nil, err
	}
	r := &cageRun{c: c, container: name, dir: s.dir, envFile: env, bin: s.harness.Bin()}
	if s.harness.Name() == "claude" {
		if p, err := claudeProgram(bin); err == nil {
			r.bin = p
		}
	}
	return r, nil
}

// cageEnv writes what a caged process is started with, to a file only the
// owner reads: keys stay out of the process list.
func (m *Manager) cageEnv(s *Session) (string, error) {
	if s.cage.Sealed {
		vars, err := m.sealedEnv(s)
		if err != nil {
			return "", err
		}
		return m.writeCageEnv(s, vars)
	}
	home, _ := os.UserHomeDir()
	vars := map[string]string{
		"HOME": home,
		// Claude Code keeps its settings file (.claude.json) beside its
		// home directory, where nothing of the owner's is mounted; this
		// keeps it in ~/.claude, which is.
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		// Root inside: Claude Code refuses to skip permissions as root
		// except in a sandbox, which a cage is.
		"IS_SANDBOX":            "1",
		"SHROOMS_AGENT_SESSION": s.Name(),
		// The shrooms tools reach agents through this (ADR-044).
		"SHROOMS_AGENT_PROXY": CageProxyDir + "/proxy.sock",
		"PATH":                cagePath(home, s.cage.Nix),
	}
	if s.cage.Nix {
		if _, err := os.Stat(nixDaemonSocket); err == nil {
			vars["NIX_REMOTE"] = "daemon"
		}
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if passedToCage(k, s.cage.GitHub) {
			vars[k] = v
		}
	}
	return m.writeCageEnv(s, vars)
}

// writeCageEnv writes a cage's environment file, which only the owner reads.
func (m *Manager) writeCageEnv(s *Session, vars map[string]string) (string, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if strings.ContainsAny(vars[k], "\n") {
			continue
		}
		b.WriteString(k + "=" + vars[k] + "\n")
	}
	dir := filepath.Join(m.dir, "cages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, s.cage.Container+".env")
	return p, os.WriteFile(p, []byte(b.String()), 0o600)
}

// passedToCage: the agent's environment variables a caged harness is given —
// the keys and settings of models and providers, not the machine's own (its
// PATH, its desktop, its user).
func passedToCage(k string, github bool) bool {
	// GitHub's tokens only to a cage given the GitHub login: otherwise the
	// *_TOKEN rule below handed the owner's GitHub to every cage.
	if strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "GITHUB_") {
		return github
	}
	// Claude Code's own CLAUDE_CODE_ variables, when the agent runs under
	// it, are about that session — its id, its socket — not this one's.
	if strings.HasPrefix(k, "CLAUDE_CODE_") {
		return k == "CLAUDE_CODE_OAUTH_TOKEN" || k == "CLAUDE_CODE_USE_BEDROCK" || k == "CLAUDE_CODE_USE_VERTEX"
	}
	for _, p := range []string{"ANTHROPIC_", "PI_", "OLLAMA_", "OPENAI_", "VENICE_", "OPENROUTER_", "GEMINI_", "GOOGLE_API"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	for _, s := range []string{"_API_KEY", "_TOKEN", "_BASE_URL"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	switch k {
	case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "TZ", "LANG":
		return true
	}
	return false
}

// command is the harness's process, run in the cage.
func (r *cageRun) command(args []string) (string, []string) {
	return r.c.Podman, append([]string{"exec", "-i", "-w", r.dir, "--env-file", r.envFile, r.container, r.bin}, args...)
}

// stop stops the cage once its harness's process has ended: what the session
// left running ends with it.
func (r *cageRun) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.c.run(ctx, "stop", "-t", "3", r.container)
}

// remove deletes a session's cage, and what was installed in it.
func (c *Cages) remove(container string, stateDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c.run(ctx, "rm", "-f", "-t", "3", container)
	os.Remove(filepath.Join(stateDir, "cages", container+".env"))
}

// busy reports a command still running in the cage besides the harness and
// its MCP servers — a background shell, which ends a turn but not the work.
func (c *Cages) busy(container, harnessBin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := c.run(ctx, "top", container, "args")
	if err != nil {
		return false
	}
	return busyIn(out, harnessBin)
}

func busyIn(top, harnessBin string) bool {
	lines := strings.Split(strings.TrimSpace(top), "\n")
	for _, l := range lines[min(1, len(lines)):] {
		l = strings.TrimSpace(l)
		switch {
		case l == "", strings.HasPrefix(l, "sleep infinity"), strings.Contains(l, "mcp"),
			harnessBin != "" && strings.HasPrefix(l, harnessBin), strings.HasPrefix(l, filepath.Base(harnessBin)+" "):
			continue
		}
		return true
	}
	return false
}

// SetCage puts a session in a cage, changes its cage, or takes it out of one
// (cage nil). Not while it works or a prompt waits: its process is stopped,
// and the next message starts it where it now runs, on the same
// conversation. The cage it had is deleted, and what was installed in it.
func (m *Manager) SetCage(name string, cage *Cage, by string) (Info, error) {
	s, ok := m.Get(name)
	if !ok {
		return Info{}, fmt.Errorf("no session called %q", name)
	}
	if cage != nil && m.Cages == nil {
		return Info{}, errors.New("this machine has no podman to cage sessions with")
	}
	if cage != nil && cage.Sealed {
		if err := m.canSeal(s.harness.Name()); err != nil {
			return Info{}, err
		}
	}
	if in := s.Info(); in.State != Idle || in.Pending > 0 {
		return Info{}, fmt.Errorf("session %s is %s: move it once it is idle", name, in.State)
	}
	s.stop()
	s.mu.Lock()
	old := s.cage
	if cage != nil {
		cage = newCage(cage, s.Name())
	}
	s.cage = cage
	data := map[string]any{"caged": cage != nil}
	if cage != nil {
		data["image"] = m.Cages.image(cage)
		data["nix"], data["github"], data["sealed"] = cage.Nix, cage.GitHub, cage.Sealed
	}
	s.record("caged", by, data)
	s.mu.Unlock()
	m.mu.Lock()
	err := m.save()
	m.mu.Unlock()
	if old != nil && m.Cages != nil {
		m.stopCageProxy(old.Container)
		go m.Cages.remove(old.Container, m.dir)
	}
	if cage != nil {
		m.Cages.Prepare(m.Cages.image(cage), m.log.Info)
	}
	return s.Info(), err
}
