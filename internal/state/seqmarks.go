package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The receiver's side of replay protection, kept across restarts.
//
// A device's own sequence number is persisted — it has to be, or a restarted
// peer looks replayed to everybody. What was not persisted is the other half:
// the highest sequence each *peer* has reached, as this node saw it. That map
// was rebuilt empty on every start, and the guard accepts the first announce it
// sees from a device because it has nothing to compare against.
//
// So a restart reopened the window the guard exists to close. An announce
// captured from the bus, still inside MaxClockSkew, replayed in the seconds
// before that peer's next heartbeat, was accepted as first-seen and installed
// whatever endpoint it named. Self-healing — the next genuine announce
// supersedes it — but "monotonic per device" was not true across a restart, and
// a restart is exactly when an observer would try it.

type seqMarkFile struct {
	// Highest accepted sequence per device, keyed by hex device key. Not a
	// secret: these are counters from messages that were public on the bus.
	Marks map[string]uint64 `json:"marks"`
}

func (s *State) seqMarkPath(networkID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7') {
			return r
		}
		return '-'
	}, networkID)
	if safe == "" {
		safe = "default"
	}
	return filepath.Join(s.dir, "seqmarks-"+safe+".json")
}

// SeqMarks returns the highest sequence accepted per device for one mesh.
//
// A missing or unreadable file means "we have no history", which is what a
// first run legitimately looks like. Refusing to start over it would be worse
// than the replay window it protects: the guard degrades to its old behaviour
// rather than the node refusing to run.
func (s *State) SeqMarks(networkID string) map[string]uint64 {
	raw, err := os.ReadFile(s.seqMarkPath(networkID))
	if err != nil {
		return nil
	}
	var f seqMarkFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	return f.Marks
}

// SetSeqMarks replaces the stored marks for one mesh.
func (s *State) SetSeqMarks(networkID string, marks map[string]uint64) error {
	body, err := json.Marshal(seqMarkFile{Marks: marks})
	if err != nil {
		return fmt.Errorf("marshal sequence marks: %w", err)
	}
	if err := writeFileAtomic(s.seqMarkPath(networkID), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write sequence marks: %w", err)
	}
	return nil
}
