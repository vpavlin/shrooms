package agent

import (
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
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
create) shared=$(echo "$*" | sed -n 's/.*xyz\.vpavlin\.shrooms\.shared=\([^ ]*\).*/\1/p')
  while [ "$1" != "--name" ]; do shift; done; touch "$D/c-$2"
  [ -z "$shared" ] || echo "$shared" > "$D/c-$2.shared" ;;
volume)
  eval last=\${$#}
  case "$2" in
  inspect) [ -f "$D/v-$last" ] || exit 1; cat "$D/v-$last" ;;
  create) echo "local map[]" > "$D/v-$last" ;;
  esac ;;
rm) eval last=\${$#}; rm -f "$D/c-$last" "$D/c-$last.old" "$D/c-$last.shared" ;;
inspect)
  case "$3" in
  *Pid*) echo 4242 ;;
  *proxy*) [ -f "$D/c-$4.old" ] || echo ` + cageGeneration + ` ;;
  *sealed*) echo ` + sealedGeneration + ` ;;
  *shared*) cat "$D/c-$4.shared" 2>/dev/null ;;
  esac ;;
unshare) cat > "$D/nft-rules" ;;
build) cat > $D/Containerfile ;;
exec)
  # The gateway's forwarder, started in the background: nothing to run here.
  case "$*" in *llm-forward*) exit 0 ;; esac
  case "$*" in *if_inet6*)
    # A cage whose pasta is gone, once: loopback only.
    if [ -f "$D/nonet-$2" ]; then rm "$D/nonet-$2"; echo "00000000000000000000000000000001 01 80 10 80       lo"; exit 0; fi
    echo "fd5eca9e000100000000000000000002 02 40 00 00     eth0"; exit 0 ;;
  esac
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
	daemonSock := filepath.Join(t.TempDir(), "shrooms.sock")
	os.WriteFile(daemonSock, nil, 0o600)
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Memory: "4g", CPUs: "2", Pids: 100, Nft: "nft", Socket: daemonSock}
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
		" localhost/bench:1 sleep infinity", "--cap-drop NET_ADMIN", "--label " + proxyLabel + "=" + cageGeneration,
		// Its own agent's socket, the only way it reaches agents (ADR-044).
		"-v " + filepath.Join(state, "cages") + "/shrooms-box-"} {
		if !strings.Contains(cs[create], want) {
			t.Errorf("the container is made without %q:\n%s", want, cs[create])
		}
	}
	if sock := m.Cages.Socket; sock != "" && strings.Contains(cs[create], sock) {
		t.Errorf("the shrooms daemon's control socket is in the cage: %s", cs[create])
	}
	home, _ := os.UserHomeDir()
	if strings.Contains(cs[create], "-v "+home+":") {
		t.Errorf("the owner's whole home is in the cage: %s", cs[create])
	}
	exec := indexOf(cs, `^exec -i -w `+regexp.QuoteMeta(project)+` --env-file \S+ `+name[1]+` \S+ -p `)
	if exec < 0 || indexOf(cs, `^start `+name[1]+`$`) > exec {
		t.Fatalf("the harness is not run in the started cage, in the project: %q", cs)
	}
	// Read-only: a socket to talk to, not a directory of the agent's to write in.
	if !strings.Contains(cs[create], ".d:"+CageProxyDir+":ro ") {
		t.Errorf("the cage's socket is not mounted, read-only, at %s: %s", CageProxyDir, cs[create])
	}
	// The agent port closed inside it, after it started and before the
	// harness runs in it.
	if np := indexOf(cs, `^unshare nsenter -t 4242 -n nft -f -$`); np < 0 || np < indexOf(cs, `^start `+name[1]+`$`) || np > exec {
		t.Fatalf("the agent port is not closed in the cage before the harness runs: %q", cs)
	}

	// What the harness is given: who it is and where things are; no keys, and nothing of the machine's.
	env, _ := os.ReadFile(filepath.Join(state, "cages", name[1]+".env"))
	if strings.Contains(string(env), "sk-test") {
		t.Errorf("the agent's API key is in the cage, which reaches its model through the gateway:\n%s", env)
	}
	for _, want := range []string{"IS_SANDBOX=1\n", "SHROOMS_AGENT_SESSION=box\n", "HOME=" + home + "\n",
		"SHROOMS_AGENT_PROXY=" + CageProxyDir + "/proxy.sock\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the cage's environment lacks %q:\n%s", want, env)
		}
	}
	if !strings.Contains(string(env), "\nPATH="+cagePath(home, false)+"\n") {
		t.Errorf("the cage's PATH is not the image's:\n%s", env)
	}
	for _, not := range []string{"PATH=" + os.Getenv("PATH") + "\n", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_CODE_MESSAGING_SOCKET="} {
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
	for indexOf(calls(t, log), `^build --network host --target workbench --label `+workbenchLabel+`=`+workbenchHash()+` -t `+regexp.QuoteMeta(WorkbenchImage)+` -f - `) < 0 {
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
	// Until the build is over. Returning while it still ran left it writing
	// into the temporary directories as they were removed, and the test
	// failed in its cleanup ("directory not empty"), once in CI on 2026-11-11.
	deadline = time.Now().Add(5 * time.Second)
	for m.Cages.Status().Building {
		if time.Now().After(deadline) {
			t.Fatal("the workbench build never finished")
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

// Only what is named goes into a cage: the locale, the time zone, the
// proxies and its harness's settings; no key or token of any name, and none
// of the machine's own.
func TestWhatACageIsGiven(t *testing.T) {
	claude := cageHarnessEnv(Claude{})
	for k, want := range map[string]bool{"TZ": true, "LANG": true, "LC_ALL": true, "HTTPS_PROXY": true, "no_proxy": true,
		"ANTHROPIC_MODEL": true, "ANTHROPIC_API_KEY": false, "ANTHROPIC_AUTH_TOKEN": false, "VENICE_API_KEY": false,
		"VENICE_INFERENCE_KEY": false, "OPENROUTER_API_KEY": false, "CLAUDE_CODE_OAUTH_TOKEN": false, "GH_TOKEN": false,
		"GITHUB_TOKEN": false, "MY_SECRET": false, "SOME_NEW_THING": false, "CLAUDE_CODE_SESSION_ID": false, "PATH": false,
		"HOME": false, "SSH_AUTH_SOCK": false, "DISPLAY": false, "DBUS_SESSION_BUS_ADDRESS": false} {
		if cageEnvAllowed(k, claude) != want {
			t.Errorf("%s given to a cage: %v, want %v", k, !want, want)
		}
	}
	if cageEnvAllowed("ANTHROPIC_MODEL", cageHarnessEnv(Pi{})) {
		t.Error("Claude Code's settings go to pi's cage")
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

	// From inside: no agent is reached directly, its own is through the
	// socket (ADR-044).
	m.Machines = func() ([]Machine, error) { return []Machine{{"here", netip.MustParseAddr("::1")}}, nil }
	ctr := s.cage.Container
	direct, _ := exec.Command("podman", "exec", ctr, "sh", "-c",
		"curl -s -m 5 -o /dev/null -w '%{http_code}' http://[fd5e:ca9e:1::1]:7387/v1/sessions; echo \" $?\"").CombinedOutput()
	if strings.HasPrefix(strings.TrimSpace(string(direct)), "200") {
		t.Errorf("an agent port answers inside the cage: %s", direct)
	}
	via, _ := exec.Command("podman", "exec", ctr, "curl", "-s", "-m", "5", "--unix-socket", CageProxyDir+"/proxy.sock", "http://cage/machines").CombinedOutput()
	if !strings.Contains(string(via), `"here"`) {
		t.Errorf("the cage's socket does not answer: %s", via)
	}
}

// moveWhenIdle moves a session once its turn has ended — the reply's text
// arrives before the turn's end, and a slow machine (CI, 2026-10-08) takes a
// while to report it — asking until the move is no longer refused as busy.
func moveWhenIdle(t *testing.T, m *Manager, name string, cage *Cage) Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		in, err := m.SetCage(name, cage, "phone")
		if err == nil {
			return in
		}
		if !strings.Contains(err.Error(), "idle") || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestASessionMovesIntoACageAndOut(t *testing.T) {
	podman, log := fakePodman(t, false)
	state := t.TempDir()
	m := newTestManager(t, state)
	m.Cages = &Cages{Podman: podman, Image: WorkbenchImage}
	if _, err := m.Create("review", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("review")
	if err := s.Send("first", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return claudeType(e) == "result/success" })
	if indexOf(calls(t, log), `^exec `) >= 0 {
		t.Fatal("a session not in a cage ran in one")
	}

	// Not while it works: the turn would be cut off.
	if err := s.Send("slow", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool {
		return claudeType(e) == "assistant" && strings.Contains(assistantText(e), "slow") || s.Info().State == Working
	})
	if _, err := m.SetCage("review", &Cage{}, "phone"); err == nil || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("a working session is moved into a cage: %v", err)
	}
	s.Interrupt("phone")
	in := moveWhenIdle(t, m, "review", &Cage{Image: DesktopImage, GitHub: true})
	if in.Cage == nil || in.Cage.Image != DesktopImage || !in.Cage.GitHub || in.Running {
		t.Fatalf("the session is not in the desktop cage, stopped until the next message: %+v", in)
	}
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "caged" && strings.Contains(string(e.Data), DesktopImage) })
	if err := s.Send("second", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "second") })
	cs := calls(t, log)
	create := indexOf(cs, `^create --name shrooms-review-`)
	if create < 0 || !strings.Contains(cs[create], " "+DesktopImage+" sleep infinity") {
		t.Fatalf("the next message is not run in a desktop cage: %q", cs)
	}
	home, _ := os.UserHomeDir()
	if gh := filepath.Join(home, ".config", "gh"); isDir(gh) && !strings.Contains(cs[create], "-v "+gh+":"+gh+":ro") {
		t.Errorf("the GitHub login is not given read-only: %s", cs[create])
	}
	// The conversation goes on where it was: resumed, not started again.
	if indexOf(cs, `^exec .* --resume fake-session-1`) < 0 {
		t.Fatalf("the moved session does not resume its conversation: %q", cs)
	}
	name := regexp.MustCompile(`--name (\S+)`).FindStringSubmatch(cs[create])[1]

	// Out again: the cage is deleted, and the session runs on the machine.
	moveWhenIdle(t, m, "review", nil)
	deadline := time.Now().Add(5 * time.Second)
	for indexOf(calls(t, log), `^rm -f -t 3 `+name+`$`) < 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if indexOf(calls(t, log), `^rm -f -t 3 `+name+`$`) < 0 {
		t.Fatalf("a session taken out of its cage leaves the container: %q", calls(t, log))
	}
	if s.Info().Cage != nil {
		t.Fatal("the session is still shown caged")
	}
	execs := len(regexp.MustCompile(`(?m)^exec `).FindAllString(strings.Join(calls(t, log), "\n"), -1))
	if err := s.Send("third", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "third") })
	if n := len(regexp.MustCompile(`(?m)^exec `).FindAllString(strings.Join(calls(t, log), "\n"), -1)); n != execs {
		t.Fatal("a session out of its cage still runs in one")
	}
}

func TestACageWithNix(t *testing.T) {
	home := "/home/me"
	if p := cagePath(home, true); !strings.HasPrefix(p, "/home/me/.local/state/nix/profiles/profile/bin:") || !strings.HasSuffix(p, ":/usr/bin:/sbin:/bin") {
		t.Fatalf("a cage with nix does not find the owner's nix profile first: %s", p)
	}
	if p := cagePath(home, false); strings.Contains(p, "nix") {
		t.Fatalf("a cage without nix has it on its PATH: %s", p)
	}
	if !nixHere() {
		t.Skip("no nix here")
	}
	ms := nixMounts(home)
	var nix *mount
	for i := range ms {
		if ms[i].path == "/nix" {
			nix = &ms[i]
		}
	}
	_, daemon := os.Stat(nixDaemonSocket)
	if nix == nil || nix.ro != (daemon == nil) {
		t.Fatalf("the store is not read-only with a daemon, and the owner's to write without one: %+v", ms)
	}
}

// A cage made before cages had their own agent's socket is made again on its
// next start, and the conversation says what went with it.
func TestAnOldCageIsMadeAgain(t *testing.T) {
	podman, log := fakePodman(t, false)
	m := newTestManager(t, t.TempDir())
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft"}
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("box")
	name := s.cage.Container
	dir := filepath.Dir(log)
	os.WriteFile(filepath.Join(dir, "c-"+name), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "c-"+name+".old"), nil, 0o600)
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })
	cs := calls(t, log)
	rm, create := indexOf(cs, `^rm -f -t 3 `+name+`$`), indexOf(cs, `^create --name `+name+` `)
	if rm < 0 || create < rm {
		t.Fatalf("an old cage is not made again before it is used: %q", cs)
	}
	waitFor(t, s, 0, func(e Event) bool { return e.Kind == "caged" && strings.Contains(string(e.Data), "made again") })
}

// A cage is started in a systemd scope of its own when the agent runs under
// systemd, so restarting the agent does not kill its network; and one found
// running without its network is stopped and started again.
func TestACageKeepsItsNetwork(t *testing.T) {
	podman, log := fakePodman(t, false)
	dir := filepath.Dir(log)
	sr := filepath.Join(t.TempDir(), "systemd-run")
	os.WriteFile(sr, []byte("#!/bin/sh\necho \"systemd-run $*\" >> "+log+"\nwhile [ \"$1\" != -- ]; do shift; done; shift; exec \"$@\"\n"), 0o755)
	m := newTestManager(t, t.TempDir())
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft", SystemdRun: sr}
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("box")
	name := s.cage.Container
	os.WriteFile(filepath.Join(dir, "nonet-"+name), nil, 0o600)
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })
	cs := calls(t, log)
	scoped := indexOf(cs, `^systemd-run --user --scope .* -- \S+ start `+name+`$`)
	stop := indexOf(cs, `^stop -t 3 `+name+`$`)
	again := -1
	if stop >= 0 {
		again = indexOf(cs[stop:], `^systemd-run --user --scope .* start `+name+`$`)
	}
	if scoped < 0 || stop < scoped || again < 0 {
		t.Fatalf("the cage is not started in a scope of its own, or not started again without its network: %q", cs)
	}
}

// A cage's own limits: made with them, changed on the cage that exists (not
// made again, so what was installed stays), refused for nonsense, and "" is
// the machine's again.
func TestACagesLimitsAreItsOwn(t *testing.T) {
	podman, log := fakePodman(t, false)
	m := newTestManager(t, t.TempDir())
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Memory: "4g", CPUs: "2", Nft: "nft"}
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{Memory: "8g"}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("box")
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })
	cs := calls(t, log)
	create := indexOf(cs, `^create `)
	if create < 0 || !strings.Contains(cs[create], "--memory 8g ") || !strings.Contains(cs[create], "--cpus 2 ") {
		t.Fatalf("not made with its own memory and the machine's CPUs: %q", cs)
	}
	in, err := m.SetCageLimits("box", "16g", "1", "phone")
	if err != nil || in.Cage.Memory != "16g" || in.Cage.CPUs != "1" || in.Cage.MachineMemory != "4g" {
		t.Fatalf("set: %v %+v", err, in.Cage)
	}
	cs = calls(t, log)
	if indexOf(cs, `^update --memory 16g --cpus 1 `+s.cage.Container+`$`) < 0 || indexOf(cs[create+1:], `^(create|rm) `) >= 0 {
		t.Fatalf("the cage was not changed where it is, or was made again: %q", cs[create:])
	}
	for _, bad := range [][2]string{{"lots", ""}, {"", "-1"}, {"", "100000"}, {"16g; rm", ""}} {
		if _, err := m.SetCageLimits("box", bad[0], bad[1], "phone"); err == nil {
			t.Errorf("%q / %q taken as limits", bad[0], bad[1])
		}
	}
	if in, _ := m.SetCageLimits("box", "", "", "phone"); in.Cage.Memory != "" {
		t.Errorf("back to the machine's: %+v", in.Cage)
	}
	m2 := newTestManager(t, m.dir)
	if s2, _ := m2.Get("box"); s2 == nil || s2.cage.Memory != "" {
		t.Error("the limits set are not what is kept")
	}
}
