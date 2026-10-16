package agent

// cagebreak_mounts_test.go — what a SEALED cage can actually reach on the host.
//
// The first test (cagebreak_test.go) used mounts of my own choosing, which is
// how a boundary test lies to you: it measures the mounts you thought of. This
// one calls the agent's OWN sealedMounts(), so the list under test is the list
// the agent would build, and then mounts it for real and probes through it.
//
//	SHROOMS_REAL_CAGE=1 SHROOMS_CAGE_TEST_IMAGE=<a local image> \
//	  go test ./internal/agent/ -run TestSealedCageMounts -v

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedCageMounts(t *testing.T) {
	if os.Getenv("SHROOMS_REAL_CAGE") != "1" {
		t.Skip("SHROOMS_REAL_CAGE=1 runs it")
	}
	image := os.Getenv("SHROOMS_CAGE_TEST_IMAGE")
	if image == "" {
		t.Skip("SHROOMS_CAGE_TEST_IMAGE=<a local image> runs it")
	}

	state := t.TempDir()
	m := newTestManager(t, state)
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Fatal("no podman")
	}
	c := m.Cages
	c.Memory, c.CPUs, c.Pids = "", "", 0
	// A sealed cage wants a token before it will be made; it need not be real for
	// anything here, which is what makes this test runnable without a credential.
	if err := os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("not-a-real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prof, err := m.sealedSeccomp()
	if err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	if _, err := m.CreateCaged("mountprobe", proj, "claude", &Cage{Sealed: true}); err != nil {
		t.Fatal(err)
	}
	s, ok := m.Get("mountprobe") // CreateCaged answers with an Info; sealedMounts wants the Session.
	if !ok {
		t.Fatal("no session mountprobe")
	}
	defer m.Remove("mountprobe")
	if s.cage.Outbox != "" {
		defer os.RemoveAll(s.cage.Outbox)
	}

	// The agent mounts filepath.Dir(claude) read-only, so everything in that
	// directory comes along — wider than the binary. claudeProgram resolves the
	// symlink first, so it is claude's own versions directory, and the width is
	// harmless.
	//
	// This is checked by READING, not by planting a file: the directory in
	// question is the real claude installation on a machine someone cares
	// about, and a test should not write into it even to clean up after
	// itself. An earlier version of this test planted a decoy there.
	claudeDir := ""
	if s.harness != nil {
		if bin, err := claudeProgram(m.binOf("claude")); err == nil {
			claudeDir = filepath.Dir(bin)
		}
	}

	ms, err := m.sealedMounts(s)
	if err != nil {
		t.Fatalf("the agent's own sealedMounts(): %v", err)
	}
	t.Log("the agent's own mount list for a sealed cage:")
	for _, x := range ms {
		at := x.path
		if x.at != "" {
			at = x.at
		}
		mode := "rw"
		if x.ro {
			mode = "ro"
		}
		t.Logf("  %-6s %s  ->  %s", mode, x.path, at)
	}

	name := "shrooms-mountprobe"
	_ = exec.Command(c.Podman, "rm", "-f", name).Run()
	t.Cleanup(func() { _ = exec.Command(c.Podman, "rm", "-f", name).Run() })
	extra := append([]string{"--security-opt", "seccomp=" + prof, "--label", sealedLabel + "=" + sealedGeneration},
		sealedDNSArgs()...)
	args := c.createArgs(name, "mountprobe", image, ms, extra...)
	if out, err := exec.Command(c.Podman, args...).CombinedOutput(); err != nil {
		t.Fatalf("creating the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(c.Podman, "start", name).CombinedOutput(); err != nil {
		t.Fatalf("starting the cage: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := c.closeAgentPort(t.Context(), name, true); err != nil {
		t.Fatalf("the agent's own closeAgentPort(): %v", err)
	}

	sh := func(script string) string {
		out, _ := exec.Command(c.Podman, "exec", name, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out))
	}

	// 1. The directory holding the claude binary is mounted, so anything beside
	//    it is readable. Not a bug in the mount — a fact about its width.
	if claudeDir != "" {
		want, err := os.ReadDir(claudeDir)
		if err != nil {
			t.Logf("cannot read %s from the host either: %v", claudeDir, err)
		} else {
			got := strings.Fields(sh("ls " + claudeDir))
			if len(got) != len(want) {
				t.Errorf("the cage sees %d entries of %s, the host has %d: the mount is narrower than it looks",
					len(got), claudeDir, len(want))
			} else {
				// Say what this MEASURES, and MEASURE what it says. The first version of this line
				// claimed two things: the entry count (measured just above) and that the directory
				// "is mounted read-only" — which nothing here had looked at. That is the same
				// shape as the bug this file exists for: a claim that reads as evidence. It also
				// called the directory "claude's own versions directory", true on a real machine
				// and false here, where the claude binary is the fake the test manager registers
				// and the directory is a go-build directory (both caught on review, 2026-10-15).
				//
				// The read-only claim is now read off `ms`, the list the cage was built from,
				// which is this agent's own sealedMounts() output — an observation of the
				// configuration, not a belief about sealedMounts().
				//
				// Deliberately NOT a write attempt from inside the cage: on a real machine this
				// directory is the live claude installation, so a write probe would write into it
				// exactly when the mount is NOT read-only — the test would do the damage it is
				// checking for. That is why the two other mounts (2, 3, 6) can be probed by
				// writing and this one cannot.
				found, ro := false, false
				for _, mt := range ms {
					if mt.path == claudeDir {
						found, ro = true, mt.ro
					}
				}
				switch {
				case !found:
					t.Errorf("the list the cage was built from has no mount for %s, yet the cage can read it: it is mounted some other way than this test believes", claudeDir)
				case !ro:
					t.Errorf("the list the cage was built from does not mark %s read-only: the directory holding the claude binary is writable from inside", claudeDir)
				default:
					t.Logf("the cage sees all %d entries of %s, and the list it was built from marks that directory read-only, so everything in it comes along, not just the binary",
						len(got), claudeDir)
				}
			}
		}
	}

	// 2. The socket directory: read-write or read-only? It is inside the agent's
	//    own state directory, so a write there is a write into the agent's world.
	pd := m.proxyDir(s.cage.Container)
	// Read-only since e5b2ddd: a socket to talk to, not the agent's state.
	if got := sh("touch /run/shrooms-agent/canary 2>&1 && echo WRITABLE || echo refused"); strings.Contains(got, "WRITABLE") {
		t.Errorf("/run/shrooms-agent is mounted READ-WRITE (%s) — a cage can create and delete files inside the agent's own state directory", pd)
		_ = sh("rm -f /run/shrooms-agent/canary")
	} else {
		t.Logf("/run/shrooms-agent refused a write (%s)", got)
	}
	// And the thing that actually matters there: can it remove its own socket?
	// Only asked of a socket that is there: rm -f succeeds on nothing.
	if got := sh("[ -e /run/shrooms-agent/proxy.sock ] || { echo ABSENT; exit 0; }; rm /run/shrooms-agent/proxy.sock 2>&1 && echo REMOVED || echo refused"); strings.Contains(got, "REMOVED") {
		t.Errorf("the cage can unlink its own proxy socket")
	} else if strings.Contains(got, "ABSENT") {
		t.Logf("no proxy socket in this cage to try unlinking (the test starts none)")
	}

	// 3. The project: read-write is the deal.
	if got := sh("touch " + proj + "/canary 2>&1 && echo WRITABLE || echo refused"); !strings.Contains(got, "WRITABLE") {
		t.Errorf("the project is not writable in a sealed cage: %s", got)
	}

	// 4. The outbox: writable by design. The question is WHAT can be written —
	//    the README says "untrusted text", and a symlink is not text.
	if got := sh("ln -sf /etc/hostname /outbox/canary-link 2>&1 && echo SYMLINK || echo refused"); strings.Contains(got, "SYMLINK") {
		t.Logf("NOTE: the cage can place a SYMLINK in the outbox, which the README describes as untrusted text (%s)", s.cage.Outbox)
		_ = sh("rm -f /outbox/canary-link")
	}

	// 5. The channel that needs no bug at all. The project is mounted read-write, so the cage can
	//    leave an EXECUTABLE file in it — and anything the owner's machine later runs out of that
	//    directory runs it OUTSIDE the cage. .git/hooks is the sharp case: the next git commit in
	//    that checkout runs the reviewed code on the host, as the owner.
	if got := sh("mkdir -p " + proj + "/.git/hooks && printf '#!/bin/sh\n' > " + proj + "/.git/hooks/post-checkout && chmod 755 " + proj + "/.git/hooks/post-checkout && echo PLANTED || echo refused"); strings.Contains(got, "PLANTED") {
		if st, err := os.Stat(filepath.Join(proj, ".git", "hooks", "post-checkout")); err == nil {
			t.Logf("NOTE: the cage planted an executable in the project: %s (mode %v). The project is "+
				"mounted read-write, so anything the machine later RUNS from that directory — a git hook, "+
				"a make target, direnv, an editor plugin, an npm postinstall — runs the reviewed code "+
				"outside the cage, as the owner. Sealing does not cover what happens after the cage exits.",
				filepath.Join(proj, ".git", "hooks", "post-checkout"), st.Mode())
		}
	}

	// 6. A mount meant to be read-only must refuse a write.
	if got := sh("touch " + filepath.Join(m.dir, "uploads", "mountprobe") + "/canary 2>&1 && echo WRITABLE || echo refused"); strings.Contains(got, "WRITABLE") {
		t.Errorf("the uploads mount is writable, but it is mounted read-only")
	}
}

// TestSealedCageMountsFollowTheSessionDir is the same list with the session's
// directory set to the home. Nothing about sealing constrains s.dir, so this is
// what "sealed" means when a session is made with ~ as its directory.
func TestSealedCageRefusesAHomeAsTheSessionDir(t *testing.T) {
	// The home as a sealed session's directory would be mounted read-write and
	// still called sealed. Refused since e5b2ddd (canSeal, sealableDir).
	//
	// The last case matters as much as the others: a rule that refused EVERY
	// directory would satisfy every refusal here and make sealing useless, so
	// one ordinary project directory must still be allowed.
	state := t.TempDir()
	m := newTestManager(t, state)
	if m.Cages = NewCages(""); m.Cages == nil {
		t.Skip("no podman")
	}
	if err := os.WriteFile(filepath.Join(state, SealedTokenFile), []byte("not-a-real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home to test with")
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r // what sealableDir compares, and what a link would resolve to
	}
	// A directory holding something private, wherever it is.
	private := t.TempDir()
	if err := os.MkdirAll(filepath.Join(private, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The home reached through a link, since a link is how one arrives there by accident.
	link := filepath.Join(t.TempDir(), "shortcut")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}

	for i, tc := range []struct {
		name, dir string
		refused   bool
	}{
		{"the home", home, true},
		{"the root", "/", true},
		{"an ancestor of the home", filepath.Dir(home), true},
		{"a directory holding .ssh", private, true},
		{"the home through a symlink", link, true},
		{"an ordinary project directory", t.TempDir(), false},
	} {
		// A name per case: one shared name turns a mutation into "already exists".
		in, err := m.CreateCaged(fmt.Sprintf("dircheck%d", i), tc.dir, "claude", &Cage{Sealed: true})
		if refused := err != nil; refused != tc.refused {
			t.Errorf("a sealed session in %s: refused=%v, wanted %v (%v)", tc.name, refused, tc.refused, err)
		} else if refused {
			t.Logf("refused, as it should be: %s (%v)", tc.name, err)
		} else {
			t.Logf("allowed, as it should be: %s", tc.name)
			if s, ok := m.Get(in.Name); ok {
				m.Remove(in.Name)
				if s.cage != nil && s.cage.Outbox != "" {
					os.RemoveAll(s.cage.Outbox)
				}
			}
		}
	}
}
