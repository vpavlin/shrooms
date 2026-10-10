package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/agent"
)

// Files between agents (docs/agents-files.md): send_file and `a2a send-file`
// push a file to another session, which keeps it if it takes files from this
// one; `files` sets, on this machine, who a session takes them from.

const filesUsage = `usage:
  shrooms-agent files list  SESSION                      who SESSION takes files from, and who was refused
  shrooms-agent files allow SESSION MACHINE/SESSION      take files from that session (MACHINE/* for all of a machine's)
  shrooms-agent files deny  SESSION MACHINE/SESSION      stop taking them

  shrooms-agent outbox send SESSION MACHINE/SESSION      send a sealed session's whole outbox there, packed`

// sendFile pushes the file at path to MACHINE/SESSION. Where it was kept.
func (c a2aClient) sendFile(to, path, note string, tell bool) (string, error) {
	name, session, ok := strings.Cut(to, "/")
	if !ok || session == "" {
		return "", fmt.Errorf("%q: want MACHINE/SESSION", to)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.IsDir() {
		return "", fmt.Errorf("%s: not a file", path)
	} else if fi.Size() > agent.MaxDrop {
		return "", fmt.Errorf("%s is %d MB; a file may be at most %d MB", path, fi.Size()>>20, agent.MaxDrop>>20)
	}
	addr, err := c.resolve(name)
	if err != nil {
		return "", err
	}
	q := url.Values{"name": {filepath.Base(path)}}
	if note != "" {
		q.Set("note", note)
	}
	if !tell {
		q.Set("tell", "0")
	}
	req, _ := http.NewRequest(http.MethodPost, c.base(addr)+"/v1/sessions/"+url.PathEscape(session)+"/drop?"+q.Encode(), f)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(agent.FromSessionHeader, os.Getenv("SHROOMS_AGENT_SESSION"))
	resp, err := c.http(10 * time.Minute).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode/100 != 2 {
		if out.Error == "" {
			out.Error = resp.Status
		}
		return "", errors.New(out.Error)
	}
	return out.Path, nil
}

// filesMain is `shrooms-agent files`: the allow list of a session on this machine.
func filesMain(args []string) error {
	if len(args) < 2 {
		return errors.New(filesUsage)
	}
	fs := flag.NewFlagSet("files", flag.ContinueOnError)
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	if err := fs.Parse(flagsFirst(args[1:])); err != nil {
		return err
	}
	rest := fs.Args()
	c := newA2AClient(*sock)
	ms, err := c.peers()
	if err != nil || len(ms) == 0 {
		return fmt.Errorf("this machine's agent: %v", err)
	}
	base := c.base(ms[0].Addr)
	session := rest[0]
	resp, err := c.http(10 * time.Second).Get(base + "/v1/sessions")
	if err != nil {
		return err
	}
	var list struct {
		Sessions []agent.Info `json:"sessions"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	var in *agent.Info
	for i := range list.Sessions {
		if list.Sessions[i].Name == session {
			in = &list.Sessions[i]
		}
	}
	if in == nil {
		return fmt.Errorf("no session %q on this machine", session)
	}
	allowed := in.AcceptFilesFrom
	switch {
	case args[0] == "list":
		fmt.Printf("%s takes files from: %s\n", session, orNone(strings.Join(allowed, ", ")))
		for _, a := range in.FileRequests {
			fmt.Printf("  refused %s: %s (%s)\n", a.From, a.Name, a.At.Local().Format("2 Jan 15:04"))
		}
		return nil
	case args[0] == "allow" && len(rest) == 2:
		for _, a := range allowed {
			if a == rest[1] {
				return nil
			}
		}
		allowed = append(allowed, rest[1])
	case args[0] == "deny" && len(rest) == 2:
		kept := []string{}
		for _, a := range allowed {
			if a != rest[1] {
				kept = append(kept, a)
			}
		}
		allowed = kept
	default:
		return errors.New(filesUsage)
	}
	if allowed == nil {
		allowed = []string{}
	}
	body, _ := json.Marshal(map[string]any{"accept_files_from": allowed})
	resp, err = c.http(10*time.Second).Post(base+"/v1/sessions/"+url.PathEscape(session)+"/settings", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	fmt.Printf("%s takes files from: %s\n", session, orNone(strings.Join(allowed, ", ")))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "nobody"
	}
	return s
}

// outboxMain is `shrooms-agent outbox send SESSION MACHINE/SESSION`: a sealed
// session's outbox, packed by this machine's agent and sent as the session.
func outboxMain(args []string) error {
	fs := flag.NewFlagSet("outbox", flag.ContinueOnError)
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	if len(args) < 1 || args[0] != "send" {
		return errors.New(filesUsage)
	}
	if err := fs.Parse(flagsFirst(args[1:])); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return errors.New(filesUsage)
	}
	c := newA2AClient(*sock)
	ms, err := c.peers()
	if err != nil || len(ms) == 0 {
		return fmt.Errorf("this machine's agent: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"to": rest[1]})
	resp, err := c.http(6*time.Minute).Post(c.base(ms[0].Addr)+"/v1/sessions/"+url.PathEscape(rest[0])+"/outbox/send",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct{ Result, Error string }
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode/100 != 2 {
		return errors.New(out.Error)
	}
	fmt.Println(out.Result)
	return nil
}
