package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Bootstrap addresses learned from peers (ADR-031).
//
// Kept on disk because that is the only moment they can be used. Bootstrap
// addresses are consumed when the delivery node is constructed and the library
// offers no way to add one to a running node, so an address learned from an
// announce is worth nothing to the process that learned it — only to the next
// one. Which is exactly the failure this exists for: on 2026-08-20 a node that
// had known its peers for weeks restarted, found the public entry nodes
// refusing, and had nowhere to look.

// MaxBootPeers bounds the file.
//
// Small on purpose. These are tried in order at startup and each one that does
// not answer costs a dial before the node reaches one that does, so a long list
// is a slow start rather than a robust one. Six is what the public fleet ships
// and it has been enough for everybody else.
const MaxBootPeers = 6

// BootPeerTTL is how long a learned address is worth keeping.
//
// A month, because the thing it protects against is a node that has been off
// for a while coming back to a fleet that has moved. Shorter would expire the
// addresses exactly when they are most needed; longer would keep dialling
// machines that were decommissioned.
const BootPeerTTL = 30 * 24 * time.Hour

type bootPeer struct {
	Addr string    `json:"addr"`
	Seen time.Time `json:"seen"`
}

type bootPeerFile struct {
	Peers []bootPeer `json:"peers"`
}

func (s *State) bootPeerPath() string {
	return filepath.Join(s.dir, "boot-peers.json")
}

// BootPeers returns the learned addresses, most recently seen first.
//
// Expired entries are dropped on read rather than on a timer: this is consulted
// once per start, so a sweep anywhere else would be work nobody needs.
func (s *State) BootPeers(now time.Time) []string {
	raw, err := os.ReadFile(s.bootPeerPath())
	if err != nil {
		return nil
	}
	var f bootPeerFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	kept := f.Peers[:0]
	for _, p := range f.Peers {
		if p.Addr != "" && now.Sub(p.Seen) < BootPeerTTL {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Seen.After(kept[j].Seen) })
	out := make([]string, 0, len(kept))
	for _, p := range kept {
		out = append(out, p.Addr)
	}
	return out
}

// NoteBootPeer records an address a peer published.
//
// Best effort in both directions: a node that cannot write this still works and
// simply forgets, and a node that cannot read it starts from the configured
// addresses, which is where every node started before this existed.
func (s *State) NoteBootPeer(addr string, now time.Time) error {
	if addr == "" {
		return nil
	}
	raw, _ := os.ReadFile(s.bootPeerPath())
	var f bootPeerFile
	_ = json.Unmarshal(raw, &f)

	for i := range f.Peers {
		if f.Peers[i].Addr == addr {
			// Refreshing the timestamp is the point: an address we keep seeing
			// is one that keeps being true.
			f.Peers[i].Seen = now
			return s.writeBootPeers(f)
		}
	}
	f.Peers = append(f.Peers, bootPeer{Addr: addr, Seen: now})

	// Oldest out first when the list is full. A node republishes every 45
	// seconds, so anything that has not been refreshed is the least likely to
	// still answer.
	sort.Slice(f.Peers, func(i, j int) bool { return f.Peers[i].Seen.After(f.Peers[j].Seen) })
	if len(f.Peers) > MaxBootPeers {
		f.Peers = f.Peers[:MaxBootPeers]
	}
	return s.writeBootPeers(f)
}

func (s *State) writeBootPeers(f bootPeerFile) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	// 0600: not secret — every member sees these in announces — but it names
	// machines this device talks to, which is nobody else's business on a
	// shared host.
	return writeFileAtomic(s.bootPeerPath(), body, 0o600)
}
