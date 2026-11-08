package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAuthClaude is a claude whose `auth` behaves as Claude Code 2.1.296's
// did without a terminal: `auth login` prints the URL, then the prompt for the
// code with no newline after it, and reads the code from its input. The code
// "good" logs it in.
func fakeAuthClaude(t *testing.T) (bin, state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "logged-in")
	bin = filepath.Join(dir, "claude")
	script := `#!/bin/sh
state='` + state + `'
case "$1 $2" in
"auth status")
  if [ -f "$state" ]; then
    echo '{"loggedIn": true, "authMethod": "claude.ai", "email": "owner@example.org"}'
  else
    echo '{"loggedIn": false, "authMethod": "none"}'; exit 1
  fi ;;
"auth login")
  echo "Opening browser to sign in…"
  echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=abc"
  printf "Paste code here if prompted > "
  read code
  if [ "$code" = good ]; then touch "$state"; echo "Login successful."; else echo "OAuth error: invalid code"; exit 1; fi ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, state
}

func loginManager(t *testing.T) (*Manager, string) {
	m := newTestManager(t, t.TempDir())
	bin, state := fakeAuthClaude(t)
	m.mu.Lock()
	m.bins["claude"] = bin
	m.mu.Unlock()
	return m, state
}

func TestClaudeLoginFromTheApp(t *testing.T) {
	m, _ := loginManager(t)
	ctx := context.Background()
	if l := m.ClaudeLogin(ctx, true); l.LoggedIn || l.Error != "" {
		t.Fatalf("before: %+v", l)
	}
	f, err := m.StartClaudeLogin("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.URL, "https://claude.com/cai/oauth/authorize") || f.State != "waiting" {
		t.Fatalf("flow: %+v", f)
	}
	if l := m.ClaudeLogin(ctx, false); l.Flow == nil || l.Flow.ID != f.ID {
		t.Fatalf("the waiting login is not shown: %+v", l)
	}
	got, err := m.ClaudeLoginCode(f.ID, "go\nod")
	if err == nil {
		t.Fatal("a code with a newline in it was taken")
	}
	got, err = m.ClaudeLoginCode(f.ID, "good")
	if err != nil || got.State != "checking" {
		t.Fatalf("handing the code in: %+v %v", got, err)
	}
	m.waitLogin()
	if got = *m.ClaudeLogin(ctx, false).Flow; got.State != "done" {
		t.Fatalf("after the code: %+v", got)
	}
	l := m.ClaudeLogin(ctx, false)
	if !l.LoggedIn || l.Method != "claude.ai" || l.Email != "owner@example.org" {
		t.Fatalf("after: %+v", l)
	}
}

func TestClaudeLoginWrongCode(t *testing.T) {
	m, _ := loginManager(t)
	f, err := m.StartClaudeLogin("phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClaudeLoginCode(f.ID, "bad"); err != nil {
		t.Fatal(err)
	}
	m.waitLogin()
	got := *m.ClaudeLogin(context.Background(), false).Flow
	if got.State != "failed" || !strings.Contains(got.Error, "invalid code") {
		t.Fatalf("a wrong code: %+v", got)
	}
	// It is over: the same id takes no second code.
	if _, err := m.ClaudeLoginCode(f.ID, "good"); err == nil {
		t.Fatal("a finished login took another code")
	}
	if l := m.ClaudeLogin(context.Background(), true); l.LoggedIn {
		t.Fatal("logged in by a wrong code")
	}
}

func TestClaudeLoginStartedAgainReplacesTheFirst(t *testing.T) {
	m, _ := loginManager(t)
	first, err := m.StartClaudeLogin("phone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartClaudeLogin("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClaudeLoginCode(first.ID, "good"); err == nil {
		t.Fatal("the replaced login took the code")
	}
	if _, err := m.ClaudeLoginCode(second.ID, "good"); err != nil {
		t.Fatal(err)
	}
	m.waitLogin()
	if got := m.ClaudeLogin(context.Background(), false); !got.LoggedIn || got.Flow == nil || got.Flow.State != "done" {
		t.Fatalf("the newer login: %+v", got)
	}
}

func TestClaudeLoginCancelled(t *testing.T) {
	m, _ := loginManager(t)
	f, err := m.StartClaudeLogin("phone")
	if err != nil {
		t.Fatal(err)
	}
	m.cancelLogin()
	if l := m.ClaudeLogin(context.Background(), false); l.Flow != nil {
		t.Fatalf("still shown after cancelling: %+v", l.Flow)
	}
	if _, err := m.ClaudeLoginCode(f.ID, "good"); err == nil {
		t.Fatal("a cancelled login took the code")
	}
}

func TestClaudeLoginNotForCages(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/claude/login"},
		{"POST", "/v1/claude/login"},
		{"POST", "/v1/claude/login/abc/code"},
		{"DELETE", "/v1/claude/login"},
	} {
		if proxyAllowed(c.method, c.path, "") {
			t.Errorf("a cage may %s %s", c.method, c.path)
		}
	}
}

func TestClaudeLoginOverHTTP(t *testing.T) {
	m, _ := loginManager(t)
	ts := localServer(Handler(m.log, m, nil))
	defer ts.Close()
	call := func(method, path, body string, out any) int {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var h struct {
		Login ClaudeLogin `json:"claude_login"`
	}
	if call("GET", "/v1/harnesses", "", &h); h.Login.LoggedIn || h.Login.Error != "" {
		t.Fatalf("harnesses' claude_login: %+v", h.Login)
	}
	var f LoginFlow
	if code := call("POST", "/v1/claude/login", "", &f); code != http.StatusOK || f.URL == "" {
		t.Fatalf("start: %d %+v", code, f)
	}
	var done struct {
		Flow  LoginFlow   `json:"flow"`
		Login ClaudeLogin `json:"login"`
	}
	if code := call("POST", "/v1/claude/login/"+f.ID+"/code", `{"code":"good"}`, &done); code != http.StatusOK || done.Flow.State != "checking" {
		t.Fatalf("code: %d %+v", code, done)
	}
	m.waitLogin()
	var now ClaudeLogin
	if call("GET", "/v1/claude/login", "", &now); !now.LoggedIn || now.Flow == nil || now.Flow.State != "done" {
		t.Fatalf("after: %+v", now)
	}
	if code := call("POST", "/v1/claude/login/"+f.ID+"/code", `{"code":"good"}`, nil); code != http.StatusConflict {
		t.Fatalf("a second code: %d", code)
	}
	if code := call("DELETE", "/v1/claude/login", "", nil); code != http.StatusNoContent {
		t.Fatalf("cancel: %d", code)
	}
}
