package state

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Revocations are kept on disk, because a revocation that a restart forgets is
// not a revocation.
//
// The list used to live only in memory: a node learned a withdrawal, dropped
// the device, and then re-admitted it the next time the daemon started. That
// made the effective lifetime of a revocation "until this machine next reboots"
// while SECURITY.md described it as lasting until the credential expired.
//
// A separate file per mesh rather than a section of state.json, for two
// reasons. Each mesh instance owns its own file, so two meshes writing at once
// cannot lose each other's entries — the failure state.json needed a merge to
// survive. And the entries are self-authenticating: each one carries the
// admin-signed revocation, verified again on load, so a corrupt or hostile file
// costs nothing that an unverified one would not.

// revocationFile is the on-disk form: the signed wire bytes, base64, and
// nothing else. Everything else about a revocation — the device, the serial —
// is inside the signature and is re-derived on load rather than trusted here.
type revocationFile struct {
	Revocations []string `json:"revocations"`
}

// revocationPath is where one mesh's withdrawals live.
func (s *State) revocationPath(networkID string) string {
	// The id is lowercase base32 by construction, but this file name is built
	// from a value that arrives as a string, and a path separator in it would
	// write somewhere else entirely.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7') {
			return r
		}
		return '-'
	}, networkID)
	if safe == "" {
		safe = "default"
	}
	return filepath.Join(s.dir, "revocations-"+safe+".json")
}

// Revocations returns the stored withdrawals for one mesh, as signed wire
// bytes. The caller verifies them: this layer does not hold the authority and
// has no business deciding what is authentic.
//
// A missing or unreadable file is not an error. It means this node has learned
// nothing yet, which is the ordinary state of a mesh where nobody has been
// removed — and a node that cannot read its list is better off starting empty
// and being re-seeded by its peers than refusing to run.
func (s *State) Revocations(networkID string) [][]byte {
	raw, err := os.ReadFile(s.revocationPath(networkID))
	if err != nil {
		return nil
	}
	var f revocationFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	out := make([][]byte, 0, len(f.Revocations))
	for _, e := range f.Revocations {
		b, err := base64.StdEncoding.DecodeString(e)
		if err != nil {
			continue // one bad entry must not cost the others
		}
		out = append(out, b)
	}
	return out
}

// SetRevocations replaces the stored withdrawals for one mesh.
//
// Written whole rather than appended: the list is small, it is derived from an
// in-memory set that already resolves duplicates and keeps the highest serial
// per device, and a rewrite cannot drift from that set the way an append can.
func (s *State) SetRevocations(networkID string, raws [][]byte) error {
	f := revocationFile{Revocations: make([]string, 0, len(raws))}
	for _, r := range raws {
		f.Revocations = append(f.Revocations, base64.StdEncoding.EncodeToString(r))
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal revocations: %w", err)
	}

	// Temp file and rename, like state.json: a torn revocation list read at the
	// next start would drop withdrawals silently, which is the whole failure
	// this file exists to prevent.
	path := s.revocationPath(networkID)
	tmpf, err := os.CreateTemp(s.dir, "revocations-*.json.tmp")
	if err != nil {
		return fmt.Errorf("write revocations: %w", err)
	}
	tmp := tmpf.Name()
	defer os.Remove(tmp) // no-op once renamed
	if _, err := tmpf.Write(append(body, '\n')); err != nil {
		tmpf.Close()
		return fmt.Errorf("write revocations: %w", err)
	}
	if err := tmpf.Chmod(0o600); err != nil {
		tmpf.Close()
		return fmt.Errorf("write revocations: %w", err)
	}
	if err := tmpf.Close(); err != nil {
		return fmt.Errorf("write revocations: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace revocations: %w", err)
	}
	return nil
}
