// Package hosts renders and applies /etc/hosts entries for mesh peers.
//
// This is the zero-dependency way to reach peers by name. It is deliberately
// not the long-term answer — it is static, needs root, and does not exist on
// Android — but it works everywhere else with nothing installed. See
// docs/adr/013-name-resolution.md.
package hosts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Markers delimiting the block this package owns. Everything outside them is
// left byte-for-byte alone.
const (
	Begin = "# BEGIN logos-vpn — managed, do not edit inside this block"
	End   = "# END logos-vpn"
)

// DefaultFile is the file updated by default.
const DefaultFile = "/etc/hosts"

// Entry is one name/address pair.
type Entry struct {
	Name string
	Addr string
}

// Render builds the managed block.
//
// Names are self-asserted in the announce, so two devices can claim the same
// one. Rather than silently letting the last writer win — which would send your
// ssh to the wrong machine — duplicates are disambiguated by appending a short
// piece of the overlay address, and the bare name is left to the first.
func Render(entries []Entry, suffix string) string {
	suffix = strings.TrimPrefix(suffix, ".")

	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString(Begin + "\n")
	b.WriteString("# Regenerate with: logos-vpn hosts --write\n")

	seen := map[string]bool{}
	for _, e := range sorted {
		name := sanitise(e.Name)
		if name == "" {
			continue
		}
		if seen[name] {
			if short := shortAddr(e.Addr); short != "" {
				name = name + "-" + short
			}
		}
		seen[name] = true

		if suffix != "" {
			fmt.Fprintf(&b, "%s  %s %s.%s\n", e.Addr, name, name, suffix)
		} else {
			fmt.Fprintf(&b, "%s  %s\n", e.Addr, name)
		}
	}
	b.WriteString(End + "\n")
	return b.String()
}

// sanitise keeps a device name usable as a hostname.
func sanitise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// shortAddr returns a short distinguishing piece of an overlay address.
func shortAddr(addr string) string {
	parts := strings.Split(addr, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// Apply replaces the managed block, leaving everything else intact. It reports
// whether the file actually changed.
//
// Written atomically via a temporary file in the same directory: a torn write
// to /etc/hosts would break name resolution for the whole machine, including
// whatever you would use to fix it.
func Apply(path, block string) (changed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := ReplaceBlock(string(existing), block)
	if err != nil {
		return false, err
	}
	if updated == string(existing) {
		return false, nil
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".logos-vpn-hosts-")
	if err != nil {
		return false, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}

// ReplaceBlock swaps the managed block for a new one, appending it if absent.
func ReplaceBlock(existing, block string) (string, error) {
	begin := strings.Index(existing, Begin)
	if begin == -1 {
		out := existing
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" {
			out += "\n"
		}
		return out + block, nil
	}

	endIdx := strings.Index(existing[begin:], End)
	if endIdx == -1 {
		// A truncated block would otherwise be silently duplicated, leaving two
		// conflicting sets of entries.
		return "", fmt.Errorf("found %q without a matching %q — fix the file by hand", Begin, End)
	}
	end := begin + endIdx + len(End)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	return existing[:begin] + block + existing[end:], nil
}
