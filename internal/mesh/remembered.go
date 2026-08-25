package mesh

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/wg"
)

// Remembering the roster across a restart (docs/remembering-the-roster.md).
//
// The point is not the announce interval — a restarted node marks its announces
// Fresh and peers answer immediately rather than waiting for their own tick, so
// the roster refills in about a round trip. The point is what has to happen
// before that round trip can start: the delivery node dials, subscribes, and
// only then can anything be published. On a phone, with a cold radio, that is
// where the time goes.
//
// A remembered peer gives WireGuard an endpoint to try while all of that is
// still happening.

// ProvisionalWindow is how long a remembered peer is carried before it has to
// have proved itself.
//
// It is wg.RekeyAttemptTime, and that is the argument rather than a coincidence:
// it is exactly how long wireguard-go spends retrying a handshake before giving
// up, so the rule is "keep a remembered peer for as long as WireGuard is still
// trying to reach it". Past that, nothing is attempting the address any more
// and carrying it would be transmitting at somewhere dead — which is precisely
// what the ordinary offline rule exists to refuse.
//
// It lands on the same number from the other side: FreshWindow is also 90s, and
// that is the period in which we are actively asking peers to answer. Both
// things that could rescue a remembered peer have the same budget.
//
// Well inside OfflineAfter (3 minutes), so this window closes strictly earlier
// than the ordinary one. A narrowing, not a widening — the safer direction for
// an override to point.
const ProvisionalWindow = wg.RekeyAttemptTime

// provisional reports whether a peer may be carried despite being offline.
//
// True only for a peer restored from disk, and only for the first
// ProvisionalWindow of this process. A peer whose remembered endpoint worked
// stops needing this almost immediately: the handshake makes the tunnel live,
// and the ordinary rule keeps a peer with a live tunnel whatever the roster
// says. So this only ever carries the ones that have not connected yet, and
// only ever culls the ones that never did.
func (m *Mesh) provisional(id string, now time.Time) bool {
	if now.Sub(m.timing.started) >= ProvisionalWindow {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restored[id]
}

// confirmed marks a peer as having spoken for itself, so it is no longer
// carried on the strength of a memory.
func (m *Mesh) confirmed(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.restored, id)
}

// carry reports whether a peer should stay configured in the data plane.
//
// Three ways to earn it, and the order is the argument:
//
//   - it has announced recently, which is the ordinary case;
//   - or the tunnel to it is live, because rendezvous and the data plane fail
//     independently and a peer that has gone quiet on the bus while its tunnel
//     still works must be kept — tearing tunnels down because the fleet went
//     away is what DESIGN §2 forbids;
//   - or it was remembered from the last run and this process has only just
//     started.
//
// That third arm is the one this file exists for. It matters when the memory is
// older than OfflineAfter — a node that was off for a while — because such an
// entry comes back reading offline and would otherwise be installed nowhere.
//
// A fresher memory never reaches it. A peer seen ten seconds before a crash
// comes back reading online, because it was, and the first arm carries it
// exactly as it would have done had nothing restarted. So the window is not the
// only thing keeping remembered peers alive, and reading ProvisionalWindow
// alone would give the wrong impression of how long one survives.
//
// It is self-limiting in both directions. A remembered endpoint that still
// works produces a handshake within a round trip, and the second arm then
// carries the peer on its own merits for as long as the tunnel lives. One that
// does not is dropped when the window closes — sooner than the ordinary rule
// would have dropped it, because ProvisionalWindow is well inside OfflineAfter.
func (m *Mesh) carry(p PeerInfo, st wg.PeerStat, haveStats bool, now time.Time) bool {
	if p.Online(now) {
		return true
	}
	if haveStats && st.Live(now) {
		return true
	}
	return m.provisional(p.ID(), now)
}

// loadRememberedPeers puts the last known roster back.
//
// Every entry goes through checkMembership — the same gate an announce passes,
// not a copy of it — by handing it the credential the peer announced. That is
// why the whole credential is stored rather than the fields derived from it: a
// second implementation of "is this device still a member" would be one more
// thing to keep in step with the first, and this one has an expiry and a
// revocation check in it that SECURITY.md leans on.
//
// A consequence worth stating: a device revoked while this node was off is
// refused here only if the revocation was already known. That window exists
// today too — the revocation list is equally stale either way — and it is
// bounded by the credential's own expiry, by rotation, and by this window.
func (m *Mesh) loadRememberedPeers(now time.Time) {
	if m.st == nil {
		return
	}
	stored := m.st.RosterPeers(m.networkID, now)
	if len(stored) == 0 {
		return
	}
	restored := 0
	for _, sp := range stored {
		dev, err := base64.StdEncoding.DecodeString(sp.DevicePub)
		if err != nil || len(dev) != ed25519.PublicKeySize {
			continue
		}
		wgRaw, err := base64.StdEncoding.DecodeString(sp.WGPub)
		if err != nil || len(wgRaw) != 32 {
			continue
		}
		var credRaw []byte
		if sp.Credential != "" {
			if credRaw, err = base64.StdEncoding.DecodeString(sp.Credential); err != nil {
				continue
			}
		}
		// The same check, reached the same way. A synthetic announce carrying
		// only what checkMembership reads: it verifies the credential against
		// this mesh's authority, refuses one that names a different device or
		// tunnel key, refuses a revoked one, and records the expiry and sealing
		// key exactly as a real announce would.
		probe := &control.Announce{
			DevicePub:  dev,
			WGPub:      wgRaw,
			Credential: credRaw,
		}
		if err := m.checkMembership(probe, now); err != nil {
			m.log.Info("not remembering a peer that is no longer a member",
				"peer", hex.EncodeToString(dev)[:16], "err", err)
			continue
		}

		var wgKey identity.WGKey
		copy(wgKey[:], wgRaw)
		p := PeerInfo{
			DevicePub: append(ed25519.PublicKey(nil), dev...),
			WGPub:     wgKey,
			Name:      sp.Name,
			Endpoints: append([]string(nil), sp.Endpoints...),
			Seq:       sp.Seq,
			LastSeen:  time.Unix(sp.Seen, 0),
			Relay:     sp.Relay,
		}
		if !m.roster.Restore(p) {
			continue
		}
		m.mu.Lock()
		if m.restored == nil {
			m.restored = map[string]bool{}
		}
		m.restored[p.ID()] = true
		if m.creds == nil {
			m.creds = map[string][]byte{}
		}
		if len(credRaw) > 0 {
			m.creds[p.ID()] = credRaw
		}
		m.mu.Unlock()
		restored++
	}
	if restored > 0 {
		m.log.Info("remembered peers from the last run", "devices", restored,
			"carried_for", ProvisionalWindow)
	}
}

// saveRememberedPeers writes the roster back for the next start.
//
// Called when an announce materially changes something, which is rare: a
// heartbeat that says exactly what the last one did changes nothing and writes
// nothing. Same reasoning as the services cache.
func (m *Mesh) saveRememberedPeers() {
	if m.st == nil {
		return
	}
	peers := m.roster.Peers()
	m.mu.Lock()
	out := make([]state.RosterPeer, 0, len(peers))
	for _, p := range peers {
		out = append(out, state.RosterPeer{
			DevicePub:  base64.StdEncoding.EncodeToString(p.DevicePub),
			WGPub:      base64.StdEncoding.EncodeToString(p.WGPub[:]),
			Name:       p.Name,
			Endpoints:  p.Endpoints,
			Seq:        p.Seq,
			Seen:       p.LastSeen.Unix(),
			Relay:      p.Relay,
			Credential: base64.StdEncoding.EncodeToString(m.creds[p.ID()]),
		})
	}
	m.mu.Unlock()

	if err := m.st.SetRosterPeers(m.networkID, out); err != nil {
		m.log.Debug("could not remember the roster", "err", err)
	}
}
