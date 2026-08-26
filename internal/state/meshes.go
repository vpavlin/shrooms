package state

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

// Several meshes in one config (ADR-015).
//
// The single-mesh form stays exactly as valid as it was:
//
//	network_key = "P27KNQ2..."
//
// and means one mesh called "default". That is not a transitional courtesy —
// it is how a mesh is bootstrapped, how one is recovered, and the only thing
// that works when nothing is running at the other end. The multi-mesh form
// adds prefixed keys, which the hand-rolled parser can read without gaining
// table support:
//
//	mesh.home.key         = "P27KNQ2..."
//	mesh.home.relay       = "true"
//	mesh.shared.key       = "D4R5TBD..."
//	mesh.shared.admin_keys = ["EGRWTGUF…", "3Y5HMGWB…"]

// A Mesh is one network this device belongs to.
type Mesh struct {
	// Label is what this node calls the mesh. Local to this node and never
	// announced: there is no authenticated channel to distribute it, so putting
	// it on the wire would let any member rename the mesh for everyone else.
	Label string

	NetworkKey string
	AdminKeys  []string

	// Relay says this node forwards for peers of *this* mesh that cannot reach
	// each other. Per mesh, because carrying traffic for one set of people does
	// not imply carrying it for another.
	Relay bool

	// Services published on this mesh, in the form ParseSpec reads.
	Services []string

	// AnnounceServices lets this mesh's peers see the names (ADR-023). Per
	// mesh, because telling your own machines what you run and telling
	// somebody else's are different decisions.
	AnnounceServices bool

	// Interface and ListenPort pin this mesh's device name and UDP port.
	//
	// Empty and zero mean "work it out", which is what every config did until
	// there was a way to rename a mesh: the name and port come from the mesh's
	// position in a label-sorted list, so logos01 is simply the second mesh
	// alphabetically.
	//
	// That is fine until a label changes. Renaming test to home re-sorts the
	// list, and every mesh at or after the new position takes a different
	// interface and a different port — so firewall rules, port forwards and
	// every peer's cached endpoint point at the wrong mesh, for a change that
	// was supposed to be cosmetic. Pinning them makes a local nickname stop
	// deciding anything the network can see.
	Interface  string
	ListenPort uint16

	// AnnounceBound lists the ports bound to this device's address on this
	// mesh (ADR-026), for the same reason and with the same default.
	AnnounceBound bool

	// QuietRevocations stops this node repeating what it knows is withdrawn.
	// Written as announce_revocations = "false", inverted for the same reason
	// as Disabled: the useful default is on, and an absent line must mean on.
	//
	// Repeating is on by default because a revocation is the one control
	// message where saying it again is always safe — every receiver verifies
	// the admin signature itself — and because the alternative is that a
	// withdrawal reaches only whoever happened to be listening in the instant
	// it was published. There is no "the admin node does it": the admin key is
	// deliberately not on any daemon (ADR-018), so every node that holds a
	// revocation is equally able to repeat it, and equally the failover.
	//
	// Turn it off on a node whose uplink you are counting bytes on. The cost is
	// one small message per revoked device per epoch, plus one when a peer
	// appears.
	QuietRevocations bool

	// Disabled keeps a mesh in the config without running it. Written as
	// enabled = "false".
	//
	// Joining a mesh and using it are different things: a device may be a
	// member of somebody's shared mesh for months and want it off most of the
	// time. Leaving would mean re-enrolling to come back, which is a poor
	// answer to "not right now".
	Disabled bool

	// Advertise is this mesh's public endpoint, when it is not on a local
	// interface.
	//
	// Per mesh because the value carries a PORT, and each mesh listens on its
	// own. A device-wide advertise was appended verbatim to every mesh's
	// candidates, so a public node told peers to reach all its meshes at one
	// address — right for whichever mesh listens there, wrong for the rest, and
	// wrong in the way that looks like a network fault rather than a typo.
	//
	// The device-wide setting is NOT inherited here, unlike the relay ones
	// below. Inheriting is what the bug was.
	Advertise []string

	// RelayAddr, RelayToken and RelayBlind override the device-wide relay
	// settings for this mesh (docs/blind-relays.md).
	//
	// These DO inherit: a relay address carries no per-mesh meaning — the tag a
	// device registers under is derived per mesh from the relay's address, so
	// one relay serves every mesh correctly. Pointing a phone at one relay and
	// having every mesh use it is the case worth keeping easy.
	//
	// The override exists for the opposite case, which is real: a mesh whose
	// traffic should not cross somebody else's relay, on a device that uses one
	// for everything else. Empty means inherit; RelayNone turns it off.
	RelayAddr  string
	RelayToken string
	RelayBlind []string

	// RelayNone stops this mesh using the device's relays without naming
	// another. Written as relay_blind = "none", because an empty list in the
	// config is indistinguishable from an absent one and would read as
	// inherit.
	RelayNone bool

	// InheritsIdentity marks the mesh that carries the device keys this node
	// had before it was on more than one mesh.
	//
	// Not a preference. Re-deriving that mesh's identity changes this node's
	// overlay address and voids its credential, so every peer sees a stranger.
	// It was previously inferred from config SHAPE — "the mesh described by the
	// top-level fields is the one with the old keys" — which is why so much
	// code had to know which shape it was holding.
	//
	// Written down instead, so the shape can stop mattering. Temporary by
	// design: a mesh re-minted onto a Keycard gets fresh keys and no longer
	// needs it, and the field goes when the last one has been.
	InheritsIdentity bool
}

// Key parses the mesh's network key.
func (m Mesh) Key() (identity.NetworkKey, error) {
	return identity.ParseNetworkKey(m.NetworkKey)
}

// Authority returns the admin keys this mesh trusts, or nil if it has none —
// which means membership is the network key alone, and is still supported.
func (m Mesh) Authority() (*cred.Authority, error) { return parseAuthority(m.AdminKeys) }

// NetworkID identifies a mesh locally: it keys the per-mesh identity, the
// per-mesh state, and demultiplexing between meshes.
//
// Not to be confused with cred.MeshID, which is derived from the admin key set
// and says who may admit devices. A mesh with no authority has no cred.MeshID
// at all, so this is the only identifier that always exists. See ADR-015.
func (m Mesh) NetworkID() (string, error) {
	nk, err := m.Key()
	if err != nil {
		return "", err
	}
	return NetworkID(nk), nil
}

// NetworkID derives the local identifier for a network key.
func NetworkID(nk identity.NetworkKey) string {
	sum := sha256.Sum256(append([]byte("mesh/v1/meshid"), nk[:]...))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:8]))
}

// MeshIDFromInvite picks the mesh id out of an invite response.
//
// A node that has been updated sends the id directly, because the first round
// of the exchange does not consume the invite and so must not carry a secret.
// A node running an older build sent the network key instead; the id is a
// one-way hash of that key, so both answer the same question and a joining
// device can talk to either.
func MeshIDFromInvite(meshID string, networkKey []byte) (string, error) {
	if meshID != "" {
		return meshID, nil
	}
	nk, err := identity.NetworkKeyFromBytes(networkKey)
	if err != nil {
		return "", fmt.Errorf("the invite answer named no mesh: %w", err)
	}
	return NetworkID(nk), nil
}

// DefaultLabel is the mesh a single-mesh config describes.
const DefaultLabel = "default"

// Active returns the meshes that should be running.
//
// The single-mesh form is always active: it has nowhere to say otherwise, and
// a device with one mesh that is switched off is a device that is switched
// off — which is what disconnecting is for.
func (c Config) Active() []Mesh {
	all := c.Meshes()
	out := all[:0:0]
	for _, m := range all {
		if !m.Disabled {
			out = append(out, m)
		}
	}
	return out
}

// Meshes returns every mesh in the config, single-mesh form included.
//
// Ordered by label so that anything derived from the list — a hosts block, a
// status page — does not reshuffle between runs for no reason.
func (c Config) Meshes() []Mesh {
	out := make([]Mesh, 0, len(c.MeshSet)+1)
	if c.NetworkKey != "" && c.NetworkKey != KeyPlaceholder {
		out = append(out, Mesh{
			Label:            DefaultLabel,
			NetworkKey:       c.NetworkKey,
			AdminKeys:        c.AdminKeys,
			Relay:            c.Relay,
			Services:         c.Services,
			AnnounceServices: c.AnnounceServices,
			AnnounceBound:    c.AnnounceBound,
			QuietRevocations: c.QuietRevocations,
			// The mesh in the top-level fields is the one this device already
			// belonged to. Recorded here, once, so nothing downstream has to
			// ask which shape a mesh came from.
			InheritsIdentity: true,
		})
	}
	for label, m := range c.MeshSet {
		m.Label = label
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })

	// Interface and port are resolved HERE and nowhere else.
	//
	// They used to be derived by each caller from a mesh's position in a list,
	// and the callers disagreed about which list: the daemon numbered the
	// ACTIVE meshes, while "mesh list" and pinning numbered ALL of them. With
	// one mesh disabled the two answers differ, so the port a node reported was
	// not the port it bound.
	//
	// Over every mesh rather than the running ones, so that disabling a mesh
	// does not renumber the others. That was the worse half of the same bug:
	// switching one mesh off moved every later mesh to a different interface
	// and a different port on the next restart, which drops their tunnels and
	// invalidates the endpoints their peers remember.
	for i := range out {
		iface, port := c.Interface, c.ListenPort
		if i > 0 {
			iface, port = fmt.Sprintf("%s%d", c.Interface, i), c.ListenPort+uint16(i)
		}
		// A pin wins, so a mesh keeps what it had when something before it in
		// the order was renamed or removed.
		if out[i].Interface == "" {
			out[i].Interface = iface
		}
		if out[i].ListenPort == 0 {
			out[i].ListenPort = port
		}
	}
	return out
}

// validateMeshes checks the mesh set. Called from Validate, so that a config
// naming two meshes with one key fails at load rather than as a mesh where
// half the peers are missing.
func (c Config) validateMeshes() error {
	seen := map[string]string{}
	for _, m := range c.Meshes() {
		if m.Label == "" {
			return fmt.Errorf("a mesh with no name")
		}
		if strings.ContainsAny(m.Label, ". \t") {
			// The label becomes a DNS label in vps.home.mesh.
			return fmt.Errorf("mesh name %q: no dots or spaces", m.Label)
		}
		nk, err := m.Key()
		if err != nil {
			return fmt.Errorf("mesh %q: %w", m.Label, err)
		}
		id := NetworkID(nk)
		if other, dup := seen[id]; dup {
			return fmt.Errorf("meshes %q and %q have the same network key", other, m.Label)
		}
		seen[id] = m.Label
		if _, err := m.Authority(); err != nil {
			return fmt.Errorf("mesh %q: %w", m.Label, err)
		}
	}
	return nil
}

// ValidMeshLabel checks a name this device may call a mesh.
//
// It is a local nickname and nothing on the wire carries it, so the rules are
// not about interoperating — they are about the two places the label is
// substituted into something with its own syntax. It becomes part of a config
// key (mesh.<label>.key), where a dot would create a field nobody meant, and
// part of a DNS name (vps.<label>.internal), where anything outside a hostname
// label cannot be queried at all.
func ValidMeshLabel(label string) error {
	if label == "" {
		return errors.New("a mesh label cannot be empty")
	}
	if len(label) > 63 {
		return fmt.Errorf("mesh label %q is %d characters; a DNS label holds 63", label, len(label))
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("mesh label %q contains %q: use lower-case letters, "+
				"digits and hyphens, because the label becomes part of a config "+
				"key and part of a DNS name", label, r)
		}
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("mesh label %q starts or ends with a hyphen, which a DNS label may not", label)
	}
	return nil
}

// parseMeshKey reads a "mesh.<label>.<field>" config key. Reports false for
// anything that is not one.
func parseMeshKey(key string) (label, field string, ok bool) {
	rest, found := strings.CutPrefix(key, "mesh.")
	if !found {
		return "", "", false
	}
	label, field, found = strings.Cut(rest, ".")
	if !found || label == "" || field == "" {
		return "", "", false
	}
	return label, field, true
}

// setMeshField applies one prefixed key.
func (c *Config) setMeshField(label, field, val string, line int) error {
	if c.MeshSet == nil {
		c.MeshSet = map[string]Mesh{}
	}
	m := c.MeshSet[label]
	switch field {
	case "key":
		m.NetworkKey = unquote(val)
	case "admin_keys":
		m.AdminKeys = parseArray(val)
	case "relay":
		m.Relay = unquote(val) == "true"
	case "enabled":
		m.Disabled = unquote(val) == "false"
	case "services":
		m.Services = parseArray(val)
	case "announce_services":
		m.AnnounceServices = unquote(val) == "true"
	case "announce_bound":
		m.AnnounceBound = unquote(val) == "true"
	case "announce_revocations":
		m.QuietRevocations = unquote(val) == "false"
	case "advertise":
		m.Advertise = parseArray(val)
	case "relay_addr":
		m.RelayAddr = unquote(val)
	case "relay_token":
		m.RelayToken = unquote(val)
	case "relay_blind":
		// "none" rather than an empty list: [] parses to nil, which is what an
		// absent line gives, so there would be no way to say "not this mesh".
		if v := unquote(val); v == "none" {
			m.RelayNone = true
		} else {
			m.RelayBlind = parseArray(val)
		}
	case "inherits_identity":
		// The mesh holding the device keys this node had before it was on more
		// than one mesh. Written down rather than inferred from config shape;
		// see the field's comment. A flattened config states it, an old-shaped
		// one has it implied by the top-level fields.
		m.InheritsIdentity = unquote(val) == "true"
	case "iface":
		m.Interface = unquote(val)
	case "port":
		n, err := strconv.ParseUint(unquote(val), 10, 16)
		if err != nil {
			return fmt.Errorf("line %d: mesh port %q: %w", line, val, err)
		}
		m.ListenPort = uint16(n)
	default:
		return fmt.Errorf("line %d: unknown mesh option %q", line, field)
	}
	c.MeshSet[label] = m
	return nil
}

// ForMesh is the config one mesh runs under: the device's settings, with
// everything the mesh owns replaced by that mesh's own.
//
// One function because there were two copies — the daemon's and the phone's —
// and they had already drifted: the phone was not applying AnnounceServices,
// AnnounceBound or QuietRevocations at all, so a per-mesh setting made on a
// desktop did nothing on Android.
//
// ListenPort is the field this exists for. A mesh binds the port its caller
// worked out (the first keeps the config's, the nth gets the nth after it), but
// the config handed to the mesh package kept the DEVICE's port — so every mesh
// except the first announced its local addresses with the first mesh's port.
//
// That is not a cosmetic error. A peer on the same LAN reads the announce,
// dials 192.168.0.10:51820 for a mesh that is actually listening on 51823, and
// reaches the first mesh's WireGuard socket — which rejects the handshake,
// because the keys belong to a different mesh, and says nothing. Both devices
// then sit there announcing and probing until something else, a relay, carries
// the traffic instead. It looks exactly like "two devices on the same network
// cannot find each other", and it was reported that way twice.
//
// Only the first mesh escaped, because its port IS the device's port. That is
// the whole shape of the primary-mesh problem in one field
// (docs/one-kind-of-mesh.md).
func (c Config) ForMesh(m Mesh, port uint16) Config {
	out := c
	out.NetworkKey = m.NetworkKey
	out.AdminKeys = m.AdminKeys
	out.Relay = m.Relay
	out.Services = m.Services
	out.AnnounceServices = m.AnnounceServices
	out.AnnounceBound = m.AnnounceBound
	out.QuietRevocations = m.QuietRevocations
	out.ListenPort = port

	// Advertise does NOT inherit. A device-wide entry names a port, and only
	// one mesh listens there — the one keeping the device's base port. Giving
	// it to the others is the bug this field exists to fix, so they get
	// nothing unless they say something.
	switch {
	case len(m.Advertise) > 0:
		out.Advertise = m.Advertise
	case port == c.ListenPort:
		// The mesh on the device's own port: the device-wide value is about
		// this one, and a single-mesh config is unchanged.
	default:
		out.Advertise = nil
	}

	// The relay settings DO inherit, because a relay address means the same
	// thing to every mesh. An override replaces the lot rather than merging:
	// half this device's relays and half the mesh's is not a thing anybody
	// asked for, and would be hard to read back out of the config.
	switch {
	case m.RelayNone:
		out.RelayAddr, out.RelayToken, out.RelayBlind = "", "", nil
	case m.RelayAddr != "" || len(m.RelayBlind) > 0:
		out.RelayAddr, out.RelayToken, out.RelayBlind = m.RelayAddr, m.RelayToken, m.RelayBlind
	}
	return out
}

// Flatten moves the top-level mesh into the mesh set, so that every mesh in the
// config is described the same way.
//
// The config has carried two shapes since multi-mesh landed: one mesh in
// top-level fields, the rest in mesh.<label> keys. Every piece of code that
// touched a mesh had to know which it was holding, and the bugs that came out
// of that are listed in docs/one-kind-of-mesh.md — they are all the same bug,
// a per-mesh fact read from a place that predates there being more than one.
//
// Three things it is careful about:
//
// **Interface and port are pinned explicitly**, from the resolved values. After
// this, no mesh's interface or port depends on its position in a sorted list,
// so adding or removing a mesh cannot move another one.
//
// **InheritsIdentity is carried across.** The mesh that was in the top-level
// fields is the one holding this device's pre-multi-mesh keys, and losing that
// would re-derive them, change the node's overlay address and void its
// credential. Implicit before, stated after.
//
// **The device's own settings stay where they are.** Name, base interface and
// port, preset, mode and the relay pins are the device's, not any mesh's.
//
// Refuses rather than merges when the set already names a mesh "default": two
// meshes would claim one label, and picking either silently is how a node ends
// up on a network it was not asked to join.
func (c Config) Flatten() (Config, error) {
	if c.NetworkKey == "" || c.NetworkKey == KeyPlaceholder {
		return c, nil // already flat, or prepared and holding no key yet
	}
	if _, clash := c.MeshSet[DefaultLabel]; clash {
		return c, fmt.Errorf("this config has a top-level mesh and a mesh called %q; "+
			"rename one with `shrooms mesh rename` before flattening, because "+
			"they would become the same entry", DefaultLabel)
	}

	out := c
	out.MeshSet = make(map[string]Mesh, len(c.MeshSet)+1)
	for _, m := range c.Meshes() {
		e := m
		e.Label = "" // the map key carries it; a field beside it would drift
		out.MeshSet[m.Label] = e
	}

	// Everything a mesh owns now lives on the mesh.
	out.NetworkKey = ""
	out.AdminKeys = nil
	out.Relay = false
	out.Services = nil
	out.AnnounceServices = false
	out.AnnounceBound = false
	out.QuietRevocations = false
	return out, nil
}

// MeshesMissingAdvertise names the meshes that will announce no configured
// endpoint because the device-wide advertise is not theirs.
//
// Worth saying out loud. Before this, a device-wide advertise was given to
// every mesh, so a public node appeared to have told all of them where it was
// — while pointing all but one at the wrong port. Now the others correctly get
// nothing, and "correctly nothing" is indistinguishable from "forgot to
// configure it" unless somebody says which happened.
//
// Empty when there is no device-wide advertise, when there is only one mesh, or
// when every mesh has its own.
func (c Config) MeshesMissingAdvertise() []string {
	if len(c.Advertise) == 0 {
		return nil
	}
	var out []string
	for _, m := range c.Meshes() {
		if len(m.Advertise) == 0 && m.ListenPort != c.ListenPort {
			out = append(out, m.Label)
		}
	}
	return out
}
