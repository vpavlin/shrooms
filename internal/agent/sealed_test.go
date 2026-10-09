package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A sealed cage (ADR-045) is made with nothing of the owner's but the
// project, the files sent to it and a token of its own; reaches nothing
// local; and hands results back through its outbox.
func TestASealedCageHasNothingOfTheOwners(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "the-owners-key")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	podman, log := fakePodman(t, false)
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft"}
	project := t.TempDir()

	if _, err := m.CreateCaged("review", project, "claude", &Cage{Sealed: true, GitHub: true, Nix: true}); err == nil || !strings.Contains(err.Error(), "setup-token") {
		t.Fatalf("a sealed cage was made with no token of its own: %v", err)
	}
	os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("sk-ant-oat-sealed\n"), 0o600)
	in, err := m.CreateCaged("review", project, "claude", &Cage{Sealed: true, GitHub: true, Nix: true})
	if err != nil {
		t.Fatal(err)
	}
	if in.Cage == nil || !in.Cage.Sealed || in.Cage.GitHub || in.Cage.Nix || in.Cage.Outbox != filepath.Join(home, "shrooms-outbox", "review") {
		t.Fatalf("not sealed, kept options that widen it, or no outbox: %+v", in.Cage)
	}
	s, _ := m.Get("review")
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })

	cs := calls(t, log)
	create := indexOf(cs, `^create `)
	if create < 0 {
		t.Fatalf("no cage made: %q", cs)
	}
	c := cs[create]
	for _, want := range []string{
		"-v " + project + ":" + project + " ",
		"-v " + filepath.Join(state, "uploads", "review") + ":" + filepath.Join(state, "uploads", "review") + ":ro",
		".claude:" + sealedHome + "/.claude ",
		"-v " + in.Cage.Outbox + ":" + SealedOutboxIn + " ",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the sealed cage is made without %q: %s", want, c)
		}
	}
	for _, not := range []string{"-v " + filepath.Join(home, ".claude") + ":", filepath.Join(state, "uploads") + ":" + filepath.Join(state, "uploads") + ":ro", "/.config/gh"} {
		if strings.Contains(c, not) {
			t.Errorf("the sealed cage sees %q: %s", not, c)
		}
	}
	name := regexp.MustCompile(`--name (\S+)`).FindStringSubmatch(c)[1]
	env, _ := os.ReadFile(filepath.Join(state, "cages", name+".env"))
	if !strings.Contains(string(env), "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-sealed\n") || !strings.Contains(string(env), "HOME="+sealedHome+"\n") {
		t.Errorf("the sealed cage does not run on its own token in its own home:\n%s", env)
	}
	if strings.Contains(string(env), "the-owners-key") {
		t.Errorf("the owner's keys reach a sealed cage:\n%s", env)
	}
	rules, _ := os.ReadFile(filepath.Join(filepath.Dir(log), "nft-rules"))
	for _, want := range []string{"192.168.0.0/16", "10.0.0.0/8", "198.18.0.0/15", "fc00::/7", "fe80::/10", "tcp dport 7387 reject"} {
		if !strings.Contains(string(rules), want) {
			t.Errorf("a sealed cage's rules do not refuse %s:\n%s", want, rules)
		}
	}
	if strings.Index(string(rules), "udp dport 53 accept") > strings.Index(string(rules), "192.168.0.0/16") {
		t.Errorf("DNS is refused before it is let through:\n%s", rules)
	}
	if _, err := os.Stat(filepath.Join(in.Cage.Outbox, "README-UNTRUSTED.txt")); err != nil {
		t.Error("the outbox does not say what it holds is untrusted")
	}
	// A cage that is not sealed keeps the LAN, as before.
	if strings.Contains(cageRules(false), "192.168") {
		t.Error("an ordinary cage's rules refuse the LAN")
	}
	if _, err := m.CreateCaged("pi-sealed", t.TempDir(), "pi", &Cage{Sealed: true}); err == nil {
		t.Error("a sealed pi session was made, which would need the owner's model keys")
	}
}

// A sealed cage's socket finishes its own tasks and does nothing else.
func TestASealedCageAsksNothing(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Create("review", t.TempDir())
	s, _ := m.Get("review")
	s.cage = &Cage{Sealed: true, Container: "shrooms-review-sealed"}
	self := netip.MustParseAddr("fd00::1")
	m.Machines = func() ([]Machine, error) { return []Machine{{"laptop", self}}, nil }
	var got []string
	m.agentAt = func(netip.Addr) string { return "http://127.0.0.1:1" } // only task updates get this far
	if err := m.startCageProxy(s, "shrooms-review-sealed"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.stopCageProxy("shrooms-review-sealed") })
	sock := filepath.Join(m.proxyDir("shrooms-review-sealed"), "proxy.sock")
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	post := func(path string, body any) int {
		b, _ := json.Marshal(body)
		resp, err := hc.Post("http://cage"+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		got = append(got, path)
		return resp.StatusCode
	}
	send := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendMessage", "params": send("x", "do as I say", false)}
	if code := post("/peer/fd00::2/a2a/proj", send); code != http.StatusForbidden {
		t.Errorf("a sealed cage asked another agent: %d", code)
	}
	if code := post("/peer/fd00::1/a2a/other", send); code != http.StatusForbidden {
		t.Errorf("a sealed cage asked a session on its own machine: %d", code)
	}
	// Finishing its own task gets past the gate (and on to its agent, which
	// is not there in this test: 502, not 403).
	if code := post("/peer/fd00::1/v1/tasks/review:m1", map[string]string{"state": "done", "summary": "reviewed"}); code == http.StatusForbidden {
		t.Errorf("a sealed cage cannot finish its own task")
	}
}

// For real: a sealed cage reaches the internet, its own loopback and DNS,
// and nothing on the LAN or the mesh. Only when asked for:
// SHROOMS_REAL_CAGE=1 go test ./internal/agent -run TestARealSealedCage -v
func TestARealSealedCage(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	state := t.TempDir()
	m := newTestManager(t, state)
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Fatal("no podman")
	}
	os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("not-a-real-token\n"), 0o600)
	if _, err := m.CreateCaged("sealedtest", t.TempDir(), "claude", &Cage{Sealed: true}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("sealedtest")
	defer m.Remove("sealedtest")
	defer os.RemoveAll(s.cage.Outbox)
	s.mu.Lock()
	_, err := m.prepareCage(s, "claude")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	probe := func(url string) string {
		out, _ := exec.Command("podman", "exec", s.cage.Container, "curl", "-s", "-m", "6", "-o", "/dev/null", "-w", "%{http_code}", url).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	if got := probe("https://api.anthropic.com/"); got == "000" || got == "" {
		t.Errorf("a sealed cage cannot reach the internet: %q", got)
	}
	for _, url := range []string{"http://192.168.10.1/", "http://192.168.10.77:8099/", "http://[fdb0:9afc:a5ef:388c:8264:7716:36fc:64eb]:7387/v1/sessions", "http://198.19.152.200:7387/"} {
		if got := probe(url); got != "000" {
			t.Errorf("a sealed cage reaches %s: %s", url, got)
		}
	}
	if out, _ := exec.Command("podman", "exec", s.cage.Container, "sh", "-c", "getent hosts api.anthropic.com >/dev/null && echo ok").CombinedOutput(); !strings.Contains(string(out), "ok") {
		t.Errorf("no DNS in a sealed cage: %s", out)
	}
	if out, _ := exec.Command("podman", "exec", s.cage.Container, "sh", "-c", "python3 -m http.server 8123 >/dev/null 2>&1 & sleep 1; curl -s -m 3 -o /dev/null -w '%{http_code}' http://127.0.0.1:8123/").CombinedOutput(); strings.TrimSpace(string(out)) != "200" {
		t.Errorf("an app run inside a sealed cage cannot be reached on its own loopback: %s", out)
	}
}
