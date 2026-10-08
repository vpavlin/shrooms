package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakePodman is a podman that writes down what it was asked, keeps
// containers as files, and runs `exec` on the machine: the harness's program,
// in the directory and with the environment it was given — the fake claude.
// noImage: `image exists` says no.
func fakePodman(t *testing.T, noImage bool) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls")
	imageExists := "exit 0"
	if noImage {
		imageExists = "exit 1"
	}
	script := `#!/bin/sh
D=` + dir + `
echo "$*" >> $D/calls
case "$1" in
image) ` + imageExists + ` ;;
container) [ -f "$D/c-$3" ]; exit $? ;;
create) while [ "$1" != "--name" ]; do shift; done; touch "$D/c-$2" ;;
rm) rm -f "$D/c-$4" ;;
build) cat > $D/Containerfile ;;
exec)
  shift
  while :; do
    case "$1" in
    -i) shift ;;
    -w) cd "$2"; shift 2 ;;
    --env-file) set -a; . "$2"; set +a; shift 2 ;;
    *) break ;;
    esac
  done
  shift
  exec "$@" ;;
esac
exit 0
`
	bin = filepath.Join(dir, "podman")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func calls(t *testing.T, logPath string) []string {
	t.Helper()
	b, _ := os.ReadFile(logPath)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func indexOf(lines []string, re string) int {
	r := regexp.MustCompile(re)
	for i, l := range lines {
		if r.MatchString(l) {
			return i
		}
	}
	return -1
}

func TestACagedSessionRunsInItsOwnContainer(t *testing.T) {
	podman, log := fakePodman(t, false)
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Memory: "4g", CPUs: "2", Pids: 100}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	project := t.TempDir()

	in, err := m.CreateCaged("box", project, "claude", &Cage{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Cage == nil || in.Cage.Image != "localhost/bench:1" {
		t.Fatalf("the list does not say the session is caged, or in what: %+v", in.Cage)
	}
	s, _ := m.Get("box")
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })

	cs := calls(t, log)
	create := indexOf(cs, `^create `)
	if create < 0 {
		t.Fatalf("no container made: %q", cs)
	}
	name := regexp.MustCompile(`--name (shrooms-box-[0-9a-f]{6}) `).FindStringSubmatch(cs[create])
	if name == nil {
		t.Fatalf("the container is not named after the session: %s", cs[create])
	}
	for _, want := range []string{"--network " + cageNetwork, "--memory 4g", "--cpus 2", "--pids-limit 100",
		"-v " + project + ":" + project + " ", "-v " + filepath.Join(state, "uploads") + ":" + filepath.Join(state, "uploads") + ":ro",
		" localhost/bench:1 sleep infinity"} {
		if !strings.Contains(cs[create], want) {
			t.Errorf("the container is made without %q:\n%s", want, cs[create])
		}
	}
	home, _ := os.UserHomeDir()
	if strings.Contains(cs[create], "-v "+home+":") {
		t.Errorf("the owner's whole home is in the cage: %s", cs[create])
	}
	exec := indexOf(cs, `^exec -i -w `+regexp.QuoteMeta(project)+` --env-file \S+ `+name[1]+` \S+ -p `)
	if exec < 0 || indexOf(cs, `^start `+name[1]+`$`) > exec {
		t.Fatalf("the harness is not run in the started cage, in the project: %q", cs)
	}

	// What the harness is given: its keys and who it is, not the machine's.
	env, _ := os.ReadFile(filepath.Join(state, "cages", name[1]+".env"))
	for _, want := range []string{"ANTHROPIC_API_KEY=sk-test\n", "IS_SANDBOX=1\n", "SHROOMS_AGENT_SESSION=box\n", "HOME=" + home + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the cage's environment lacks %q:\n%s", want, env)
		}
	}
	for _, not := range []string{"PATH=", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_CODE_MESSAGING_SOCKET="} {
		if strings.Contains("\n"+string(env), "\n"+not) {
			t.Errorf("%s… of the machine (or of a session the agent runs under) is given to the cage:\n%s", not, env)
		}
	}

	// A restart stops the cage — and what the session left running in it —
	// before the next process starts.
	if err := s.Restart("phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "restarted" })
	deadline := time.Now().Add(5 * time.Second)
	for cs = calls(t, log); indexOf(cs[exec+1:], `^exec `) < 0 && time.Now().Before(deadline); cs = calls(t, log) {
		time.Sleep(20 * time.Millisecond)
	}
	stop := indexOf(cs, `^stop -t 3 `+name[1]+`$`)
	if stop < 0 || stop < exec || indexOf(cs[stop:], `^exec `) < 0 {
		t.Fatalf("a restart does not stop the cage, then run the harness again: %q", cs)
	}
	if indexOf(cs[create+1:], `^create `) >= 0 {
		t.Fatalf("a restart made a second container, losing what was installed: %q", cs)
	}

	// The cage is the session's: kept with it, and deleted with it.
	m2 := newTestManager(t, state)
	if s2, _ := m2.Get("box"); s2 == nil || s2.Info().Cage == nil {
		t.Fatal("a caged session is not caged after the agent restarts")
	}
	if err := m.Remove("box"); err != nil {
		t.Fatal(err)
	}
	if indexOf(calls(t, log), `^rm -f -t 3 `+name[1]+`$`) < 0 {
		t.Fatalf("deleting the session leaves its container: %q", calls(t, log))
	}
}

func TestAFirstCageBuildsTheWorkbench(t *testing.T) {
	podman, log := fakePodman(t, true)
	m := newTestManager(t, t.TempDir())
	m.Cages = &Cages{Podman: podman, Image: WorkbenchImage}
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("box")
	err := s.Send("hello", "phone")
	if err == nil || !strings.Contains(err.Error(), "being built") {
		t.Fatalf("a message before the image is there is not told it is being built: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for indexOf(calls(t, log), `^build -t `+regexp.QuoteMeta(WorkbenchImage)+` -f - `) < 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the workbench is not built: %q", calls(t, log))
		}
		time.Sleep(50 * time.Millisecond)
	}
	// From the Containerfile embedded in the agent.
	deadline = time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(log), "Containerfile"))
		if strings.Contains(string(b), "FROM ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the workbench is not built from the agent's Containerfile")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestACageIsRefusedWithoutPodman(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{}); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("a caged session is made on a machine that cannot cage it: %v", err)
	}
	if st := m.Cages.Status(); st.Available {
		t.Fatal("a machine without podman says it can cage")
	}
}

func TestWorkLeftRunningInACage(t *testing.T) {
	bin := "/home/me/.local/share/claude/versions/2.1.292"
	top := "COMMAND\nsleep infinity\n" + bin + " -p --input-format stream-json\n/usr/local/bin/shrooms-agent mcp\n"
	if busyIn(top, bin) {
		t.Fatal("the harness, its MCP server and the cage's own sleep count as work left running")
	}
	if !busyIn(top+"/bin/bash -c npm run build\n", bin) {
		t.Fatal("a command left running in the cage is not seen")
	}
}

func TestWhatACageIsGiven(t *testing.T) {
	for k, want := range map[string]bool{"ANTHROPIC_API_KEY": true, "VENICE_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"GH_TOKEN": true, "CLAUDE_CODE_SESSION_ID": false, "CLAUDE_CODE_MESSAGING_TOKEN": false, "PATH": false, "HOME": false, "SSH_AUTH_SOCK": false, "DISPLAY": false, "DBUS_SESSION_BUS_ADDRESS": false} {
		if passedToCage(k) != want {
			t.Errorf("%s given to a cage: %v, want %v", k, !want, want)
		}
	}
}

// TestARealCage runs a caged Claude Code session with the machine's podman,
// image and login — a real turn, so only when asked for:
// SHROOMS_REAL_CAGE=1 go test ./internal/agent -run TestARealCage -v
func TestARealCage(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	ctx := t.Context()
	m, err := NewManager(ctx, slog.New(slog.NewTextHandler(os.Stderr, nil)), t.TempDir(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Fatal("no podman")
	}
	project := t.TempDir()
	if _, err := m.CreateCaged("realcage", project, "claude", &Cage{}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("realcage")
	defer m.Remove("realcage")
	s.SetAutoApprove(true, "test")
	if err := s.Send("Run `id -u && apt-get update -qq && apt-get install -y -qq cowsay >/dev/null 2>&1 && echo installed > made-in-cage` with Bash, then reply with the uid only.", "test"); err != nil {
		t.Fatal(err)
	}
	backlog, ch := s.Since(0)
	defer s.Unsubscribe(ch)
	deadline := time.After(3 * time.Minute)
	for done := false; !done; {
		for _, e := range backlog {
			if claudeType(e) == "result/success" {
				done = true
			}
		}
		backlog = nil
		if done {
			break
		}
		select {
		case e := <-ch:
			backlog = []Event{e}
		case <-deadline:
			t.Fatalf("no turn ended: %+v", s.Info())
		}
	}
	st, err := os.Stat(filepath.Join(project, "made-in-cage"))
	if err != nil {
		all, c := s.Since(0)
		s.Unsubscribe(c)
		for _, e := range all {
			t.Logf("%s %s %.400s", e.Kind, e.By, e.Data)
		}
		t.Fatal("the cage did not write into the project: ", err)
	}
	// Written as root inside, owned by the owner outside.
	if uid := st.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
		t.Fatalf("a file the cage wrote belongs to uid %d, not the owner", uid)
	}
	t.Logf("preview: %q", s.Info().Preview)
}
