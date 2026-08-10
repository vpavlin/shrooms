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

// Entry is one name/address pair, and the mesh it came from.
type Entry struct {
	Name string
	Addr string

	// Mesh is the local label for the mesh this peer belongs to. Empty means
	// an unlabelled single-mesh node, which renders exactly as before.
	Mesh string

	// Self marks this device's own entry, which is written differently: the
	// bare name is omitted and only the qualified form is emitted.
	//
	// Writing the bare name here shadows the machine's own hostname. A host
	// normally resolves its hostname to 127.0.1.1, and every daemon that looks
	// itself up — PAM session setup, mail, web servers — expects a local
	// address. Adding an overlay address under the same name gives them a ULA
	// instead, which RFC 6724 will often *prefer*, and which is unroutable
	// entirely until shrooms0 exists. That happens on every boot, before this
	// daemon has started.
	//
	// The qualified name is kept because it is useful and shadows nothing.
	Self bool
}

// Render builds the managed block.
//
// Two kinds of collision have to be survived, and neither may be resolved by
// last-writer-wins — that silently sends your ssh to the wrong machine.
//
// Within a mesh, device names are self-asserted in the announce, so two devices
// can claim the same one. Duplicates get a short piece of their overlay address
// appended; the bare name is left to the first.
//
// Across meshes, two meshes can each hold a "vps". Every peer therefore gets a
// qualified name — vps.home.mesh — which is unambiguous by construction. The
// short form, vps.mesh, is emitted only when that name occurs in exactly one
// mesh, so ambiguity removes the short name rather than resolving it to an
// arbitrary candidate. A single-mesh node has no ambiguity and so keeps every
// short name it has today.
func Render(entries []Entry, suffix string) string {
	suffix = strings.TrimPrefix(suffix, ".")

	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Mesh != sorted[j].Mesh {
			return sorted[i].Mesh < sorted[j].Mesh
		}
		return sorted[i].Name < sorted[j].Name
	})

	// Resolve within-mesh duplicates first, so the cross-mesh count below sees
	// the names that will actually be written.
	type resolved struct {
		name, mesh, addr string
		self             bool
	}
	var out []resolved
	seen := map[string]bool{} // mesh+name
	for _, e := range sorted {
		name := sanitise(e.Name)
		if name == "" {
			continue
		}
		key := e.Mesh + "\x00" + name
		if seen[key] {
			if short := shortAddr(e.Addr); short != "" {
				name = name + "-" + short
			}
		}
		seen[key] = true
		out = append(out, resolved{name: name, mesh: sanitise(e.Mesh), addr: e.Addr, self: e.Self})
	}

	// A bare name is only safe if exactly one entry claims it.
	claims := map[string]int{}
	for _, r := range out {
		claims[r.name]++
	}

	var b strings.Builder
	b.WriteString(Begin + "\n")
	b.WriteString("# Regenerate with: logos-vpn hosts --write\n")

	for _, r := range out {
		names := make([]string, 0, 3)
		if r.mesh != "" {
			names = append(names, r.name+"."+r.mesh)
		}
		// Never the bare name for ourselves: it would shadow the machine's own
		// hostname. See Entry.Self.
		if claims[r.name] == 1 && !r.self {
			names = append(names, r.name)
		}
		fields := make([]string, 0, len(names)*2+1)
		for _, n := range names {
			fields = append(fields, n)
			if suffix != "" {
				fields = append(fields, n+"."+suffix)
			}
		}
		// Our own entry with no mesh label has no qualified short form above,
		// so emit name.suffix directly — otherwise a single-mesh node loses its
		// own name entirely.
		if r.self && r.mesh == "" && suffix != "" {
			fields = append(fields, r.name+"."+suffix)
		}
		if len(fields) == 0 {
			// Ambiguous and unlabelled: nothing safe to write.
			continue
		}
		fmt.Fprintf(&b, "%s  %s\n", r.addr, strings.Join(fields, " "))
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
