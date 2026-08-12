package state

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"sort"
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

	// AnnounceBound lists the ports bound to this device's address on this
	// mesh (ADR-026), for the same reason and with the same default.
	AnnounceBound bool

	// Disabled keeps a mesh in the config without running it. Written as
	// enabled = "false".
	//
	// Joining a mesh and using it are different things: a device may be a
	// member of somebody's shared mesh for months and want it off most of the
	// time. Leaving would mean re-enrolling to come back, which is a poor
	// answer to "not right now".
	Disabled bool
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
		})
	}
	for label, m := range c.MeshSet {
		m.Label = label
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
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
	default:
		return fmt.Errorf("line %d: unknown mesh option %q", line, field)
	}
	c.MeshSet[label] = m
	return nil
}
