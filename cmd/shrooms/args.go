package main

import "flag"

// Letting flags appear on either side of a positional argument.
//
// Go's flag package stops at the first argument that is not a flag, and puts
// everything after it in Args(). So this:
//
//	shrooms set-key KEY --config /tmp/other.toml
//
// parses no flags at all. The command then runs against the DEFAULT config,
// silently, which is the worst version of being ignored: `set-key` writes a
// mesh's network key, so as root that edits the live mesh instead of the file
// named on the command line — and changing a running mesh's network key is
// leaving that mesh.
//
// Found by the two-node end-to-end test on its first CI run, which set a key on
// a second node's config and got a node that had never joined anything.
//
// `credential set` already worked around this by hand, with a comment about
// nobody remembering the rule at the moment they are pasting a long string.
// That is right, and it is not a property of that one command: nothing here has
// a positional argument that would sensibly be typed before its flags.

// splitArgs separates flags from positionals so that order does not matter.
//
// A flag that takes a value consumes the token after it, which is why this has
// to ask the FlagSet rather than guess: `--config x` is two tokens and
// `--json x` is one flag and one positional, and only the FlagSet knows which.
func splitArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		// "-" alone is a positional by convention, and "--" ends flags
		// entirely — everything after it is positional, whatever it looks like.
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		// --name=value carries its own value; --name may take the next token.
		if containsEquals(a) {
			continue
		}
		name := a[1:]
		if len(name) > 0 && name[0] == '-' {
			name = name[1:]
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown: let Parse produce the error it would have
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue // -v style, takes no value
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if len(positional) == 0 {
		return flags
	}
	// "--" between the two halves, always. Without it a positional that begins
	// with a dash — a value the user protected with their own "--" — is read
	// back as a flag, which is the thing they were protecting it from.
	return append(append(flags, "--"), positional...)
}

func containsEquals(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return true
		}
	}
	return false
}
