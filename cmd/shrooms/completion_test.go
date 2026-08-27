package main

import (
	"os"
	"regexp"
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
