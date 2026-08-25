package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Peers remembered across a restart (docs/remembering-the-roster.md).
//
// The roster is derived state — rebuilt from announces, which arrive anyway —
// so for a long time nothing kept it. What that costs is a cold start with
// nothing for the data plane to do: WireGuard has no peers until the delivery
// node has bootstrapped, published a Fresh announce and been answered, and on a
// phone that bootstrap is the slow part by a wide margin. The 45-second
// announce interval was never the bottleneck; the rendezvous plane coming up
// is.
//
// So this exists to give WireGuard something to do at t=0, while the delivery
// node is still dialling. Only one side needs to remember: WireGuard roams a
// peer's endpoint to wherever its authenticated packets arrive from, so a phone
// that remembers a server bootstraps both directions at once.
//
// The whole credential is kept with each peer rather than the few fields
// derived from it. It is not a secret — it is published in every announce — and
// holding it means a restored peer goes through the same membership check a
// fresh announce does, instead of a weaker copy that would drift from it.

// MaxRosterPeers bounds the file.
//
// Generous next to MaxBootPeers, and for a reason that inverts it: bootstrap
// addresses are tried in series, so each extra one is a dial before the node
// reaches a live one. These are installed at once and handshaked in parallel,
// so an extra entry costs a few packets rather than a slower start.
const MaxRosterPeers = 64

// RosterPeerTTL is a coarse bound on how long a peer is worth remembering.
//
// Coarse because the credential is the real gate: it carries its own expiry and
// is verified on restore, so an entry cannot outlive the membership it names.
// This is for the case where that gate does not exist — a v1 mesh with a
// network key and no authority, where nothing else would ever drop an entry —
// and to stop a decommissioned device being dialled for the rest of the year.
const RosterPeerTTL = 7 * 24 * time.Hour

// RosterPeer is one remembered peer, in the form it takes on disk.
//
// Keys are base64 like everything else in this package. None of it is secret:
// every field here was published in an announce.
type RosterPeer struct {
	DevicePub string   `json:"device_pub"`
	WGPub     string   `json:"wg_pub"`
	Name      string   `json:"name,omitempty"`
	Endpoints []string `json:"endpoints,omitempty"`
	Seq       uint64   `json:"seq"`
	Seen      int64    `json:"seen"` // unix seconds, the real LastSeen
	Relay     bool     `json:"relay,omitempty"`

	// Credential is the wire form of the membership this peer announced, or
	// empty on a mesh with no authority. Checked again on restore.
	Credential string `json:"credential,omitempty"`
}

type rosterPeerFile struct {
	Peers []RosterPeer `json:"peers"`
}

// safeNetworkName maps a network id to something usable as a filename.
//
// Base32 alphabet only, everything else to a dash: a network id reaches this
// from a config file, and a path separator in one would otherwise choose where
// the file lands.
func safeNetworkName(networkID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7') {
			return r
		}
		return '-'
	}, networkID)
	if safe == "" {
		safe = "default"
	}
	return safe
}

func (s *State) rosterPeerPath(networkID string) string {
	return filepath.Join(s.dir, "roster-"+safeNetworkName(networkID)+".json")
}

// RosterPeers returns remembered peers for one mesh, freshest first.
//
// Entries past the TTL are dropped on read rather than on a timer: this is
// consulted once per start, so a sweep anywhere else would be work nobody
// needs. A missing or unreadable file means "we remember nothing", which is
// what a first run legitimately looks like.
func (s *State) RosterPeers(networkID string, now time.Time) []RosterPeer {
	raw, err := os.ReadFile(s.rosterPeerPath(networkID))
	if err != nil {
		return nil
	}
	var f rosterPeerFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	kept := make([]RosterPeer, 0, len(f.Peers))
	for _, p := range f.Peers {
		if p.DevicePub == "" || p.WGPub == "" {
			continue
		}
		if now.Sub(time.Unix(p.Seen, 0)) > RosterPeerTTL {
			continue
		}
		kept = append(kept, p)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Seen > kept[j].Seen })
	if len(kept) > MaxRosterPeers {
		kept = kept[:MaxRosterPeers]
	}
	return kept
}

// SetRosterPeers replaces the remembered peers for one mesh.
//
// The caller's slice is copied before it is sorted. Sorting in place worked and
// silently reordered the roster snapshot the caller had just built from a
// sorted-by-name list — the sort of thing that is invisible until something
// downstream depends on the order it passed in.
func (s *State) SetRosterPeers(networkID string, peers []RosterPeer) error {
	sorted := append([]RosterPeer(nil), peers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seen > sorted[j].Seen })
	if len(sorted) > MaxRosterPeers {
		sorted = sorted[:MaxRosterPeers]
	}
	body, err := json.Marshal(rosterPeerFile{Peers: sorted})
	if err != nil {
		return fmt.Errorf("marshal remembered peers: %w", err)
	}
	if err := writeFileAtomic(s.rosterPeerPath(networkID), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write remembered peers: %w", err)
	}
	return nil
}
