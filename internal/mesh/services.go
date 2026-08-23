package mesh

import (
	"encoding/hex"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/listeners"
	"github.com/vpavlin/shrooms/internal/service"
	"github.com/vpavlin/shrooms/internal/state"
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
// Hours rather than the three intervals it started as. Deliberately not tied to
// the peer being online: a list is a claim about what a device offers, and a
// device that is asleep still offers them — so expiring it quickly bought
// nothing except a wait. What it cost was the common case, since a node that
// comes back has to be rediscovered service by service, several minutes at a
// time, and every restart — including the ones our own watchdog performs —
// started that clock again.
//
// A remembered claim is no less true than a freshly received one; it is only
// older, and the roster already says whether the device can be reached.
const ServicesStale = 2 * time.Hour

// ServicesDebounce bounds how often the list is repeated outside its own
// timer, so a burst of peers arriving cannot turn into a burst of messages.
const ServicesDebounce = 30 * time.Second

// offerServices repeats the list because somebody new turned up.
//
// Without this a device that joins between two five-minute broadcasts sees no
// services at all until the next one — which is exactly what happens after any
// reconnect, so the common experience of the feature was "it shows nothing".
// An announce already gets this treatment for the same reason (shouldReplyTo);
// this is the same courtesy for the thing that is otherwise silent for minutes.
func (m *Mesh) offerServices(now time.Time) {
	if svc, _ := m.Announcing(); !svc {
		return
	}
	m.mu.Lock()
	due := now.Sub(m.lastServices) >= ServicesDebounce
	if due {
		m.lastServices = now
	}
	m.mu.Unlock()
	if !due {
		return
	}
	if err := m.publishServices(now); err != nil {
		m.log.Debug("could not offer services to a new peer", "err", err)
	}
}

// publishServices puts this node's service names on the mesh.
//
// Names only, and only the ones that parse — a malformed spec is already
// reported at startup, and repeating the complaint every five minutes would
// not help anyone.
func (m *Mesh) publishServices(now time.Time) error {
	// Read once, under the lock: these change while this loop runs now.
	wantServices, wantBound := m.Announcing()
	if m.node == nil || (!wantServices && !wantBound) {
		return nil
	}
	m.mu.Lock()
	m.lastServices = now
	m.mu.Unlock()
	var specs []service.Spec
	if wantServices {
		var err error
		if specs, err = m.cfg.ServiceSpecs(); err != nil {
			specs = nil
		}
	}
	bound := m.boundPorts()
	if len(specs) == 0 && len(bound) == 0 {
		return nil
	}
	names := make([]string, 0, len(specs))
	var types []string
	for _, sp := range specs {
		names = append(names, sp.Name)
		if sp.Type != "" {
			types = append(types, sp.Name+"="+sp.Type)
		}
	}

	msg := &control.Services{
		Kind:      control.KindServices,
		DevicePub: m.st.Identity.DevicePub,
		Names:     names,
		Types:     types,
		Bound:     bound,
		Timestamp: now.Unix(),
	}
	// Trim rather than fail. This message is padded like every other one, and a
	// node with a long list should advertise most of it instead of vanishing
	// from the roster's service view entirely.
	for {
		sealed, err := m.keys().Seal(topic.Epoch(now), m.st.Identity.DevicePriv, msg)
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

// SetAnnounce changes whether this device's services and bound ports are listed
// for its peers, on a running mesh.
//
// Live, unlike almost everything else that comes out of the config. It is a
// boolean gating what goes into the next announcement — there is no device to
// rebuild and no session to renegotiate — so needing a restart for it was an
// oversight rather than a constraint. It was worse than that, in fact: neither
// flag appeared in needsRestart either, so a reload neither applied them nor
// said it had not.
//
// Returns whether anything changed, so a caller can skip republishing.
func (m *Mesh) SetAnnounce(services, bound bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.AnnounceServices == services && m.cfg.AnnounceBound == bound {
		return false
	}
	m.cfg.AnnounceServices = services
	m.cfg.AnnounceBound = bound
	// Forces the next tick to publish rather than waiting out the debounce: the
	// point of a switch is that something happens when you press it.
	m.lastServices = time.Time{}
	return true
}

// Announcing reports what this mesh currently discloses.
func (m *Mesh) Announcing() (services, bound bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.AnnounceServices, m.cfg.AnnounceBound
}

// Bound is what would be announced for this mesh right now, whether or not
// announcing is on — which is the list a UI needs in order to show somebody
// what they are about to disclose *before* they disclose it.
func (m *Mesh) BoundHere() []string {
	found, err := listeners.On([]netip.Addr{m.self})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(found))
	for _, l := range found {
		if l.Port == 80 || l.Port == 443 || l.Port == 53 {
			continue
		}
		out = append(out, l.Spec())
	}
	return out
}

// boundPorts is what is listening on this device's address on this mesh
// (ADR-026), as "name:port".
//
// Only this mesh's address, which is the whole safety property: a socket on ::
// is reachable from every network this device is on, and listing it here would
// claim otherwise.
func (m *Mesh) boundPorts() []string {
	if _, want := m.Announcing(); !want {
		return nil
	}
	found, err := listeners.On([]netip.Addr{m.self})
	if err != nil {
		m.log.Debug("could not read what is bound to the mesh address", "err", err)
		return nil
	}
	out := make([]string, 0, len(found))
	for _, l := range found {
		// This daemon's own ports are not services anybody offers. The name
		// router on 80 and 443 is how <service>.<device>.mesh works at all,
		// and the resolver on 53 is what makes .mesh resolve here — announcing
		// either would send a peer to our plumbing rather than to anything of
		// ours, and every peer has its own.
		if l.Port == 80 || l.Port == 443 || l.Port == 53 {
			continue
		}
		out = append(out, l.Spec())
	}
	return out
}

// handleServices records what a peer says it offers.
//
// Kept for any device that could seal the message, and filtered when it is
// read: Services returns only what a peer on the roster said, and a roster
// entry is the outcome of a verified announce — credential checked, replay
// guard passed. So the rule is the same one, applied a moment later.
//
// The moment matters. Refusing at arrival meant a list that came in before
// that peer's first announce was dropped for good, and the next repeat is five
// minutes away — which is a coin flip after every reconnect, and exactly what
// "I enabled it and see nothing from that device" looked like.
func (m *Mesh) handleServices(sv *control.Services, now time.Time) {
	id := hex.EncodeToString(sv.DevicePub)
	// Bound ports are carried through as they arrived: they are "name:port",
	// and the port is the part that matters, so sanitising them like a DNS
	// label would destroy them.
	bound := make([]string, 0, len(sv.Bound))
	for _, b := range sv.Bound {
		name, port, ok := strings.Cut(b, ":")
		if !ok || sanitiseName(name) == "" {
			continue
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			continue
		}
		bound = append(bound, sanitiseName(name)+":"+port)
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
	// Bounded, because this accepts from any holder of the network key rather
	// than only from devices already admitted. A mesh has tens of devices;
	// anything past this is somebody being tiresome, and dropping their claim
	// costs nothing since it would never be displayed anyway.
	//
	// Accepting here and filtering in Services() is deliberate, not an omission
	// - a list that arrives before its peer's first announce has to survive to
	// be shown when the announce lands, or a reconnect shows nothing for five
	// minutes. The membership check is real and lives at the point of display:
	// Services() shows only devices on the roster, and revocation Forgets them.
	// A gate here instead would drop the early list on every mesh that has an
	// authority, which is the case the storing exists for.
	if _, known := m.services[id]; !known && len(m.services) >= maxServiceClaims {
		m.mu.Unlock()
		return
	}
	changed := !sameNames(m.services[id].names, names) ||
		!sameNames(m.services[id].bound, bound)
	types := map[string]string{}
	for _, e := range sv.Types {
		name, typ, ok := strings.Cut(e, "=")
		// Both halves sanitised the way they will be used: the name becomes a
		// DNS label, and the type is checked against the rules that make it
		// registerable at all. A peer claiming a type nobody could register is
		// claiming a type nobody else will ever match.
		if !ok {
			continue
		}
		name, typ = sanitiseName(name), strings.ToLower(strings.TrimSpace(typ))
		if name == "" || service.ValidType(typ) != nil {
			continue
		}
		types[name] = typ
	}
	m.services[id] = peerServices{names: names, bound: bound, types: types, seen: now}
	m.mu.Unlock()

	if changed {
		m.saveServices()
	}
}

// sameNames reports whether two claims say the same thing, so an unchanged
// repeat every few minutes does not rewrite the state file.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// maxServiceClaims caps how many devices' lists are held at once.
const maxServiceClaims = 64

// peerServices is one peer's claim, and when it made it.
type peerServices struct {
	names []string
	bound []string
	// types maps a service name to what it is: "backup" -> "logos-storage".
	types map[string]string
	seen  time.Time
}

// loadServices takes the cache back from disk at startup.
//
// The whole point of persisting it: a daemon that restarts — because it was
// upgraded, or because a watchdog decided the rendezvous plane was beyond
// repair — otherwise knows nothing about what anybody offers until each peer's
// next broadcast, which is up to five minutes each and looks like the feature
// having quietly stopped working.
func (m *Mesh) loadServices(now time.Time) {
	if m.st == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.services == nil {
		m.services = map[string]peerServices{}
	}
	kept := 0
	for id, c := range m.st.Services {
		seen := time.Unix(c.Seen, 0)
		if now.Sub(seen) > ServicesStale {
			continue
		}
		m.services[id] = peerServices{names: c.Names, bound: c.Bound, seen: seen}
		kept++
	}
	if kept > 0 {
		m.log.Info("remembered what peers offer", "devices", kept)
	}
}

// saveServices writes the cache back, so the next start has it.
//
// Called after a change rather than on a timer, because the change is rare —
// a list arrives every few minutes per peer and almost always says exactly
// what the last one did.
func (m *Mesh) saveServices() {
	// A mesh with nowhere to write is not an error worth reporting: the claim
	// is already held in memory, which is where it is read from, and only the
	// surviving-a-restart part is lost.
	if m.st == nil {
		return
	}
	m.mu.Lock()
	out := make(map[string]state.ServiceClaim, len(m.services))
	for id, ps := range m.services {
		out[id] = state.ServiceClaim{Names: ps.names, Bound: ps.bound, Seen: ps.seen.Unix()}
	}
	m.mu.Unlock()

	m.st.Services = out
	if err := m.st.Save(); err != nil {
		m.log.Debug("could not persist what peers offer", "err", err)
	}
}

// Services reports what each peer says it offers, keyed by peer id.
//
// A claim, not a promise: it says what a device intends to publish, not what is
// listening. Only the publishing node knows whether the port answers, so a
// caller should present these as names to try rather than as a health display.
func (m *Mesh) Services(now time.Time) map[string][]string {
	// On the roster, which is where the membership check lives: a peer is
	// there because an announce of its carried a credential this mesh's
	// authority signed.
	known := map[string]bool{}
	for _, p := range m.roster.Peers() {
		known[p.ID()] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string][]string, len(m.services))
	for id, ps := range m.services {
		if now.Sub(ps.seen) > ServicesStale || !known[id] {
			continue
		}
		out[id] = append([]string(nil), ps.names...)
	}
	return out
}

// Bound reports which ports each peer says are listening on its own mesh
// address (ADR-026), as "name:port", under the same rules as Services.
func (m *Mesh) Bound(now time.Time) map[string][]string {
	known := map[string]bool{}
	for _, p := range m.roster.Peers() {
		known[p.ID()] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string][]string, len(m.services))
	for id, ps := range m.services {
		if now.Sub(ps.seen) > ServicesStale || !known[id] || len(ps.bound) == 0 {
			continue
		}
		out[id] = append([]string(nil), ps.bound...)
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

// Found is one answer to "does this mesh have a <type>?".
type Found struct {
	Type   string `json:"type"`
	Name   string `json:"name"`   // the nickname it is published under
	Device string `json:"device"` // which peer offers it
	Port   uint16 `json:"port"`
	URL    string `json:"url"` // scheme://<name>.<device>.<suffix>:<port>
}

// OfType answers "does this mesh have a <type>?" with somewhere to go.
//
// A URL rather than a host and a port, because a caller would otherwise
// assemble one and every caller would assemble it slightly differently. The
// name is the leftmost label, the device is the peer, and the suffix is this
// node's — a peer does not tell us what to call it (ADR-032 and the invite that
// used to carry a suffix).
//
// Only peers on the roster, which is where the membership check lives: a claim
// from a device this mesh has not admitted is not an answer.
func (m *Mesh) OfType(typ, suffix string, now time.Time) []Found {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		return nil
	}
	byID := map[string]PeerInfo{}
	for _, p := range m.roster.Peers() {
		byID[p.ID()] = p
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Found
	for id, ps := range m.services {
		p, known := byID[id]
		if !known || now.Sub(ps.seen) > ServicesStale {
			continue
		}
		for name, t := range ps.types {
			if t != typ {
				continue
			}
			f := Found{Type: t, Name: name, Device: p.Name}
			// The port comes from the bound list when the peer published one
			// there; a forwarded service does not announce its port yet, and
			// saying so is better than inventing 80.
			for _, b := range ps.bound {
				if bn, bp, ok := strings.Cut(b, ":"); ok && bn == name {
					if v, err := strconv.Atoi(bp); err == nil && v > 0 && v < 65536 {
						f.Port = uint16(v)
					}
				}
			}
			host := name + "." + p.Name
			if suffix = strings.Trim(suffix, "."); suffix != "" {
				host += "." + suffix
			}
			f.URL = "http://" + host
			if f.Port != 0 {
				f.URL += ":" + strconv.Itoa(int(f.Port))
			}
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Name < out[j].Name
	})
	return out
}
