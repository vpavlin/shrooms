package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Logging this machine's Claude Code in from the apps (ADR-081).
//
// A session on a machine whose Claude Code is not logged in answers every
// message with "Not logged in · Please run /login", and /login typed into it
// answers that it is not available: `claude -p` has no slash commands. So the
// agent runs the login itself, `claude auth login`, which prints a URL and
// waits on its input for the code the sign-in page shows. The app opens the
// URL and sends the code back; the agent feeds it in. The login stays in this
// machine's ~/.claude, renewed by Claude Code itself; only the one-time code
// crosses the mesh. Whether it worked is read from `claude auth status`, never
// from what the login prints, so a reworded prompt cannot break it.
//
// `claude setup-token`, for sealed cages' token, is not offered: it needs a
// terminal, and printed nothing without one (Claude Code 2.1.296). Sealed
// cages run on this login too (ADR-059).

// ClaudeLogin is where this machine's Claude Code login stands.
type ClaudeLogin struct {
	LoggedIn bool   `json:"logged_in"`
	Method   string `json:"method,omitempty"` // "claude.ai", "console", …
	Email    string `json:"email,omitempty"`
	// Error: the status could not be read (no claude, or it failed).
	Error string `json:"error,omitempty"`
	// Flow is a login started from an app and not yet finished.
	Flow *LoginFlow `json:"flow,omitempty"`
}

// LoginFlow is one `claude auth login` the agent is running.
type LoginFlow struct {
	ID      string    `json:"id"`
	URL     string    `json:"url,omitempty"`
	State   string    `json:"state"` // "waiting" (for the code), "checking", "done", "failed"
	Error   string    `json:"error,omitempty"`
	By      string    `json:"by,omitempty"`
	Started time.Time `json:"started"`
}

// loginTimeout is how long a login waits for its code, and how long a failed
// or finished one is still shown.
const loginTimeout = 10 * time.Minute

// loginURL finds the sign-in URL in what `claude auth login` prints.
var loginURL = regexp.MustCompile(`https://\S*oauth\S*`)

type logins struct {
	mu   sync.Mutex
	flow *LoginFlow
	in   io.WriteCloser
	done chan struct{} // closed when the process has ended
	stop context.CancelFunc
	// status caches `claude auth status`: running it costs a process start.
	status   ClaudeLogin
	statusAt time.Time
}

// claudeStatus runs `claude auth status --json`. Running it also renews a
// login near its end, under Claude Code's own locking (gateway.go).
func (m *Manager) claudeStatus(ctx context.Context) ClaudeLogin {
	bin := m.binOf("claude")
	if bin == "" {
		return ClaudeLogin{Error: "Claude Code is not installed on this machine"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--json").Output()
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
	}
	if jerr := json.Unmarshal(out, &st); jerr != nil {
		// Logged out, it still answers in JSON; anything else is older than
		// `claude auth` or broken.
		if err == nil {
			err = jerr
		}
		return ClaudeLogin{Error: "could not read claude auth status: " + err.Error()}
	}
	l := ClaudeLogin{LoggedIn: st.LoggedIn, Email: st.Email}
	if st.AuthMethod != "none" {
		l.Method = st.AuthMethod
	}
	return l
}

// ClaudeLogin is this machine's login, read at most every half minute unless
// fresh is set, with the login in progress if there is one.
func (m *Manager) ClaudeLogin(ctx context.Context, fresh bool) ClaudeLogin {
	m.login.mu.Lock()
	cached, at := m.login.status, m.login.statusAt
	m.login.mu.Unlock()
	if fresh || time.Since(at) > 30*time.Second {
		cached = m.claudeStatus(ctx)
		m.login.mu.Lock()
		m.login.status, m.login.statusAt = cached, time.Now()
		m.login.mu.Unlock()
	}
	m.login.mu.Lock()
	defer m.login.mu.Unlock()
	if f := m.login.flow; f != nil {
		if f.State != "waiting" && f.State != "checking" && time.Since(f.Started) > loginTimeout {
			m.login.flow = nil
		} else {
			c := *f
			cached.Flow = &c
		}
	}
	return cached
}

// StartClaudeLogin starts `claude auth login` and returns once it has printed
// the URL to sign in at. A login already waiting is replaced: whoever asks
// again has the newer URL in hand.
func (m *Manager) StartClaudeLogin(by string) (LoginFlow, error) {
	bin := m.binOf("claude")
	if bin == "" {
		return LoginFlow{}, errors.New("Claude Code is not installed on this machine")
	}
	m.cancelLogin()

	ctx, cancel := context.WithTimeout(m.ctx, loginTimeout)
	// --claudeai: the subscription, as a person logging in by hand gets by
	// default. BROWSER=true: there is no browser here to open; the URL goes
	// to the app instead.
	cmd := exec.CommandContext(ctx, bin, "auth", "login", "--claudeai")
	cmd.Env = append(cmd.Environ(), "BROWSER=true")
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return LoginFlow{}, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		return LoginFlow{}, fmt.Errorf("claude auth login: %w", err)
	}
	id := make([]byte, 8)
	rand.Read(id)
	f := &LoginFlow{ID: hex.EncodeToString(id), State: "waiting", By: by, Started: time.Now()}
	done := make(chan struct{})
	m.login.mu.Lock()
	m.login.flow, m.login.in, m.login.done, m.login.stop = f, in, done, cancel
	m.login.mu.Unlock()

	found := make(chan string, 1)
	var said strings.Builder
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		// The prompt for the code ends without a newline: split on it too.
		sc.Split(splitPrompt)
		for sc.Scan() {
			line := sc.Text()
			m.login.mu.Lock()
			if said.Len() < 4<<10 {
				said.WriteString(line + "\n")
			}
			m.login.mu.Unlock()
			if u := loginURL.FindString(line); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		pw.Close()
		cancel()
		m.finishLogin(f, err, &said)
		close(done)
	}()

	select {
	case u := <-found:
		m.login.mu.Lock()
		f.URL = u
		c := *f
		m.login.mu.Unlock()
		m.log.Info("claude login started", "by", by)
		return c, nil
	case <-done:
		m.login.mu.Lock()
		defer m.login.mu.Unlock()
		return LoginFlow{}, fmt.Errorf("claude auth login ended without a URL to sign in at: %s", strings.TrimSpace(said.String()))
	case <-time.After(15 * time.Second): // under Basecamp's 20 s
		m.cancelLogin()
		return LoginFlow{}, errors.New("claude auth login printed no URL to sign in at within 15 s")
	}
}

// splitPrompt splits on newlines, and on a "> " at the end of what has
// arrived: the prompt for the code, which waits on the same line.
func splitPrompt(data []byte, atEOF bool) (int, []byte, error) {
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if strings.HasSuffix(string(data), "> ") {
		return len(data), data, nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// finishLogin records how a login process ended: done only if
// `claude auth status` now says logged in.
func (m *Manager) finishLogin(f *LoginFlow, err error, said *strings.Builder) {
	st := m.claudeStatus(m.ctx)
	m.login.mu.Lock()
	defer m.login.mu.Unlock()
	m.login.status, m.login.statusAt = st, time.Now()
	if m.login.flow != f {
		return // replaced or cancelled
	}
	m.login.in = nil
	switch {
	case st.LoggedIn:
		f.State, f.Error = "done", ""
		m.log.Info("claude login finished", "by", f.By, "method", st.Method)
	default:
		f.State = "failed"
		f.Error = "not logged in after claude auth login"
		if last := lastSaid(said.String()); last != "" {
			f.Error += ": " + last
		} else if err != nil {
			f.Error += ": " + err.Error()
		}
		m.log.Warn("claude login failed", "by", f.By, "err", f.Error)
	}
	// Shown until it is old: Started is when the outcome's clock starts.
	f.Started = time.Now()
}

func lastSaid(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !loginURL.MatchString(l) {
			return l
		}
	}
	return ""
}

// ClaudeLoginCode hands the code from the sign-in page to the waiting login.
// It returns at once, "checking": the apps poll GET /v1/claude/login for
// "done" or "failed", as Basecamp's calls to its core give up after 20 s.
func (m *Manager) ClaudeLoginCode(id, code string) (LoginFlow, error) {
	code = strings.TrimSpace(code)
	if code == "" || strings.ContainsAny(code, "\r\n") {
		return LoginFlow{}, errors.New("no code: paste the one the sign-in page shows")
	}
	m.login.mu.Lock()
	f, in := m.login.flow, m.login.in
	if f == nil || f.ID != id {
		m.login.mu.Unlock()
		return LoginFlow{}, errors.New("that login is no longer waiting: start it again")
	}
	if f.State != "waiting" || in == nil {
		c := *f
		m.login.mu.Unlock()
		return c, fmt.Errorf("that login is %s, not waiting for a code", f.State)
	}
	f.State = "checking"
	m.login.in = nil
	c := *f
	m.login.mu.Unlock()

	_, err := io.WriteString(in, code+"\n")
	in.Close()
	if err != nil {
		m.cancelLogin()
		return LoginFlow{}, fmt.Errorf("claude auth login: %w", err)
	}
	return c, nil
}

// waitLogin waits for the login in progress to end (tests).
func (m *Manager) waitLogin() {
	m.login.mu.Lock()
	done := m.login.done
	m.login.mu.Unlock()
	if done != nil {
		<-done
	}
}

// cancelLogin stops a login in progress, if there is one.
func (m *Manager) cancelLogin() {
	m.login.mu.Lock()
	stop, done := m.login.stop, m.login.done
	if f := m.login.flow; f != nil && (f.State == "waiting" || f.State == "checking") {
		m.login.flow = nil
	}
	m.login.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// The HTTP side: GET where the login stands, POST to start one, POST the code,
// DELETE to give up. None of it is in proxyAllowed: a cage cannot log the
// machine in or out.

func (h *handler) claudeLogin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.m.ClaudeLogin(r.Context(), r.URL.Query().Get("fresh") != ""))
}

func (h *handler) claudeLoginStart(w http.ResponseWriter, r *http.Request) {
	f, err := h.m.StartClaudeLogin(h.caller(r))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *handler) claudeLoginCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	f, err := h.m.ClaudeLoginCode(r.PathValue("id"), req.Code)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flow": f, "login": h.m.ClaudeLogin(r.Context(), false)})
}

func (h *handler) claudeLoginCancel(w http.ResponseWriter, r *http.Request) {
	h.m.cancelLogin()
	w.WriteHeader(http.StatusNoContent)
}
