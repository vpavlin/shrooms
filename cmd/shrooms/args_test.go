package main

import (
	"flag"
	"testing"
)

func aFlagSet() (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cfg := fs.String("config", "/etc/shrooms/config.toml", "")
	v := fs.Bool("json", false, "")
	return fs, cfg, v
}

// The bug: Go's flag package stops at the first positional, so everything after
// it is ignored. `shrooms set-key KEY --config /tmp/x` then ran against the
// DEFAULT config, silently — which as root means editing the live mesh instead
// of the file named on the command line.
func TestFlagsAreHonouredAfterAPositional(t *testing.T) {
	fs, cfg, _ := aFlagSet()
	if err := fs.Parse(splitArgs(fs, []string{"somekey", "--config", "/tmp/x.toml"})); err != nil {
		t.Fatal(err)
	}
	if *cfg != "/tmp/x.toml" {
		t.Errorf("--config after a positional was ignored: %q", *cfg)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "somekey" {
		t.Errorf("the positional was lost: %v", fs.Args())
	}
}

// A boolean flag takes no value, so the token after it is a positional and must
// not be swallowed as one.
func TestABooleanFlagDoesNotEatTheNextArgument(t *testing.T) {
	fs, cfg, jsonOut := aFlagSet()
	if err := fs.Parse(splitArgs(fs, []string{"--json", "peername", "--config", "/tmp/y.toml"})); err != nil {
		t.Fatal(err)
	}
	if !*jsonOut {
		t.Error("--json was not set")
	}
	if *cfg != "/tmp/y.toml" {
		t.Errorf("--config was ignored: %q", *cfg)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "peername" {
		t.Errorf("the positional was eaten by --json: %v", fs.Args())
	}
}

func TestFlagOrderingCasesThatMustKeepWorking(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		cfg  string
		pos  []string
	}{
		{"flags first, as they always worked", []string{"--config", "/a", "one", "two"}, "/a", []string{"one", "two"}},
		{"name=value form carries its own value", []string{"one", "--config=/b"}, "/b", []string{"one"}},
		{"single dash is accepted too", []string{"one", "-config", "/c"}, "/c", []string{"one"}},
		{"no flags at all", []string{"one", "two"}, "/etc/shrooms/config.toml", []string{"one", "two"}},
		// -- ends flags: everything after is positional even if it looks like
		// a flag, which is how a value starting with a dash is passed.
		{"a double dash ends flag parsing", []string{"--config", "/d", "--", "--not-a-flag"}, "/d", []string{"--not-a-flag"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs, cfg, _ := aFlagSet()
			if err := fs.Parse(splitArgs(fs, c.args)); err != nil {
				t.Fatal(err)
			}
			if *cfg != c.cfg {
				t.Errorf("config is %q, want %q", *cfg, c.cfg)
			}
			if len(fs.Args()) != len(c.pos) {
				t.Fatalf("positionals are %v, want %v", fs.Args(), c.pos)
			}
			for i, p := range c.pos {
				if fs.Arg(i) != p {
					t.Errorf("positional %d is %q, want %q", i, fs.Arg(i), p)
				}
			}
		})
	}
}
