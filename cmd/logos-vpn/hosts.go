package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Markers delimiting the block this command owns in /etc/hosts. Everything
// outside them is left byte-for-byte alone.
const (
	hostsBegin = "# BEGIN logos-vpn — managed, do not edit inside this block"
	hostsEnd   = "# END logos-vpn"
)

// DefaultHostsFile is where entries are written.
const DefaultHostsFile = "/etc/hosts"

func cmdHosts(args []string) error {
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	suffix := fs.String("suffix", "mesh", "domain suffix; '.internal' is the formally reserved choice")
	write := fs.Bool("write", false, "update "+DefaultHostsFile+" instead of printing")
	file := fs.String("file", DefaultHostsFile, "hosts file to update")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	block := renderHosts(st, *suffix)
	if !*write {
		fmt.Print(block)
		if len(st.Peers) > 0 {
			fmt.Fprintf(os.Stderr, "\n# to apply: sudo logos-vpn hosts --write\n")
		}
		return nil
	}

	if err := updateHostsFile(*file, block); err != nil {
		return err
	}
	fmt.Printf("updated %s with %d entries\n", *file, len(st.Peers)+1)
	return nil
}

// renderHosts builds the managed block.
//
// Names are self-asserted in the announce, so two devices can claim the same
// one. Rather than silently letting the last writer win — which would send your
// ssh to the wrong machine — duplicates are disambiguated by appending a short
// piece of the overlay address, and the bare name is left to the first.
func renderHosts(st statusPayload, suffix string) string {
	suffix = strings.TrimPrefix(suffix, ".")

	type entry struct{ addr, name string }
	entries := []entry{{st.Overlay, st.Name}}
	for _, p := range st.Peers {
		entries = append(entries, entry{p.Overlay, p.Name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var b strings.Builder
	b.WriteString(hostsBegin + "\n")
	b.WriteString("# Regenerate with: logos-vpn hosts --write\n")

	seen := map[string]bool{}
	for _, e := range entries {
		name := sanitiseName(e.name)
		if name == "" {
			continue
		}
		if seen[name] {
			// Two devices claiming one name. Disambiguate rather than
			// overwrite; the addresses are what actually identify them.
			if short := shortAddr(e.addr); short != "" {
				name = name + "-" + short
			}
		}
		seen[name] = true

		if suffix != "" {
			fmt.Fprintf(&b, "%s  %s %s.%s\n", e.addr, name, name, suffix)
		} else {
			fmt.Fprintf(&b, "%s  %s\n", e.addr, name)
		}
	}
	b.WriteString(hostsEnd + "\n")
	return b.String()
}

// sanitiseName keeps a device name usable as a hostname.
func sanitiseName(s string) string {
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

// updateHostsFile replaces the managed block, leaving everything else intact.
//
// Written atomically via a temporary file in the same directory: a torn write
// to /etc/hosts would break name resolution for the whole machine, including
// whatever you would use to fix it.
func updateHostsFile(path, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := replaceBlock(string(existing), block)
	if err != nil {
		return err
	}
	if updated == string(existing) {
		return nil // already correct
	}

	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".logos-vpn-hosts-")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// replaceBlock swaps the managed block for a new one, appending it if absent.
func replaceBlock(existing, block string) (string, error) {
	begin := strings.Index(existing, hostsBegin)
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

	endIdx := strings.Index(existing[begin:], hostsEnd)
	if endIdx == -1 {
		// A truncated block would otherwise be silently duplicated, leaving two
		// conflicting sets of entries.
		return "", fmt.Errorf("found %q without a matching %q — fix the file by hand", hostsBegin, hostsEnd)
	}
	end := begin + endIdx + len(hostsEnd)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	return existing[:begin] + block + existing[end:], nil
}
