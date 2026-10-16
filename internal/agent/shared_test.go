package agent

// The shared folder (ADR-057): asked for per cage, one per set of meshes,
// never in a sealed cage, and a volume someone else defined is not mounted.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A named volume has no path to stat: the filter that drops mounts which are
// not there keeps it, and still drops a path that is not there.
func TestTheSharedFolderSurvivesTheMissingMountFilter(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "not-there")
	ms := keepMounts([]mount{
		{path: sharedVolume, at: CageSharedDir, volume: true},
		{path: gone, at: "/gone"},
	})
	if len(ms) != 1 || ms[0].path != sharedVolume || !ms[0].volume {
		t.Fatalf("kept %+v, want the volume alone", ms)
	}
}

// Only a cage that asked for it gets it, read-write, and is told where it is.
func TestTheSharedFolderIsAskedFor(t *testing.T) {
	state := t.TempDir()
	m := newTestManager(t, state)
	name := "envtest"
	for _, shared := range []bool{false, true} {
		s := &Session{dir: filepath.Join(state, "session"), harness: missingHarness{name: "pi"}, cage: &Cage{Shared: shared}}
		s.label.Store(&name)
		os.MkdirAll(s.dir, 0o700)
		found := findShared(m.cageMounts(s, "pi"))
		path, err := m.cageEnv(s)
		if err != nil {
			t.Fatal(err)
		}
		env, _ := os.ReadFile(path)
		told := strings.Contains(string(env), "SHROOMS_SHARED="+CageSharedDir+"\n")
		if !shared {
			if found != nil || told {
				t.Fatalf("a cage that did not ask has the shared folder (%v) or is told of it (%v)", found, told)
			}
			continue
		}
		if found == nil || found.ro || found.at != CageSharedDir || found.path != sharedVolume {
			t.Fatalf("a cage that asked has %+v, want %s read-write at %s", found, sharedVolume, CageSharedDir)
		}
		if !told {
			t.Fatalf("a cage with the shared folder is not told where it is:\n%s", env)
		}
	}
}

// Sessions share only with sessions seen from the same meshes (ADR-049): a
// home-mesh session and an office one get different folders.
func TestTheSharedFolderIsPerMeshes(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	all, home, office := sharedVolumeFor(nil), sharedVolumeFor([]string{"home"}), sharedVolumeFor([]string{"office"})
	if all != sharedVolume || home == all || office == all || home == office {
		t.Fatalf("all %q, home %q, office %q: want three folders", all, home, office)
	}
	if sharedVolumeFor([]string{"home", "office"}) != sharedVolumeFor([]string{"office", "home"}) {
		t.Error("the same meshes in another order are another folder")
	}
	if sharedVolumeFor([]string{"a.b"}) == sharedVolumeFor([]string{"a_b"}) ||
		sharedVolumeFor([]string{"a b"}) == sharedVolumeFor([]string{"a_b"}) {
		t.Error("two meshes whose labels look alike in a volume name share a folder")
	}
	for _, v := range []string{home, sharedVolumeFor([]string{"hôme / office", "x"}), sharedVolumeFor([]string{strings.Repeat("long", 30)})} {
		if !valid.MatchString(v) || len(v) > 80 {
			t.Errorf("%q is not a volume name podman takes", v)
		}
	}
}

// Not even read-only: a sealed cage holds code nobody vouches for (ADR-045,
// "the internet and nothing local"), and the danger there is exfiltration,
// which read-only does not stop.
func TestASealedCageDoesNotGetTheSharedFolder(t *testing.T) {
	if c := newCage(&Cage{Shared: true, Sealed: true}, "x"); c.Shared {
		t.Fatal("a sealed cage keeps the shared folder it was asked with")
	}
	state := t.TempDir()
	m := newTestManager(t, state)
	if err := os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("not-a-real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "sharedtest"
	s := &Session{dir: filepath.Join(state, "session"),
		harness: missingHarness{name: "claude"},
		cage:    &Cage{Container: "sharedtest", Outbox: filepath.Join(state, "outbox"), Shared: true, Sealed: true}}
	s.label.Store(&name)
	os.MkdirAll(s.dir, 0o700)
	if s.sharedFor() != "" {
		t.Fatal("a sealed cage is given a shared folder")
	}
	ms, err := m.sealedMounts(s)
	if err != nil {
		t.Skipf("a sealed cage cannot be built here (%v)", err)
	}
	if found := findShared(ms); found != nil {
		t.Fatalf("a sealed cage mounts the shared folder: %+v", *found)
	}
}

// An empty volume is seeded from the image unless nocopy; a path mount does
// not take nocopy at all. And the options follow a colon: with a comma
// straight after the path, podman mounts at "/shared,nocopy".
func TestAVolumeMountCarriesItsModeAndNocopy(t *testing.T) {
	c := &Cages{Podman: "podman"}
	joined := strings.Join(c.createArgs("n", "s", "img", []mount{
		{path: sharedVolume, at: CageSharedDir, volume: true},
		{path: sharedVolume, at: CageSharedDir, volume: true, ro: true},
	}), " ")
	for _, want := range []string{sharedVolume + ":" + CageSharedDir + ":nocopy ", sharedVolume + ":" + CageSharedDir + ":ro,nocopy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s in:\n%s", want, joined)
		}
	}
	if plain := strings.Join(c.createArgs("n", "s", "img", []mount{{path: "/tmp/x", at: "/x", ro: true}}), " "); strings.Contains(plain, "nocopy") {
		t.Fatalf("a path mount was given nocopy, which podman does not accept:\n%s", plain)
	}
}

// With podman: the volume is made and the cage labelled with it; a cage made
// for other meshes' folder is made again when the meshes change; a volume
// someone defined as a bind is refused, not mounted.
func TestASharedCageIsMadeWithItsMeshesFolder(t *testing.T) {
	podman, log := fakePodman(t, false)
	m := newTestManager(t, t.TempDir())
	m.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft"}
	if _, err := m.CreateCaged("box", t.TempDir(), "claude", &Cage{Shared: true}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("box")
	if err := s.Send("hello", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "hello") })
	cs := calls(t, log)
	create := indexOf(cs, `^create `)
	if indexOf(cs, `^volume create .* `+sharedVolume+`$`) < 0 || create < 0 ||
		!strings.Contains(cs[create], "-v "+sharedVolume+":"+CageSharedDir+":nocopy ") ||
		!strings.Contains(cs[create], "--label "+sharedLabel+"="+sharedVolume+" ") {
		t.Fatalf("the shared cage is not made with its volume, labelled: %q", cs)
	}
	if in := s.Info(); in.Cage == nil || in.Cage.Shared != sharedVolume {
		t.Fatalf("the list does not say which folder it shares: %+v", in.Cage)
	}

	// Limited to the home mesh: stopped, and made again with home's folder.
	if err := s.SetMeshes([]string{"home"}, nil, "phone"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("again", "phone"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, 0, func(e Event) bool { return strings.Contains(assistantText(e), "again") })
	cs = calls(t, log)
	home := sharedVolumeFor([]string{"home"})
	rm := indexOf(cs[create+1:], `^rm -f -t 3 shrooms-box-`)
	again := indexOf(cs[create+1:], `^create .*-v `+regexp.QuoteMeta(home+":"+CageSharedDir+":nocopy "))
	if rm < 0 || again < 0 || again < rm {
		t.Fatalf("the cage keeps the folder of every mesh after it was limited to home: %q", cs[create+1:])
	}
	if strings.Contains(cs[create+1+again], "-v "+sharedVolume+":") {
		t.Fatalf("the home cage still has the folder of every mesh: %s", cs[create+1+again])
	}

	// A volume of that name made as a bind: refused before any cage is made.
	m2 := newTestManager(t, t.TempDir())
	m2.Cages = &Cages{Podman: podman, Image: "localhost/bench:1", Nft: "nft"}
	office := sharedVolumeFor([]string{"office"})
	os.WriteFile(filepath.Join(filepath.Dir(log), "v-"+office), []byte("local map[device:/home/you o:bind type:none]\n"), 0o600)
	if _, err := m2.CreateCaged("bound", t.TempDir(), "claude", &Cage{Shared: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := m2.Get("bound")
	b.SetMeshes([]string{"office"}, nil, "phone")
	before := len(calls(t, log))
	err := b.Send("hello", "phone")
	if err == nil {
		waitFor(t, b, 0, func(e Event) bool { return e.Kind == "error" || strings.Contains(assistantText(e), "hello") })
	}
	if indexOf(calls(t, log)[before:], `^create .*`+regexp.QuoteMeta(office)) >= 0 {
		t.Fatalf("a cage was made with a volume defined as a bind: %q", calls(t, log)[before:])
	}
}

func findShared(ms []mount) *mount {
	for i := range ms {
		if ms[i].volume {
			return &ms[i]
		}
	}
	return nil
}

// With real podman: two cages given the same folder see each other's files,
// at /shared and nowhere else; a volume defined as a bind is refused.
func TestARealSharedFolder(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	image := os.Getenv("SHROOMS_CAGE_TEST_IMAGE")
	if image == "" {
		t.Skip("SHROOMS_CAGE_TEST_IMAGE=<a local image> runs it")
	}
	c := NewCages("")
	if c == nil {
		t.Fatal("no podman")
	}
	// No resource limits, as the other three real-cage tests do (cagebreak_test.go,
	// cagebreak_mounts_test.go, cagebreak_icmp6_test.go). Without this the container
	// cannot start where the memory controller is not delegated to the user session:
	// rootless podman on pi5 fails with "crun: opening file `memory.max` for writing:
	// No such file or directory", which is a fact about the machine and not about the
	// shared folder this test is about. It was the only one of the four without it.
	c.Memory, c.CPUs, c.Pids = "", "", 0
	ctx := context.Background()
	vol := sharedVolumeFor([]string{"realtest-" + filepath.Base(t.TempDir())})
	t.Cleanup(func() { c.run(ctx, "volume", "rm", "-f", vol) })
	if err := c.ensureSharedVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureSharedVolume(ctx, vol); err != nil {
		t.Fatalf("the volume it made is refused the second time: %v", err)
	}
	run := func(script string) string {
		name := "shrooms-sharedtest-" + filepath.Base(t.TempDir())
		args := c.createArgs(name, "sharedtest", image, []mount{{path: vol, at: CageSharedDir, volume: true}})
		args = append(args[:len(args)-2], "sh", "-c", script)
		args[0] = "run"
		args = append([]string{args[0], "--rm"}, args[1:]...)
		out, err := c.run(ctx, args...)
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		return strings.TrimSpace(out)
	}
	run("echo from-one > " + CageSharedDir + "/note")
	if got := run("cat " + CageSharedDir + "/note; ls -d /shared*"); got != "from-one\n"+CageSharedDir {
		t.Fatalf("the second cage sees %q, want the first one's note at %s alone", got, CageSharedDir)
	}

	bind := sharedVolumeFor([]string{"realbind-" + filepath.Base(t.TempDir())})
	t.Cleanup(func() { c.run(ctx, "volume", "rm", "-f", bind) })
	if _, err := c.run(ctx, "volume", "create", "--opt", "type=none", "--opt", "o=bind", "--opt", "device="+t.TempDir(), bind); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureSharedVolume(ctx, bind); err == nil {
		t.Fatal("a volume defined as a bind is taken as a shared folder")
	}
}
