package agent

// A cage reaches agents only through its own (ADR-044). The agent port is
// closed inside every cage (closeAgentPort), and each caged session gets a
// socket to this agent instead, mounted at /run/shrooms-agent/proxy.sock,
// which forwards what the shrooms tools need — the machines and their
// sessions, an agent's card, SendMessage, GetTask, AckTask, and task_update
// for the session's own tasks — and nothing that makes, deletes or
// reconfigures a session. It names the asker itself, from the session the
// socket belongs to, and says it is caged (cagedHeader), which a receiver can
// believe: no cage can reach an agent except through its own.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cagedHeader carries the caged session a request comes from, set by its
// own agent's proxy and by nothing else.
const cagedHeader = "X-Shrooms-Caged"

// CageProxyDir is where a cage sees its socket.
const CageProxyDir = "/run/shrooms-agent"

// proxyLabel marks a container with how it was made; one of an older
// generation is made again on its next start. 1: with its socket to its
// agent (ADR-044). 2: without the shrooms daemon's control socket.
const (
	proxyLabel     = "xyz.vpavlin.shrooms.proxy"
	cageGeneration = "2"
	// sealedLabel marks a sealed cage with how it was sealed; 1: without
	// user namespaces (seccomp).
	sealedLabel      = "xyz.vpavlin.shrooms.sealed"
	sealedGeneration = "1"
)

// Machine is one machine on the mesh with an agent's address: this one
// first, then its peers (cmd/shrooms-agent fills Manager.Machines).
type Machine struct {
	Name string     `json:"name"`
	Addr netip.Addr `json:"addr"`
}

// cageProxies are the proxies running, by container.
type cageProxies struct {
	mu sync.Mutex
	ls map[string]net.Listener
}

// proxyDir is the directory a cage's socket is in, on the machine: mounted
// as a directory, so a socket made again after the agent restarts is the one
// the cage sees.
func (m *Manager) proxyDir(container string) string {
	return filepath.Join(m.dir, "cages", container+".d")
}

// startCageProxy serves a caged session's socket, if it is not served yet.
func (m *Manager) startCageProxy(s *Session, container string) error {
	m.proxies.mu.Lock()
	defer m.proxies.mu.Unlock()
	if m.proxies.ls == nil {
		m.proxies.ls = map[string]net.Listener{}
	}
	if _, ok := m.proxies.ls[container]; ok {
		return nil
	}
	dir := m.proxyDir(container)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sock := filepath.Join(dir, "proxy.sock")
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	m.proxies.ls[container] = l
	srv := &http.Server{Handler: m.cageProxy(s), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	return nil
}

// stopCageProxy stops serving a cage's socket, and removes it.
func (m *Manager) stopCageProxy(container string) {
	m.proxies.mu.Lock()
	l := m.proxies.ls[container]
	delete(m.proxies.ls, container)
	m.proxies.mu.Unlock()
	if l != nil {
		l.Close()
	}
	os.RemoveAll(m.proxyDir(container))
}

// proxyAllowed: what a cage may ask of an agent, by method and path, and for
// JSON-RPC the method in the body ("" for none).
func proxyAllowed(method, path, rpc string) bool {
	switch {
	case method == http.MethodGet && (path == "/v1/sessions" || path == "/.well-known/agent-card.json" ||
		strings.HasPrefix(path, "/a2a/") && strings.HasSuffix(path, "/.well-known/agent-card.json")):
		return true
	case method == http.MethodPost && (path == "/a2a" || strings.HasPrefix(path, "/a2a/") && !strings.Contains(path[5:], "/")):
		switch rpc {
		case "SendMessage", "SendStreamingMessage", "GetTask", "AckTask":
			return true
		}
	case method == http.MethodPost && isTaskUpdate(path):
		return true // its own machine and session only (cageProxy)
	case method == http.MethodPost && isDrop(path):
		return true // a file to another session, named as this one's (drop.go)
	}
	return false
}

// isTaskUpdate is task_update's path, /v1/tasks/{id}, and nothing under it:
// a cage finishes its own session's tasks, and does not answer, cancel or
// acknowledge anyone's (/answer, /cancel, /ack, /nudge).
func isTaskUpdate(path string) bool {
	id, ok := strings.CutPrefix(path, "/v1/tasks/")
	return ok && id != "" && !strings.Contains(id, "/")
}

// isDrop is a file sent to a session: /v1/sessions/{name}/drop.
func isDrop(path string) bool {
	rest, ok := strings.CutPrefix(path, "/v1/sessions/")
	name, tail, _ := strings.Cut(rest, "/")
	return ok && name != "" && tail == "drop"
}

// cageProxy is a caged session's socket.
func (m *Manager) cageProxy(s *Session) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /machines", func(w http.ResponseWriter, r *http.Request) {
		ms, err := m.machines()
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	})
	mux.HandleFunc("/peer/{addr}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		addr, err := netip.ParseAddr(r.PathValue("addr"))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		path := "/" + r.PathValue("path")
		limit := int64(16 << 20)
		if isDrop(path) {
			limit = MaxDrop + 1
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		var rpc struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(body, &rpc)
		s.mu.Lock()
		sealed := s.cage != nil && s.cage.Sealed
		s.mu.Unlock()
		// A sealed cage answers and asks nothing (ADR-045): it finishes its
		// own tasks, and a review poisoned by what it read cannot instruct
		// the owner's other agents.
		if sealed && !(r.Method == http.MethodPost && isTaskUpdate(path)) {
			fail(w, http.StatusForbidden, fmt.Errorf("not from a sealed cage: %s %s %s", r.Method, path, rpc.Method))
			return
		}
		if !proxyAllowed(r.Method, path, rpc.Method) {
			fail(w, http.StatusForbidden, fmt.Errorf("not from a cage: %s %s %s", r.Method, path, rpc.Method))
			return
		}
		ms, err := m.machines()
		if err != nil || len(ms) == 0 {
			fail(w, http.StatusBadGateway, fmt.Errorf("the machines on the mesh: %v", err))
			return
		}
		self := ms[0]
		switch {
		case isTaskUpdate(path):
			// task_update: on this machine, for this session's tasks.
			if addr != self.Addr {
				fail(w, http.StatusForbidden, errors.New("a cage updates only its own session's tasks, on its own machine"))
				return
			}
			var u map[string]any
			json.Unmarshal(body, &u)
			if u == nil {
				u = map[string]any{}
			}
			u["session"] = s.Name()
			body, _ = json.Marshal(u)
		case rpc.Method == "SendMessage" || rpc.Method == "SendStreamingMessage":
			// The asker, as this agent knows it, not as the cage says.
			if body, err = nameAsker(body, self.Name+"/"+s.Name()); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
		}
		m.forward(w, r, addr, path, body, s.Name())
	})
	return mux
}

// nameAsker writes shrooms/from into a SendMessage's message metadata.
func nameAsker(body []byte, from string) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	params, _ := req["params"].(map[string]any)
	msg, _ := params["message"].(map[string]any)
	if msg == nil {
		return nil, errors.New("a message is needed")
	}
	meta, _ := msg["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["shrooms/from"] = from
	msg["metadata"] = meta
	return json.Marshal(req)
}

// forward sends a cage's request to an agent and streams the answer back.
func (m *Manager) forward(w http.ResponseWriter, r *http.Request, addr netip.Addr, path string, body []byte, session string) {
	ctx, cancel := context.WithTimeout(r.Context(), 11*time.Minute) // a blocking send is held for at most 10
	defer cancel()
	u := m.agentURL(addr) + path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery // a file's name and note (drop.go)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, bytes.NewReader(body))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	req.Header.Set("Accept", r.Header.Get("Accept"))
	req.Header.Set(cagedHeader, session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// agentURL is where an agent at addr is reached: its port on that address,
// or a test's server.
func (m *Manager) agentURL(addr netip.Addr) string {
	if m.agentAt != nil {
		return m.agentAt(addr)
	}
	return "http://" + net.JoinHostPort(addr.String(), fmt.Sprint(Port))
}

func (m *Manager) machines() ([]Machine, error) {
	if m.Machines == nil {
		return nil, errors.New("this agent does not know the mesh's machines")
	}
	return m.Machines()
}

// closeAgentPort closes the agent port inside a started cage's network
// namespace, from outside it: an nftables rule its root, without NET_ADMIN,
// cannot remove. On every start, since the namespace is made anew.
func (c *Cages) closeAgentPort(ctx context.Context, container string, sealed bool) error {
	pid, err := c.run(ctx, "inspect", "--format", "{{.State.Pid}}", container)
	if err != nil {
		return err
	}
	pid = strings.TrimSpace(pid)
	nft := c.Nft
	if nft == "" {
		if nft, err = exec.LookPath("nft"); err != nil {
			return errors.New("no nft on this machine to close the agent port in the cage with")
		}
	}
	cmd := exec.CommandContext(ctx, c.Podman, "unshare", "nsenter", "-t", pid, "-n", nft, "-f", "-")
	cmd.Stdin = strings.NewReader(cageRules(sealed))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("closing the agent port in the cage: %v: %s", err, lastLine(strings.TrimSpace(string(out))))
	}
	return nil
}
