package mesh

import (
	"encoding/hex"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/topic"
)

// Announcing services (ADR-023).
//
// `services` publishes a local port under a name — immich:2283 becomes
// immich.nas.mesh (ADR-019) — and nothing tells anyone. Every node knows only
// what it publishes itself, so the roster shows your own services and never a
// peer's, and finding out what somebody offers means remembering or asking.
//
// What it discloses is smaller than it looks and not nothing. A member can
// already enumerate a peer's services by connecting to its overlay address on
// common ports; announcing buys discoverability, not access. What it adds is
// intent, in names: "immich" tells a reader what you run in a way a port scan
// does not. On a mesh of your own machines that is worth nothing; on one shared
// with other people it is an inventory. Hence per mesh, and off unless asked.

// ServicesInterval is how often a device repeats its service list.
//
// Far slower than an announce. A service list changes when somebody edits a
// config, which is roughly never, and a peer that missed one waits a few
// minutes rather than being unable to reach anything — the names still resolve
// from the roster, this only says which names are worth trying.
const ServicesInterval = 5 * time.Minute

// ServicesStale is how long a peer's list is kept after its last repeat.
//
// Three intervals, so two may be lost. Deliberately not tied to the peer being
// online: a list is a claim about what a device offers, and a device that is
// asleep still offers them.
const ServicesStale = 3 * ServicesInterval

// publishServices puts this node's service names on the mesh.
//
// Names only, and only the ones that parse — a malformed spec is already
// reported at startup, and repeating the complaint every five minutes would
// not help anyone.
func (m *Mesh) publishServices(now time.Time) error {
	if !m.cfg.AnnounceServices || m.node == nil {
		return nil
	}
	specs, err := m.cfg.ServiceSpecs()
	if err != nil || len(specs) == 0 {
		return nil
	}
	names := make([]string, 0, len(specs))
	for _, sp := range specs {
		names = append(names, sp.Name)
	}

	msg := &control.Services{
		Kind:      control.KindServices,
		DevicePub: m.st.Identity.DevicePub,
		Names:     names,
		Timestamp: now.Unix(),
	}
	// Trim rather than fail. This message is padded like every other one, and a
	// node with a long list should advertise most of it instead of vanishing
	// from the roster's service view entirely.
	for {
		sealed, err := control.Seal(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv, msg)
		if err == nil {
			_, err = m.node.Send(topic.Current(m.nk, now), sealed, true)
			return err
		}
		if len(msg.Names) <= 1 {
			return err
		}
		msg.Names = msg.Names[:len(msg.Names)-1]
	}
}

// handleServices records what a peer says it offers.
//
// Only from a device already on the roster. A roster entry is the outcome of a
// verified announce — credential checked, replay guard passed — so this reuses
// that decision rather than making a weaker one of its own. A device we have
// not admitted can still put a message on the bus; it simply goes nowhere.
func (m *Mesh) handleServices(sv *control.Services, now time.Time) {
	id := hex.EncodeToString(sv.DevicePub)
	if !m.roster.Known(id) {
		return
	}
	names := make([]string, 0, len(sv.Names))
	for _, n := range sv.Names {
		// Sanitised the same way a device name is, because these become DNS
		// labels the moment anyone displays them as service.device.mesh, and a
		// name that cannot be resolved is worse than no name.
		if s := sanitiseName(n); s != "" {
			names = append(names, s)
		}
	}

	m.mu.Lock()
	if m.services == nil {
		m.services = map[string]peerServices{}
	}
	m.services[id] = peerServices{names: names, seen: now}
	m.mu.Unlock()
}

// peerServices is one peer's claim, and when it made it.
type peerServices struct {
	names []string
	seen  time.Time
}

// Services reports what each peer says it offers, keyed by peer id.
//
// A claim, not a promise: it says what a device intends to publish, not what is
// listening. Only the publishing node knows whether the port answers, so a
// caller should present these as names to try rather than as a health display.
func (m *Mesh) Services(now time.Time) map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string][]string, len(m.services))
	for id, ps := range m.services {
		if now.Sub(ps.seen) > ServicesStale {
			continue
		}
		out[id] = append([]string(nil), ps.names...)
	}
	return out
}

// OwnServices is what this node publishes, for the same display.
func (m *Mesh) OwnServices() []string {
	specs, err := m.cfg.ServiceSpecs()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(specs))
	for _, sp := range specs {
		names = append(names, sp.Name)
	}
	return names
}
