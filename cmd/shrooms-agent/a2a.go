package main

// An A2A client for agents (docs/agents-together.md): `shrooms-agent a2a`
// for those that would rather run a command, and the tools of
// `shrooms-agent mcp` (mcp.go) for those that take MCP. It asks sessions on
// other machines — or this one — over the mesh.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/agent"
)

const a2aUsage = `usage:
  shrooms-agent a2a list                                 the agents on the mesh: MACHINE/SESSION, harness, state
  shrooms-agent a2a send [--wait] [--title T] MACHINE/SESSION TEXT
                                                         ask a session; --wait prints its reply, --title
                                                         names the task in lists
  shrooms-agent a2a send-file [--note N] MACHINE/SESSION FILE
                                                         push a file to a session that takes files from this one
  shrooms-agent a2a get  MACHINE/TASK-ID                 where a task stands, and its reply
  shrooms-agent a2a cancel MACHINE/TASK-ID               interrupt it
  shrooms-agent a2a ack MACHINE/TASK-ID                  you have seen its result: it is closed for you
  shrooms-agent a2a update TASK-ID done|blocked|failed SUMMARY
                                                         (a worker) finish a task given to this session

MACHINE is a peer's name as "shrooms status" shows it, or its overlay address.
A task id is what send printed: SESSION:MESSAGE-ID.`

// a2aClient reaches agents: machines found through the daemon's socket.
type a2aClient struct {
	sock string
	// dial, when set, is how every request goes: a cage's socket.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// base is an agent's URL; the agent's port on its overlay address, but
	// a test's server in tests.
	base func(netip.Addr) string
	// peers lists the machines with agents: this one and the daemon's peers.
	peers func() ([]machine, error)
}

type machine struct {
	Name string
	Addr netip.Addr
}

func newA2AClient(sock string) a2aClient {
	// In a cage, agents are reached only through its own agent's socket
	// (ADR-044): the machines from it, and every request through it.
	if p := os.Getenv("SHROOMS_AGENT_PROXY"); p != "" {
		return newProxiedClient(p)
	}
	c := a2aClient{sock: sock, base: func(a netip.Addr) string {
		return "http://" + net.JoinHostPort(a.String(), fmt.Sprint(agent.Port))
	}}
	c.peers = func() ([]machine, error) {
		st, err := fetchStatus(sock)
		if err != nil {
			return nil, err
		}
		return st.machines(), nil
	}
	return c
}

// machines is this device and its peers, each once — a peer on two meshes
// is one machine — by the names the mesh knows them by: this device as the
// daemon names it ("laptop"), not by its hostname.
func (st status) machines() []machine {
	var out []machine
	seen := map[string]bool{}
	self := st.Name
	if self == "" {
		self, _ = os.Hostname()
	}
	for _, m := range st.Meshes {
		if a, err := netip.ParseAddr(m.Overlay); err == nil {
			out = append(out, machine{self, a})
			seen[self] = true
			break
		}
	}
	for _, p := range st.Peers {
		if a, err := netip.ParseAddr(p.Overlay); err == nil && !seen[p.Name] {
			out = append(out, machine{p.Name, a})
			seen[p.Name] = true
		}
	}
	return out
}

func a2aMain(args []string) error {
	if len(args) == 0 {
		return errors.New(a2aUsage)
	}
	fs := flag.NewFlagSet("a2a "+args[0], flag.ContinueOnError)
	wait := fs.Bool("wait", false, "wait for the turn to end and print the reply")
	title := fs.String("title", "", "send: a few words saying what you ask, for lists of tasks")
	note := fs.String("note", "", "send-file: what the file is and what to do with it")
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket, to find machines by name")
	if err := fs.Parse(flagsFirst(args[1:], "wait")); err != nil {
		return err
	}
	c := newA2AClient(*sock)
	rest := fs.Args()
	var t cliTask
	var err error
	switch {
	case args[0] == "list":
		out, err := c.list()
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case args[0] == "send" && len(rest) >= 2:
		t, err = c.send(rest[0], strings.Join(rest[1:], " "), *title, *wait)
	case args[0] == "send-file" && len(rest) == 2:
		at, err := c.sendFile(rest[0], rest[1], *note, true)
		if err != nil {
			return err
		}
		fmt.Printf("sent %s to %s, kept there at %s\n", filepath.Base(rest[1]), rest[0], at)
		return nil
	case args[0] == "get" && len(rest) == 1:
		t, err = c.task(rest[0], "GetTask")
	case args[0] == "cancel" && len(rest) == 1:
		t, err = c.task(rest[0], "CancelTask")
	case args[0] == "ack" && len(rest) == 1:
		t, err = c.task(rest[0], "AckTask")
	case args[0] == "update" && len(rest) >= 2:
		t, err = c.update(rest[0], rest[1], strings.Join(rest[2:], " "))
	default:
		return errors.New(a2aUsage)
	}
	if err != nil {
		return err
	}
	fmt.Print(t.String())
	if t.State() == "failed" || t.State() == "rejected" {
		os.Exit(2)
	}
	return nil
}

type cliTask struct {
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata"`
	Status   struct {
		State   string `json:"state"`
		Message *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	} `json:"status"`
}

func (t cliTask) State() string {
	return strings.ToLower(strings.TrimPrefix(t.Status.State, "TASK_STATE_"))
}

// String is the task as a person or a model reads it: its id and state, then
// what the session said.
func (t cliTask) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "task %s: %s\n", t.ID, strings.ReplaceAll(t.State(), "_", "-"))
	// The supervision (shrooms extension): what a quiet task is doing.
	var notes []string
	if q, _ := t.Metadata["shrooms/queued"].(bool); q {
		notes = append(notes, "queued: the session is busy and starts it when free")
	}
	if n, _ := t.Metadata["shrooms/nudges"].(float64); n > 0 {
		notes = append(notes, fmt.Sprintf("reminded %d times for going quiet", int(n)))
	}
	if s, _ := t.Metadata["shrooms/stalled"].(bool); s {
		notes = append(notes, "stalled: no progress after the reminders; its owner is told")
	}
	if a, _ := t.Metadata["shrooms/acknowledged"].(bool); a {
		notes = append(notes, "acknowledged")
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "(%s)\n", n)
	}
	if t.Status.Message != nil {
		for _, p := range t.Status.Message.Parts {
			if p.Text != "" {
				fmt.Fprintf(&b, "\n%s\n", p.Text)
			}
		}
	}
	return b.String()
}

// send asks MACHINE/SESSION; with wait, the answer is the turn's end.
func (c a2aClient) send(to, text, title string, wait bool) (cliTask, error) {
	return c.message(to, "", text, title, wait)
}

// answer is more on a task already open — what it said it needs:
// MACHINE/SESSION:MESSAGE-ID.
func (c a2aClient) answer(ref, text string, wait bool) (cliTask, error) {
	name, id, ok := strings.Cut(ref, "/")
	session, _, _ := strings.Cut(id, ":")
	if !ok || !strings.Contains(id, ":") {
		return cliTask{}, fmt.Errorf("%q: want MACHINE/SESSION:MESSAGE-ID", ref)
	}
	return c.message(name+"/"+session, id, text, "", wait)
}

func (c a2aClient) message(to, taskID, text, title string, wait bool) (cliTask, error) {
	name, session, ok := strings.Cut(to, "/")
	if !ok || session == "" {
		return cliTask{}, fmt.Errorf("%q: want MACHINE/SESSION", to)
	}
	addr, err := c.resolve(name)
	if err != nil {
		return cliTask{}, err
	}
	meta := map[string]any{}
	if me := os.Getenv("SHROOMS_AGENT_SESSION"); me != "" {
		// This machine by its mesh name, as the other side lists it.
		host, _ := os.Hostname()
		if ms, err := c.peers(); err == nil && len(ms) > 0 {
			host = ms[0].Name
		}
		meta["shrooms/from"] = host + "/" + me
	}
	if title != "" {
		meta["shrooms/title"] = title
	}
	msg := map[string]any{"messageId": newMessageID(), "role": "ROLE_USER",
		"parts": []any{map[string]any{"text": text}}, "metadata": meta}
	if taskID != "" {
		msg["taskId"] = taskID
	}
	params := map[string]any{"message": msg, "configuration": map[string]any{"blocking": wait}}
	return c.rpc(addr, "/a2a/"+session, "SendMessage", params, wait)
}

// task calls method (GetTask, CancelTask, AckTask) on MACHINE/SESSION:MESSAGE-ID.
func (c a2aClient) task(ref, method string) (cliTask, error) {
	name, id, ok := strings.Cut(ref, "/")
	if !ok || !strings.Contains(id, ":") {
		return cliTask{}, fmt.Errorf("%q: want MACHINE/SESSION:MESSAGE-ID", ref)
	}
	addr, err := c.resolve(name)
	if err != nil {
		return cliTask{}, err
	}
	return c.rpc(addr, "/a2a", method, map[string]any{"id": id}, false)
}

// update is a worker's word on a task given to its session on this machine:
// done, blocked (and what it needs) or failed.
func (c a2aClient) update(id, state, summary string) (cliTask, error) {
	ms, err := c.peers()
	if err != nil || len(ms) == 0 {
		return cliTask{}, fmt.Errorf("this machine's agent: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"state": state, "summary": summary, "session": os.Getenv("SHROOMS_AGENT_SESSION")})
	resp, err := c.http(30*time.Second).Post(c.base(ms[0].Addr)+"/v1/tasks/"+url.PathEscape(id),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return cliTask{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return cliTask{}, fmt.Errorf("task_update: %s: %.300s", resp.Status, b)
	}
	var t cliTask
	return t, json.Unmarshal(b, &t)
}

// list is every agent session on the mesh, one line each, from each
// machine's agent; machines without one, or away, are left out.
func (c a2aClient) list() (string, error) {
	ms, err := c.peers()
	if err != nil {
		return "", fmt.Errorf("the machines on the mesh: %w", err)
	}
	var mu sync.Mutex
	var lines []string
	var wg sync.WaitGroup
	hc := c.http(4 * time.Second)
	for _, m := range ms {
		wg.Add(1)
		go func(m machine) {
			defer wg.Done()
			resp, err := hc.Get(c.base(m.Addr) + "/v1/sessions")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var d struct {
				Sessions []struct {
					Name, State, Harness, Dir string
				}
			}
			if json.NewDecoder(resp.Body).Decode(&d) != nil {
				return
			}
			mu.Lock()
			for _, s := range d.Sessions {
				lines = append(lines, fmt.Sprintf("%s/%s\t%s, %s, in %s", m.Name, s.Name, s.Harness, s.State, s.Dir))
			}
			mu.Unlock()
		}(m)
	}
	wg.Wait()
	sort.Strings(lines)
	if len(lines) == 0 {
		return "no agents found on the mesh\n", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (c a2aClient) rpc(addr netip.Addr, path, method string, params any, long bool) (cliTask, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	timeout := 30 * time.Second
	if long {
		timeout = 11 * time.Minute // the agent holds a blocking send for at most 10
	}
	hc := c.http(timeout)
	resp, err := hc.Post(c.base(addr)+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return cliTask{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var out struct {
		Result struct {
			Task cliTask `json:"task"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return cliTask{}, fmt.Errorf("%s answered %s: %.200s", addr, resp.Status, b)
	}
	if out.Error != nil {
		return cliTask{}, fmt.Errorf("%s: %s (%d)", method, out.Error.Message, out.Error.Code)
	}
	return out.Result.Task, nil
}

// resolve is a machine's overlay address: given as one, or found by name
// among the machines on the mesh, or by a name the resolver knows
// ("proteus.office.mesh").
func (c a2aClient) resolve(name string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return a, nil
	}
	ms, err := c.peers()
	for _, m := range ms {
		if m.Name == name || name == "localhost" && m == ms[0] {
			return m.Addr, nil
		}
	}
	if ips, lerr := net.LookupIP(name); lerr == nil {
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip); ok && a.Is6() {
				return a, nil
			}
		}
	}
	if err != nil {
		return netip.Addr{}, fmt.Errorf("no machine %q (and the daemon's socket: %v)", name, err)
	}
	return netip.Addr{}, fmt.Errorf("no machine called %q on the mesh (shrooms-agent a2a list shows them)", name)
}

func newMessageID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "cli-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// http is a client with the timeout given, through the cage's socket when
// there is one.
func (c a2aClient) http(timeout time.Duration) *http.Client {
	hc := &http.Client{Timeout: timeout}
	if c.dial != nil {
		hc.Transport = &http.Transport{DialContext: c.dial}
	}
	return hc
}

// newProxiedClient is the client inside a cage: everything through its own
// agent's socket at path.
func newProxiedClient(path string) a2aClient {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
	c := a2aClient{sock: path, dial: dial, base: func(a netip.Addr) string {
		return "http://cage/peer/" + a.String()
	}}
	c.peers = func() ([]machine, error) {
		resp, err := c.http(10 * time.Second).Get("http://cage/machines")
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var ms []agent.Machine
		if err := json.NewDecoder(resp.Body).Decode(&ms); err != nil {
			return nil, err
		}
		out := make([]machine, len(ms))
		for i, m := range ms {
			out[i] = machine{m.Name, m.Addr}
		}
		return out, nil
	}
	return c
}

// flagsFirst moves this command's flags ahead of its words, wherever they
// were written: `a2a send pi5/jimmy "the text" --title "…"` put the title in
// the message, because Go's flags stop at the first word. Only names that
// start with "--" or "-" and are followed by a value move, except the
// booleans named, which take none; after "--" nothing moves.
func flagsFirst(args []string, booleans ...string) []string {
	isBool := func(name string) bool {
		for _, b := range booleans {
			if name == b {
				return true
			}
		}
		return false
	}
	var flags, words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			words = append(words, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || isBool(name) || i+1 >= len(args) {
			flags = append(flags, a)
			continue
		}
		flags = append(flags, a, args[i+1])
		i++
	}
	return append(flags, words...)
}
