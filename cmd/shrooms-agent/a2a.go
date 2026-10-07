package main

// `shrooms-agent a2a`: an A2A client for agents that would rather run a
// command than speak JSON-RPC (docs/agents-together.md). It asks a session on
// another machine — or this one — over the mesh, and prints the reply.

import (
	"bytes"
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
	"os"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/agent"
)

const a2aUsage = `usage:
  shrooms-agent a2a send [--wait] MACHINE/SESSION TEXT   ask a session; --wait prints its reply
  shrooms-agent a2a get  MACHINE/TASK-ID                 where a task stands, and its reply
  shrooms-agent a2a cancel MACHINE/TASK-ID               interrupt it

MACHINE is a peer's name as "shrooms status" shows it, or its overlay address.
A task id is what send printed: SESSION:MESSAGE-ID.`

func a2aMain(args []string) error {
	if len(args) == 0 {
		return errors.New(a2aUsage)
	}
	fs := flag.NewFlagSet("a2a "+args[0], flag.ContinueOnError)
	wait := fs.Bool("wait", false, "wait for the turn to end and print the reply")
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket, to find machines by name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()
	switch args[0] {
	case "send":
		if len(rest) < 2 {
			return errors.New(a2aUsage)
		}
		machine, session, ok := strings.Cut(rest[0], "/")
		if !ok || session == "" {
			return fmt.Errorf("%q: want MACHINE/SESSION", rest[0])
		}
		addr, err := resolveMachine(machine, *sock)
		if err != nil {
			return err
		}
		meta := map[string]any{}
		if me := os.Getenv("SHROOMS_AGENT_SESSION"); me != "" {
			host, _ := os.Hostname()
			meta["shrooms/from"] = host + "/" + me
		}
		params := map[string]any{
			"message": map[string]any{"messageId": newMessageID(), "role": "ROLE_USER",
				"parts": []any{map[string]any{"text": strings.Join(rest[1:], " ")}}, "metadata": meta},
			"configuration": map[string]any{"blocking": *wait},
		}
		t, err := rpc(addr, "/a2a/"+session, "SendMessage", params, *wait)
		if err != nil {
			return err
		}
		return printTask(t)
	case "get", "cancel":
		if len(rest) != 1 {
			return errors.New(a2aUsage)
		}
		machine, id, ok := strings.Cut(rest[0], "/")
		if !ok || !strings.Contains(id, ":") {
			return fmt.Errorf("%q: want MACHINE/SESSION:MESSAGE-ID", rest[0])
		}
		addr, err := resolveMachine(machine, *sock)
		if err != nil {
			return err
		}
		method := "GetTask"
		if args[0] == "cancel" {
			method = "CancelTask"
		}
		t, err := rpc(addr, "/a2a", method, map[string]any{"id": id}, false)
		if err != nil {
			return err
		}
		return printTask(t)
	}
	return errors.New(a2aUsage)
}

type cliTask struct {
	ID     string `json:"id"`
	Status struct {
		State   string `json:"state"`
		Message *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	} `json:"status"`
}

func printTask(t cliTask) error {
	state := strings.ToLower(strings.TrimPrefix(t.Status.State, "TASK_STATE_"))
	fmt.Printf("task %s: %s\n", t.ID, state)
	if t.Status.Message != nil {
		for _, p := range t.Status.Message.Parts {
			if p.Text != "" {
				fmt.Println()
				fmt.Println(p.Text)
			}
		}
	}
	if state == "failed" || state == "rejected" {
		os.Exit(2)
	}
	return nil
}

func rpc(addr netip.Addr, path, method string, params any, long bool) (cliTask, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	timeout := 30 * time.Second
	if long {
		timeout = 11 * time.Minute // the agent holds a blocking send for at most 10
	}
	c := &http.Client{Timeout: timeout}
	url := "http://" + net.JoinHostPort(addr.String(), fmt.Sprint(agent.Port)) + path
	resp, err := c.Post(url, "application/json", bytes.NewReader(body))
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

// resolveMachine is a machine's overlay address: given as one, or found by
// name among this device's peers — and this device itself.
func resolveMachine(name, sock string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return a, nil
	}
	st, err := fetchStatus(sock)
	if err == nil {
		for _, p := range st.Peers {
			if p.Name == name || p.Name+"."+p.Mesh == name {
				if a, err := netip.ParseAddr(p.Overlay); err == nil {
					return a, nil
				}
			}
		}
		if host, _ := os.Hostname(); name == host || name == "localhost" {
			for _, m := range st.Meshes {
				if a, err := netip.ParseAddr(m.Overlay); err == nil {
					return a, nil
				}
			}
		}
	}
	// A mesh name the resolver knows ("proteus.office.mesh").
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
	return netip.Addr{}, fmt.Errorf("no machine called %q among this device's peers", name)
}

func newMessageID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "cli-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}
