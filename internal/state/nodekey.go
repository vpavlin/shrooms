package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The rendezvous node's own libp2p identity, kept rather than regenerated.
//
// Without this the delivery node mints a fresh key every start — three distinct
// peer ids in six hours on one machine, measured. That is invisible until
// something names the identity, and then it is not: a bootstrap address is
// `/ip4/…/tcp/…/p2p/<peer id>` (ADR-031), so every address a node has published
// stops working the moment it restarts, and every peer holding one dials the
// right socket and is turned away by the wrong identity.
//
// It broke a laptop on 2026-08-21, whose entry_nodes had been pinned by hand to
// an address that had been correct an hour earlier.
//
// Separate from the device key on purpose. This one is a libp2p transport
// identity, visible to anybody the node connects to on a public shard, and it
// authenticates nothing about mesh membership — that is the device key's job
// (ADR-007), and conflating the two would put a mesh identity on the wire in
// front of strangers.

// NodeKeyLen is what the library asks for: "P2P node private key as 64 char
// hex string", i.e. 32 bytes.
const NodeKeyLen = 32

// NodeKey returns this node's libp2p private key as hex, creating one on first
// use.
//
// Best effort by design: a node that cannot persist a key still runs, with the
// identity the library invents for it. That is exactly today's behaviour, so
// failing here would trade a working node for a stable name.
func (s *State) NodeKey() (string, error) {
	path := filepath.Join(s.dir, "nodekey")

	if raw, err := os.ReadFile(path); err == nil {
		k := strings.TrimSpace(string(raw))
		if isNodeKey(k) {
			return k, nil
		}
		// Anything else is corrupt. Replaced rather than refused: the identity
		// is not a secret anybody else depends on, and a node that will not
		// start because of a mangled file is worse than one with a new name.
	}

	b := make([]byte, NodeKeyLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	k := hex.EncodeToString(b)
	// 0600: it is not a mesh credential, but it is the identity this node
	// answers to on the shard, and letting another local user take it over is
	// not a thing to be casual about.
	if err := writeFileAtomic(path, []byte(k+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("store the node key: %w", err)
	}
	return k, nil
}

func isNodeKey(s string) bool {
	if len(s) != NodeKeyLen*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
