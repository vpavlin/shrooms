package hosts

import (
	"os"
	"sort"
	"strings"
)

// A managed block outlives the thing that wrote it, and while it does it wins.
//
// systemd-resolved answers from /etc/hosts synthetically, ahead of any resolver
// registered for the domain — so an entry written months ago by a build that no
// longer exists silently beats a correct DNS answer. Found on a real machine:
// `nothing.mesh` resolved to an address that peer had not held for weeks, while
// the mesh resolver had the right one all along.
//
// Nothing detects that today, because the file is only ever written and never
// read back. It has no expiry, and `manage_hosts` being off means the daemon
// that would have refreshed it is not looking at it either. The failure
// presents as one peer being unreachable for no visible reason, which is the
// worst kind of wrong answer: confident, specific, and stale.

// Disagreement is a managed entry that no longer matches the roster.
type Disagreement struct {
	// Name as it appears in the file.
	Name string
	// Has is the address the file gives; Wants is the current one, or empty
	// when the name is not in the mesh at all any more.
	Has, Wants string
}

// Stale reports managed entries that disagree with what the block would say if
// it were written now.
//
// Compared against rendered entries rather than against a roster directly, so
// the same name-mangling rules apply on both sides and a duplicate resolved as
// `nas-bbbb` is not reported as a stranger.
//
// A missing or unreadable file is not a disagreement: there is nothing
// shadowing anything, which is the good case.
func Stale(path string, current []Entry, suffix string) []Disagreement {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	inFile := parseBlock(string(body))
	if len(inFile) == 0 {
		return nil
	}
	want := parseBlock(Render(current, suffix))

	// Collapsed by the disagreement rather than by the name, because one
	// device appears under several — "nothing" and "nothing.mesh" are the same
	// peer having moved, and counting them twice makes a single stale entry
	// read as two problems.
	seen := map[string]int{}
	var out []Disagreement
	for _, name := range sortedKeys(inFile) {
		has := inFile[name]
		wants, known := want[name]
		if known && has == wants {
			continue
		}
		if !known {
			wants = ""
		}
		key := has + "\x00" + wants
		if i, dup := seen[key]; dup {
			// Keep the shortest name, which is the one a person would type.
			if len(name) < len(out[i].Name) {
				out[i].Name = name
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, Disagreement{Name: name, Has: has, Wants: wants})
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseBlock reads the managed block into name -> address.
//
// Only the first address for a name is kept, which is the overlay one: the
// block writes IPv6 first and IPv4 second, and comparing the family that is
// actually derived from the peer's key is what catches a moved device. An alias
// that disagreed while the overlay matched would be a bug in derivation rather
// than staleness, and is not what this is looking for.
func parseBlock(body string) map[string]string {
	begin := strings.Index(body, Begin)
	if begin < 0 {
		return nil
	}
	rest := body[begin:]
	if end := strings.Index(rest, End); end >= 0 {
		rest = rest[:end]
	}

	out := map[string]string{}
	for _, line := range strings.Split(rest, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		addr := fields[0]
		for _, name := range fields[1:] {
			if _, seen := out[name]; !seen {
				out[name] = addr
			}
		}
	}
	return out
}
