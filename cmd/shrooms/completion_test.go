package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The completion drifts silently, so a test reads both and compares.
//
// It had gone stale in the way that is hard to notice: `keycard` and
// `services` were whole commands the shell had never heard of, and `mesh
// remove`, `mesh rename`, `config flatten` and `admin renew` were verbs it
// would not offer. Nothing fails when this happens — you just press Tab and
// get nothing, and conclude the command does not exist.
//
// Reading the source rather than a second list, because a second list is the
// thing that drifted.

var caseLabels = regexp.MustCompile(`case "([a-z][a-z0-9-]*)"(?:, "([a-z][a-z0-9-]*)")*:`)

// commandsIn pulls the case labels out of a dispatch switch.
func commandsIn(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, m := range caseLabels.FindAllStringSubmatch(string(raw), -1) {
		for _, g := range m[1:] {
			if g != "" {
				out = append(out, g)
			}
		}
	}
	return out
}

func completionScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../packaging/shrooms.bash")
	if err != nil {
		t.Fatalf("read the completion: %v", err)
	}
	return string(raw)
}

// Every top-level command must be offered on the first Tab.
func TestCompletionKnowsEveryCommand(t *testing.T) {
	script := completionScript(t)

	line := regexp.MustCompile(`local commands="([^"]*)"`).FindStringSubmatch(script)
	if line == nil {
		t.Fatal("no `local commands=` in the completion")
	}
	offered := map[string]bool{}
	for _, c := range strings.Fields(line[1]) {
		offered[c] = true
	}

	// Aliases and help spellings are not worth offering; the real ones are.
	skip := map[string]bool{"meshes": true, "help": true}
	for _, c := range commandsIn(t, "main.go") {
		if skip[c] || offered[c] {
			continue
		}
		t.Errorf("`shrooms %s` exists and the completion does not offer it", c)
	}
}

// And every subcommand must appear somewhere in the script, so that the second
// Tab is not a dead end.
func TestCompletionKnowsEverySubcommand(t *testing.T) {
	script := completionScript(t)

	for _, group := range []struct{ file, cmd string }{
		{"mesh.go", "mesh"},
		{"configcmd.go", "config"},
		{"admin.go", "admin"},
		{"keycard.go", "keycard"},
		{"services.go", "services"},
	} {
		// Aliases carry no extra meaning for a person pressing Tab, and
		// no-slots is an error tag rather than a verb.
		skip := map[string]bool{
			"ls": true, "on": true, "off": true, "rm": true, "leave": true,
			"check": true, "list": true, "help": true, "no-slots": true,
		}
		for _, sub := range commandsIn(t, group.file) {
			if skip[sub] {
				continue
			}
			if !strings.Contains(script, sub) {
				t.Errorf("`shrooms %s %s` exists and the completion never mentions it",
					group.cmd, sub)
			}
		}
	}
}

// Flags drift the same way verbs do, and worse: a flag that does not exist
// completes just as confidently as one that does.
//
// Both directions, because they fail differently. A missing flag is a Tab that
// offers less than it could. An invented one is a Tab that hands you something
// the binary rejects — which is how `--keep` got offered for a flag actually
// called `--keep-for`, alongside `--publish` and `--rotate` that were never
// offered at all. All three were hand-copied from the source into the script,
// which is the step this replaces.
func TestCompletionOffersTheFlagsThatExist(t *testing.T) {
	var src strings.Builder
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src.Write(raw)
	}

	flagDef := regexp.MustCompile(`fs\.(?:String|Bool|Uint|Uint64|Int|Duration)\("([a-z][a-z0-9-]*)"`)
	defined := map[string]bool{}
	for _, m := range flagDef.FindAllStringSubmatch(src.String(), -1) {
		defined[m[1]] = true
	}

	script := completionScript(t)
	offered := map[string]bool{}
	for _, w := range regexp.MustCompile(`compgen -W "([^"]*)"`).FindAllStringSubmatch(script, -1) {
		for _, tok := range strings.Fields(w[1]) {
			if strings.HasPrefix(tok, "--") {
				offered[strings.TrimPrefix(tok, "--")] = true
			}
		}
	}

	// Two that the regexp cannot see, rather than two that are wrong.
	//
	// --invite is matched as a literal argument in setup.go instead of being
	// declared on a FlagSet, because it decides WHICH join this is before
	// there is a FlagSet to declare it on.
	//
	// v is declared as fs.Bool("v"), which Go's flag package accepts as -v,
	// and -v is what the script offers — a single dash, so it is not picked up
	// as a long flag above.
	knownGood := map[string]bool{"invite": true, "v": true}

	for f := range offered {
		if !defined[f] && !knownGood[f] {
			t.Errorf("the completion offers --%s and no command defines it", f)
		}
	}
	for f := range defined {
		if !offered[f] && !knownGood[f] {
			t.Errorf("--%s exists and the completion never offers it", f)
		}
	}
}

// completeWith runs the real completion function and returns what it offers.
//
// The script rather than a model of it. Everything above compares two lists of
// strings, which catches drift and proves nothing about whether pressing Tab
// does anything — and the bugs here have all been in the second category.
func completeWith(t *testing.T, words ...string) []string {
	t.Helper()
	var quoted []string
	for _, w := range words {
		quoted = append(quoted, "'"+strings.ReplaceAll(w, "'", `'\''`)+"'")
	}
	script := `
source ../../packaging/shrooms.bash
COMP_WORDS=(` + strings.Join(quoted, " ") + ` "")
COMP_CWORD=` + itoa(len(words)) + `
COMPREPLY=()
_shrooms 2>/dev/null
printf '%s\n' "${COMPREPLY[@]}"
`
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("running the completion: %v", err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

func itoa(n int) string { return strconv.Itoa(n) }

func offers(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// Pressing Tab has to actually produce something.
func TestCompletionActuallyCompletes(t *testing.T) {
	for _, c := range []struct {
		want  string
		words []string
	}{
		{"keycard", []string{"shrooms"}},
		{"services", []string{"shrooms"}},
		{"free-slots", []string{"shrooms", "keycard"}},
		{"remove", []string{"shrooms", "mesh"}},
		{"flatten", []string{"shrooms", "config"}},
		{"renew", []string{"shrooms", "admin"}},
		{"--mesh", []string{"shrooms", "admin", "revoke"}},
		{"--keep-for", []string{"shrooms", "admin", "revoke"}},
		{"--keycard", []string{"shrooms", "init"}},
		// Values, not only flag names. These need no daemon: the list is here.
		{"720h", []string{"shrooms", "admin", "revoke", "--keep-for"}},
		{"Core", []string{"shrooms", "config", "set", "mode"}},
	} {
		got := completeWith(t, c.words...)
		if !offers(got, c.want) {
			t.Errorf("`%s <Tab>` did not offer %q (got %v)",
				strings.Join(c.words, " "), c.want, got)
		}
	}
}

// A flag that takes a value the machine can know should complete to it. This
// is the half that was missing: every flag NAME completed and --mesh gave
// nothing, because the helper behind it shelled out to a root-only command.
func TestFlagsWithKnowableValuesAreWired(t *testing.T) {
	script := completionScript(t)
	prev := regexp.MustCompile(`(?s)case \$prev in(.*?)\n    esac`).FindStringSubmatch(script)
	if prev == nil {
		t.Fatal("no `case $prev` block, so no flag completes to a value at all")
	}
	for _, f := range []string{
		"--mesh",     // labels, from the daemon
		"--name",     // peers, for revoke
		"--device",   // device keys, from the roster
		"--reader",   // attached readers
		"--life",     // a duration, in Go's format
		"--within",   //
		"--keep-for", //
		"--mode",     // Core or Edge
		"--config",   // paths
		"--socket",   //
		"--dir",      //
		"--file",     //
		"--sign-with",
	} {
		if !strings.Contains(prev[1], f) {
			t.Errorf("%s takes a value this machine can work out, and nothing completes it", f)
		}
	}
}
