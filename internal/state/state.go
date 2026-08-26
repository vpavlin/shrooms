// Package state handles on-disk configuration and persistent device state.
//
// Two files, deliberately separated by who writes them:
//
//   - config.toml   — human-edited. The network key and a few knobs.
//   - state.json    — daemon-owned. Device keys and the announce sequence
//     number. Never edited by hand.
package state

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/service"
)

// Default locations. Overridable so a developer can run several nodes on one
// machine without root.
const (
	DefaultConfigPath = "/etc/shrooms/config.toml"
	DefaultStateDir   = "/var/lib/shrooms"

	// The paths used before the project was renamed to Shrooms. Still honoured
	// when they exist and the new ones do not, because /var/lib/logos-vpn holds
	// the device identity: a node that silently failed to find it would come
	// back as a different device with a different overlay address, and every
	// peer would have to learn it. Migration is therefore `mv`, at leisure,
	// per node.
	LegacyConfigPath = "/etc/logos-vpn/config.toml"
	LegacyStateDir   = "/var/lib/logos-vpn"
)

// ConfigPath returns the config to read: the Shrooms path when it exists, the
// pre-rename path when only that does.
//
// Only consulted for the default. An explicit --config is used as given, since
// someone naming a path means that path.
func ConfigPath(explicit string) string {
	return pickPath(explicit, DefaultConfigPath, LegacyConfigPath, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}

// pickPath is the choice itself, with the filesystem passed in so it can be
// tested without one — the previous test asserted "neither exists" on a machine
// where one did, and passed or failed depending on whose laptop ran it.
func pickPath(explicit, preferred, legacy string, exists func(string) bool) string {
	if explicit != preferred {
		return explicit // naming a path means that path
	}
	if exists(preferred) {
		return preferred
	}
	if exists(legacy) {
		return legacy
	}
	return preferred
}

// StateDir returns the state directory to use, preferring the Shrooms path but
// keeping an existing pre-rename one — which is where the device identity is.
func StateDir(explicit string) string {
	// state.json, not the directory: an empty directory left by a package
	// manager must not win over one that actually holds an identity.
	return pickPath(explicit, DefaultStateDir, LegacyStateDir, func(p string) bool {
		_, err := os.Stat(filepath.Join(p, "state.json"))
		return err == nil
	})
}

// KeyPlaceholder marks a config prepared without its key.
//
// It exists so a machine can be set up by someone — or something — that must
// never see the key: everything else is written, and the key is typed in
// afterwards by whoever holds it. The key is a bearer credential (ADR-008), so
// the fewer places it travels, the better.
const KeyPlaceholder = "PASTE-THE-NETWORK-KEY-HERE"

// DefaultPreset is the network to join.
//
// logos.test, not logos.dev: it is what the messaging team recommends, and as
// of 2026-08-07 it is also the one that works. logos.dev has migrated to
// cluster 3 while the preset compiled into our pinned liblogosdelivery still
// says cluster 2, so a logos.dev node is hung up on by every peer it meets.
// logos.test is cluster 2 and the preset agrees, so it needs no override.
const DefaultPreset = "logos.test"

// Node modes. Core relays for the network; Edge subscribes and forwards
// nothing. See Config.Mode for what each costs.
const (
	ModeCore = "Core"
	ModeEdge = "Edge"
)

// Config is the human-edited configuration.
type Config struct {
	// NetworkKey is the mesh secret, base32. In v1 it is a bearer credential:
	// holding it makes you a member.
	NetworkKey string

	// Name is self-asserted and appears in the signed announce. It
	// authenticates only as "the device holding key X calls itself this".
	Name string

	// AnnounceServices tells this mesh's peers which service names this device
	// publishes, so their roster can show what the mesh offers (ADR-023).
	//
	// Off by default, and per mesh. A member can already find your services by
	// connecting to your address on common ports, so this discloses
	// discoverability rather than access — but the names carry intent that a
	// port scan does not, and a mesh shared with other people is exactly where
	// an inventory is worth something to somebody else.
	AnnounceServices bool

	// AnnounceBound tells this mesh's peers which ports are listening on this
	// device's mesh address (ADR-026).
	//
	// Separate from AnnounceServices and off by default for a reason of its
	// own: those are declared, one line at a time, by somebody who meant it,
	// while these are discovered — so this announces whatever happens to be
	// bound, including the thing you started for ten minutes and forgot. The
	// ports are already reachable by every member either way; what this
	// changes is whether they are listed.
	AnnounceBound bool

	// QuietRevocations stops this node repeating what it knows is withdrawn.
	// See the field of the same name on Mesh; inverted so an absent line means
	// the useful default, which is to repeat.
	QuietRevocations bool

	// PortMapping asks the local router to open this node's port, so a machine
	// behind a home NAT can be dialled without anyone editing a router page
	// (ADR-024). On by default: it is best effort, a refusal costs one request
	// at startup, and the alternative is that a mesh with no publicly
	// reachable member simply does not work from outside the house.
	//
	// Off is a legitimate choice — it does ask to be reachable from the
	// internet — which is why it is a setting rather than a fact.
	PortMapping bool

	// Advertise lists extra endpoints to announce as dialable.
	//
	// Usually unnecessary. A node enumerates its own interface addresses, so a
	// VPS with a directly attached public IP announces it automatically, and a
	// NATed node learns its public address from the first peer that answers a
	// probe. This is for the case neither covers: a public address that is not
	// on any local interface, such as a port forward or a cloud instance that
	// sees a private IP with the public one NAT'd in front.
	Advertise []string

	// ListenPort is the shared UDP port for WireGuard and control traffic.
	ListenPort uint16

	// Interface is the TUN device name.
	Interface string

	// Preset selects the network. It is what loads the fleet's entry nodes, so
	// it is required even when ClusterID is set.
	Preset string

	// DeliveryPort pins the TCP port the rendezvous node listens on.
	//
	// Zero means "let the library choose", which is what every node did before
	// this existed and remains right for a node nobody dials.
	//
	// It matters for a node other people bootstrap from (ADR-031). The library
	// exposes no way to ask which port it picked — there is a PeerID call and
	// nothing for listen addresses — so a node can only publish a dialable
	// address for itself if it decided the port in the first place. A random
	// port would also change on every restart, which is a published address
	// that quietly stops working.
	DeliveryPort uint16

	// EntryNodes are explicit bootstrap addresses (enrtree:, enr:, or
	// multiaddr), used instead of whatever the preset would supply.
	//
	// Exists because the presets compiled into our pinned liblogosdelivery are
	// demonstrably stale — they still describe logos.dev as cluster 2, which it
	// is not — so their bootstrap addresses cannot be trusted either. This is
	// the escape hatch for pointing at a fleet whose current addresses we know,
	// without waiting for a library rebuild.
	EntryNodes []string

	// ClusterID overrides the cluster the preset would select. Zero means
	// "let the preset decide", which is normally right.
	//
	// Set it only when a fleet has migrated and the library has not caught up.
	// That is the state logos.dev is in as of 2026-08-07: its peers report
	// cluster 3, our preset says 2, and the mismatch does not fail loudly — the
	// node connects to each peer, the metadata exchange disagrees, and the peer
	// hangs up:
	//
	//   Received WakuMetadata request  remoteClusterId=ok(3) localClusterId=2
	//   disconnecting from peer  reason="different clusterId reported: 2 vs 3"
	//
	// which presents as "the fleet is down" rather than as a version skew.
	//
	// Passing it is NOT free: a non-zero clusterId activates a legacy
	// cluster-to-network mapping inside the library, where 2 means logos.dev.
	// So setting cluster_id = 2 alongside preset = "logos.test" silently
	// bootstraps off logos.dev instead. Leave it at zero unless you have
	// measured that you need it.
	ClusterID uint16

	// Mode is the rendezvous node mode: ModeCore or ModeEdge.
	//
	// Core relays for the network. That is the neighbourly setting and it is
	// not cheap: a Core node subscribes to every shard in the cluster, so it
	// carries the whole cluster's traffic and not just this mesh's. Measured
	// idle on a home connection, against logos.test, over ten minutes:
	//
	//	Core   20.3 MB/h   (0.49 GB/day)   139 connections in 10 min
	//	Edge    3.4 MB/h   (0.08 GB/day)    90 connections in 10 min
	//
	// In that sample 693 of 745 relayed messages belonged to another
	// application entirely, and 14 were on this mesh's shard. By comparison
	// everything logos-vpn itself sends — a 512-byte announce every 45s, a
	// 104-byte probe per path every 5s — is well under 1 MB/h.
	//
	// Edge uses filter and lightpush instead: it subscribes to what it wants
	// and forwards nothing. Right for anything metered or battery-powered, and
	// on Android arguably right regardless, since Doze suspends the SoC and
	// gossipsub tears down every connection when the keepalive loop is late
	// (ADR-003).
	//
	// Still Core by default everywhere, including Android: someone has to
	// relay, and changing what a node contributes to the network should be a
	// decision rather than a surprise.
	Mode string

	// Relay makes this node forward traffic for peers that cannot reach each
	// other directly. Only useful on a node with a reachable address.
	Relay bool

	// RelayAddr pins relays that are members of this mesh, overriding
	// discovery.
	//
	// Normally empty. A member relay announces itself like any other peer and
	// is picked up from the roster, so no node needs to be told where one is.
	// Kept as an escape hatch: bringing up a mesh whose relay has not announced
	// yet, or forcing a particular one while debugging.
	//
	// For a relay somebody else runs, see RelayBlind — the two are separate
	// settings because they are separate things, and an address alone cannot
	// say which it is.
	RelayAddr string

	// RelayToken authenticates us to a blind relay that wants one
	// (docs/blind-relays.md). Setting it implies the pinned relay is blind.
	//
	// Not a secret that protects anything of ours: it says the operator agreed
	// to carry our traffic, and it is shared by everyone they agreed to carry.
	// A relay cannot read what it forwards whether it asks for a token or not.
	RelayToken string

	// RelayBlind lists relays run by people who are not on this mesh
	// (docs/blind-relays.md).
	//
	//	relay_blind = ["203.0.113.10:31760", "198.51.100.7:32100"]
	//
	// A separate setting from RelayAddr rather than a flag beside it, because
	// an address cannot say which kind it is and guessing wrong fails silently
	// — every frame authenticated under the wrong key, dropped without comment
	// at the other end.
	//
	// Being blind changes two things. Frames authenticate under a key derived
	// from the relay's token, or a public one when it is open, rather than from
	// the network key a stranger must never hold. And devices register under a
	// tag derived from the mesh relay key rather than under their tunnel key,
	// so the operator sees handles that mean nothing off their own relay.
	//
	// Several are worth listing: a relay somebody else runs may be busy,
	// redeployed at a new port, or simply gone. A device registers with at most
	// two of them, and takes them in the order given.
	//
	// These have to be configured because they cannot be discovered: a blind
	// relay holds no network key and runs no delivery node, so it has no way to
	// announce itself.
	RelayBlind []string

	// StatusFile optionally writes the status JSON to a file, for a monitoring
	// view that can read a file but not open a unix socket — QML can do the
	// first and not the second.
	//
	// Preferred over the HTTP endpoint that used to exist: no port is opened
	// at all, and access is decided
	// by file permissions, which is a mechanism the operating system already
	// has and everyone already understands.
	StatusFile string

	// SocketGroup is the group allowed to use the control socket, by name or
	// numeric gid. Empty means the daemon's own group, which is root.
	//
	// Without it every `shrooms status` needs sudo, because the daemon needs
	// CAP_NET_ADMIN and therefore runs as root. The socket is 0660 either way;
	// this is what makes the group half of that mean something.
	SocketGroup string

	// StatusFileGroup is the group allowed to read StatusFile, by name or
	// numeric gid. Empty means the daemon's own group, which is root.
	//
	// Required in practice, not optional: the daemon runs as root, the file is
	// written 0640, and a desktop monitoring view runs as you. Without a group
	// the file is readable only by root and the view reports that it cannot
	// read it — which is precisely the state this setting exists to avoid, and
	// which shipped for a day because "0640" was implemented and "with a group"
	// was not.
	//
	//	status_file       = "/run/logos-vpn/status.json"
	//	status_file_group = "vpavlin"
	StatusFileGroup string

	// ManageHosts lets the daemon keep /etc/hosts current as the roster
	// changes, so peers stay reachable by name without re-running anything.
	//
	// Off by default: a VPN silently editing a system file that cloud-init,
	// NetworkManager and others also touch is a surprise, and it should be the
	// operator's choice. The DNS server (M6) removes the need for it.
	ManageHosts bool

	// Services publishes local ports on the mesh under their own names, so
	// that what runs here is reachable as `<service>.<device>.mesh` rather than
	// as a port number someone has to remember.
	//
	//	services = ["immich:2283", "jellyfin:8096", "grafana:443->3000"]
	//
	// Each entry is `name:port`, optionally `->target` when the application
	// listens somewhere other than the same port on loopback. The daemon binds
	// the port on the overlay address and forwards to the target, which is what
	// makes this work for the many applications that bind 0.0.0.0 and would
	// otherwise never see an IPv6 connection.
	//
	// Flat strings rather than a table because this parser has no nesting; see
	// internal/service for the syntax.
	Services []string

	// HostsSuffix is the domain appended to peer names.
	HostsSuffix string

	// AdminKeys are the admin public keys this mesh trusts to sign membership,
	// base32, as printed by `shrooms admin init`. Public values: they belong in
	// a config file, in git, on a sticker.
	//
	// Empty means membership is the network key alone, which is how every mesh
	// works today and what ADR-018 replaces. Both schemes run at once during
	// migration: with no admin keys a node behaves exactly as before, and with
	// them it additionally requires every peer to present a credential the set
	// signed.
	//
	// The set is fixed when the mesh is minted, because the mesh id commits to
	// it and the address prefix derives from the id. Adding one later
	// re-addresses every node.
	AdminKeys []string

	// AdminPK is reserved for M5 (admin-signed credentials) and ignored while
	// empty. Present now so adding it later is not a config break.
	AdminPK string

	// MeshSet holds the additional meshes of a multi-mesh config (ADR-015),
	// keyed by their local label. Empty for every config written so far, and
	// NetworkKey above remains the single-mesh form rather than being folded
	// into this — it is the form that bootstraps and recovers a mesh, and the
	// one the whole install base uses.
	MeshSet map[string]Mesh
}

// DefaultConfig returns a config with everything but the network key filled in.
func DefaultConfig() Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "unnamed"
	}
	return Config{
		Name:        host,
		ListenPort:  51820,
		PortMapping: true,
		Interface:   "shrooms0",
		Preset:      DefaultPreset,
		Mode:        "Core",
		HostsSuffix: "mesh",
	}
}

// Validate checks a loaded config.
func (c *Config) Validate() error {
	// Device-level settings, checked whatever shape the meshes are in.
	//
	// These used to be skipped for a config whose meshes are all in
	// mesh.<label> tables: the function returned validateMeshes() before
	// reaching them. So the shape this codebase is moving TOWARDS was the one
	// where an empty name, a zero port or an unset mode loaded cleanly and
	// failed later, somewhere with less context to explain itself.
	if c.Name == "" {
		return errors.New("name is empty")
	}
	if c.ListenPort == 0 {
		return errors.New("listen_port is 0")
	}
	if c.Interface == "" {
		return errors.New("interface is empty")
	}
	// Checked here because the library does not: an unrecognised mode is
	// accepted and quietly behaves as some default, so a typo reads as
	// "I set Edge and it still uses a gigabyte a day".
	switch c.Mode {
	case ModeCore, ModeEdge:
	case "":
		return errors.New("mode is empty — expected \"Core\" or \"Edge\"")
	default:
		return fmt.Errorf("mode %q is not a node mode — expected %q (relays for the network) or %q (subscribes only)",
			c.Mode, ModeCore, ModeEdge)
	}
	if c.Preset == "" && len(c.EntryNodes) == 0 {
		return errors.New("preset is empty and no entry_nodes are set — the node would have nowhere to bootstrap from")
	}

	// A mesh, in either shape. "No network_key" is only an error when there
	// are no meshes at all.
	if c.NetworkKey == "" && len(c.MeshSet) == 0 {
		return errors.New("network_key is not set — run `shrooms init` or `shrooms join`")
	}
	if c.NetworkKey == KeyPlaceholder {
		// Named specifically: "invalid base32" would send someone hunting a
		// corrupt file rather than the line they were told to edit.
		return errors.New("network_key is still the placeholder — edit the config and paste the mesh key")
	}
	if c.NetworkKey != "" {
		if _, err := identity.ParseNetworkKey(c.NetworkKey); err != nil {
			return fmt.Errorf("network_key: %w", err)
		}
	}
	// Rejected at load rather than at bind: a typo here otherwise surfaces as
	// one service quietly missing from a mesh that is otherwise fine.
	// Services are deliberately NOT validated here.
	//
	// Validate runs at load, so anything it refuses is a daemon that will not
	// start. A service is the least important thing in this file: it publishes
	// a local port under a name, and the mesh works perfectly without it —
	// startInstance already logs "services not published" and carries on, which
	// is the behaviour this check was preventing from ever running.
	//
	// It cost real access. A mistyped service string on a remote machine made
	// the daemon refuse to start, which took away the tunnel that was the way
	// in to fix it. Nothing that optional may stand between a machine and its
	// own mesh.
	//
	// Checked on the way IN instead, by whoever sets it — see ValidateServices
	// and the /config/services handler — because that is where a person is
	// present to be told, and where refusing costs nothing.
	// Rejected at load: a mistyped admin key would otherwise surface much later
	// as a mesh where every peer is refused, which looks like a network fault.
	if _, err := c.Authority(); err != nil {
		return err
	}
	// And the extra meshes, if this config names any.
	return c.validateMeshes()
}

// Authority returns the admin keys this mesh trusts, or nil when membership is
// still the network key alone.
//
// nil is not an error: it is the pre-credential world, and every node lives
// there until a mesh is minted with admin keys.
func (c *Config) Authority() (*cred.Authority, error) { return parseAuthority(c.AdminKeys) }

// parseAuthority is shared with the per-mesh form, so a mesh in a multi-mesh
// config and the single-mesh one cannot disagree about what an admin key is.
func parseAuthority(adminKeys []string) (*cred.Authority, error) {
	if len(adminKeys) == 0 {
		return nil, nil
	}
	keys := make([]ed25519.PublicKey, 0, len(adminKeys))
	for _, s := range adminKeys {
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).
			DecodeString(strings.ToUpper(strings.TrimSpace(s)))
		if err != nil {
			return nil, fmt.Errorf("admin_keys: %q: %w", s, err)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return cred.NewAuthority(keys...)
}

// ValidateServices reports whether the service list can be parsed.
//
// For write paths only: a caller about to store a config should refuse a
// malformed service rather than let somebody discover it as a service that
// silently never appears. Load does not call it — see Validate.
func (c *Config) ValidateServices() error {
	if _, err := service.ParseSpecs(c.Services); err != nil {
		return fmt.Errorf("services: %w", err)
	}
	return nil
}

// ServiceSpecs returns the parsed services.
//
// An error is survivable everywhere it is called: the mesh runs without
// publishing anything, so callers log and continue rather than failing.
func (c *Config) ServiceSpecs() ([]service.Spec, error) {
	return service.ParseSpecs(c.Services)
}

// SetOwnCredential stores a credential this mesh received for itself.
//
// The mesh may hold a view rather than the owning State — it does whenever the
// entry was decoded from disk rather than created this run — and a view's Save
// writes the mesh entry and nothing else. So assigning Credential and saving
// updated the mesh and left the single-mesh field showing the credential the
// device had stopped using, which is what `shrooms keys` reads.
//
// Renewal made that visible: the daemon logged "credential renewed", announced
// with the new one, and `keys` went on reporting the old serial indefinitely.
func (s *State) SetOwnCredential(raw []byte) error {
	s.Credential = append([]byte(nil), raw...)
	if s.owner != nil && s.original {
		s.owner.Credential = append([]byte(nil), raw...)
	}
	return s.Save()
}

// SetCredential stores this device's membership and persists it.
//
// devPub is the device the credential names, and it decides where this lands.
// The top-level field is not enough on its own: once a mesh has a per-mesh
// entry, MeshState returns that and never consults the top level again, so a
// credential written only there is stored successfully and never read.
//
// That is what "installed a credential, and status still says no credential"
// was — on a node whose per-mesh entry had been created during the window when
// it had no credential to adopt. `shrooms keys` reads the top level and showed
// it; the daemon reads the mesh entry and did not.
//
// Written to every identity that matches, which is at most one per mesh: the
// legacy mesh shares the top-level identity, and every other mesh has a
// distinct derived one, so the credential can only belong to the mesh whose
// device key it names.
func (s *State) SetCredential(devPub []byte, raw []byte) error {
	stored := false
	if s.Identity != nil && bytes.Equal(s.Identity.DevicePub, devPub) {
		s.Credential = append([]byte(nil), raw...)
		stored = true
	}
	for _, ms := range s.Meshes {
		if ms == nil || ms.Identity == nil {
			continue
		}
		if bytes.Equal(ms.Identity.DevicePub, devPub) {
			ms.Credential = append([]byte(nil), raw...)
			stored = true
		}
	}
	if !stored {
		return fmt.Errorf("this credential names device %x, which is not this "+
			"device's key on any mesh it belongs to", devPub[:min(8, len(devPub))])
	}
	return s.Save()
}

// Key returns the parsed network key.
func (c *Config) Key() (identity.NetworkKey, error) {
	return identity.ParseNetworkKey(c.NetworkKey)
}

// state is the JSON form of persistent device state.
type stateFile struct {
	DevicePriv string `json:"device_priv"` // base64, ed25519 seed+pub
	WGPriv     string `json:"wg_priv"`     // base64, curve25519
	Seq        uint64 `json:"seq"`

	// Credential is this device's membership, base64 of the wire form. Not a
	// secret — it is published in every announce — but it belongs with the
	// identity it names, and a device that lost it would be unable to prove
	// membership until re-enrolled.
	Credential string `json:"credential,omitempty"`

	// Services is the single-mesh form of what peers offer, the same shape
	// the per-mesh entries carry (ADR-023).
	Services map[string]ServiceClaim `json:"services,omitempty"`

	// Master is the secret every per-mesh identity derives from (ADR-015),
	// base64. Absent in every file written so far, and generated the first time
	// a second mesh is joined — never for a device that only ever has one, so
	// nothing is created that nothing uses.
	Master string `json:"master,omitempty"`

	// Meshes is per-mesh state, keyed by network id. The fields above remain
	// the single-mesh form and are not duplicated into it for the mesh that
	// owns them, so an older binary reading this file still finds what it
	// expects.
	Meshes map[string]meshStateFile `json:"meshes,omitempty"`
}

// State is the daemon-owned persistent state.
type State struct {
	dir string

	Identity *identity.Identity

	// Credential is this device's membership, or nil before it has one.
	Credential []byte

	// Seq is the announce sequence number. It MUST persist across restarts: a
	// device that restarts and resets Seq to 1 is rejected by every peer's
	// ReplayGuard until they forget it, which looks exactly like the device
	// having vanished.
	Seq uint64

	// Master derives this device's identity in every mesh but its first
	// (ADR-015). Zero until a second mesh is joined.
	Master identity.Master

	// Meshes is per-mesh state, keyed by network id. Empty on a single-mesh
	// device, where the fields above are the whole story.
	Meshes map[string]*MeshState

	// Services is what peers said they publish, for the mesh this state
	// belongs to (ADR-023). Written through to the mesh entry by Save, like
	// the credential.
	Services map[string]ServiceClaim

	// Generation is the announce generation this mesh is on, mirrored onto a
	// view so the mesh that owns it can read and write it like any other
	// per-mesh field. See MeshState for what these mean.
	Generation       uint64
	GenerationSecret []byte
	PrevGeneration   uint64
	PrevSecret       []byte
	Rotation         []byte

	// owner and view are set on a View: the state this one is a window onto,
	// and the entry it stands for. Saving a view writes through to the owner,
	// so one mesh cannot overwrite another's.
	// original says this view is the device's first mesh, so the single-mesh
	// fields mirror it. False on the owner itself, which IS those fields.
	original bool

	owner *State
	view  *MeshState

	// mu serialises Save. Several meshes advance their sequence numbers
	// independently and all write the same file, and two of them saving at
	// once wrote the same temporary path and then raced to rename it — one
	// succeeded, the other failed with "no such file or directory" and lost
	// its announce. Held only on the owner; a view takes the owner's.
	mu sync.Mutex
}

// LoadOrCreateState reads device state, generating a fresh identity on first run.
func LoadOrCreateState(dir string) (*State, error) {
	// Keeps an existing pre-rename state directory, because it holds the device
	// identity. Creating a new one instead would give this node a new overlay
	// address and make it a stranger to every peer.
	dir = StateDir(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "state.json")
	recovered := false

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		id, err := identity.New()
		if err != nil {
			return nil, err
		}
		s := &State{dir: dir, Identity: id, Seq: 0}
		if err := s.Save(); err != nil {
			return nil, err
		}
		// Immediately, not on the next start. A backup written only when an
		// existing file is read leaves a node that has never restarted with no
		// copy at all — and the window between minting an identity and first
		// restarting it is exactly when a machine is being set up, plugged in
		// and moved around.
		if raw, err := os.ReadFile(path); err == nil {
			_ = writeBackupState(dir, raw)
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read state: %w", err)
	}

	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		// The identity is in here, and it is not regenerable: a fresh one has
		// no credential, cannot get one without an admin, and arrives at a
		// different overlay address. So an unreadable state.json is never a
		// reason to mint a new device — it is a reason to look for the last
		// copy that worked.
		backup, berr := os.ReadFile(backupStatePath(dir))
		if berr != nil || json.Unmarshal(backup, &sf) != nil {
			return nil, fmt.Errorf("parse state: %w.\n\n"+
				"%s is unreadable and there is no usable backup beside it. Its "+
				"contents are this device's identity, so DO NOT DELETE IT: "+
				"starting without it mints a new device, which has no "+
				"credential, cannot be given one without an admin, and joins at "+
				"a different address as a stranger to every peer. A zero-length "+
				"file here is a power loss during a write. Restore state.json "+
				"from a backup if you have one; otherwise this device has to be "+
				"enrolled again.", err, path)
		}
		// Recovered. The sequence number in a backup is behind whatever the
		// live file reached, and peers persist the highest they have seen, so
		// resuming at a lower one would have every announce refused as a replay
		// until it climbed back past. Jumping forward is free — the counter is
		// a uint64 and only has to increase — so skip well clear rather than
		// spend the outage invisible.
		sf.Seq += seqJumpAfterRecovery
		recovered = true
	}

	devPriv, err := base64.StdEncoding.DecodeString(sf.DevicePriv)
	if err != nil || len(devPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("state.json: bad device key")
	}
	wgPriv, err := base64.StdEncoding.DecodeString(sf.WGPriv)
	if err != nil || len(wgPriv) != 32 {
		return nil, errors.New("state.json: bad wireguard key")
	}

	id := &identity.Identity{
		DevicePriv: ed25519.PrivateKey(devPriv),
		DevicePub:  ed25519.PrivateKey(devPriv).Public().(ed25519.PublicKey),
	}
	copy(id.WGPriv[:], wgPriv)
	pub, err := identity.PublicFromPrivate(id.WGPriv)
	if err != nil {
		return nil, fmt.Errorf("derive wireguard public key: %w", err)
	}
	id.WGPub = pub
	// state.json has no field for the sealing key and predates it, so it is
	// derived from the device key rather than loaded. Without this the legacy
	// identity - which is the primary mesh of every existing deployment -
	// carries a zero sealing key.
	if err := id.DeriveSealing(); err != nil {
		return nil, err
	}

	st := &State{dir: dir, Identity: id, Seq: sf.Seq, Services: sf.Services}
	if sf.Master != "" {
		raw, err := base64.StdEncoding.DecodeString(sf.Master)
		if err != nil || len(raw) != identity.MasterLen {
			return nil, errors.New("state.json: bad master secret")
		}
		copy(st.Master[:], raw)
	}
	if st.Meshes, err = decodeMeshes(sf.Meshes); err != nil {
		return nil, err
	}
	if sf.Credential != "" {
		c, err := base64.StdEncoding.DecodeString(sf.Credential)
		if err != nil {
			// Not fatal: a device without a readable credential can still run,
			// and on a mesh that does not use them it is irrelevant. Losing the
			// tunnel over it would be a worse outcome than being asked to
			// re-enrol.
			return st, nil
		}
		st.Credential = c
	}
	// One write per start, and it is the difference between a power cut costing
	// a reboot and costing a re-enrolment.
	//
	// Written from the bytes that just parsed, so the copy is known-good rather
	// than re-serialised from a struct that may have dropped a field this build
	// does not know about. If we got here from the backup, the live file is the
	// broken one and gets the good state put back instead.
	if recovered {
		if err := st.Save(); err != nil {
			return nil, fmt.Errorf("recovered state.json from its backup but could not write it back: %w", err)
		}
	} else {
		_ = writeBackupState(dir, raw)
	}
	return st, nil
}

// seqJumpAfterRecovery is how far the sequence number skips when state has been
// recovered from a backup.
//
// The backup's counter is behind whatever the live file reached, and peers keep
// the highest they have accepted across restarts, so resuming below it means
// every announce is refused as a replay until the counter climbs back past —
// invisible to the mesh for exactly as long as the outage was busy. The counter
// is a uint64 that only has to increase, so jumping is free and guessing high
// is the cheap direction to be wrong in.
const seqJumpAfterRecovery = 10000

// backupStatePath is the last state.json known to have parsed.
func backupStatePath(dir string) string {
	return filepath.Join(dir, "state.json.bak")
}

// writeBackupState keeps a copy of a state file that was read successfully.
//
// Best effort throughout: this is a safety net, and a node that cannot write it
// should still run rather than refuse to start over a backup.
func writeBackupState(dir string, raw []byte) error {
	// Synced for the same reason the live file is: a backup that is present and
	// empty after a power cut is worse than no backup, because it looks like one.
	return writeFileAtomic(backupStatePath(dir), raw, 0o600)
}

// Save writes state atomically. The sequence number changes on every announce,
// so a torn write here would make the device look replayed to its peers.
func (s *State) Save() error {
	// A view writes through: it holds one mesh's fields, and the file is the
	// whole device's.
	if s.owner != nil {
		s.owner.mu.Lock()
		s.view.Seq = s.Seq
		s.view.Credential = s.Credential
		s.view.Services = s.Services
		s.view.Generation = s.Generation
		s.view.GenerationSecret = s.GenerationSecret
		s.view.PrevGeneration = s.PrevGeneration
		s.view.PrevSecret = s.PrevSecret
		s.view.Rotation = s.Rotation
		s.owner.mu.Unlock()
		return s.owner.Save()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, "state.json")

	// Ordering against other processes, not only other goroutines. The daemon
	// writes this file every announce; an admin command that writes it too has
	// only the gap between them to work in.
	if release, err := s.lockFile(); err == nil {
		defer release()
	}

	sf := stateFile{
		DevicePriv: base64.StdEncoding.EncodeToString(s.Identity.DevicePriv),
		WGPriv:     base64.StdEncoding.EncodeToString(s.Identity.WGPriv[:]),
		Seq:        s.Seq,
		Services:   s.Services,
		Meshes:     encodeMeshes(s.Meshes),
	}
	if len(s.Credential) > 0 {
		sf.Credential = base64.StdEncoding.EncodeToString(s.Credential)
	}
	if s.Master != (identity.Master{}) {
		sf.Master = base64.StdEncoding.EncodeToString(s.Master[:])
	}

	// Merge over what is on disk rather than replacing it.
	//
	// This used to serialise the in-memory state and rename it over the file,
	// which meant the last writer erased everything the other had done. The
	// daemon writes on every announce, so the daemon always won: `init --mesh`
	// created a mesh identity and enrolled it, and within 45 seconds the
	// running daemon — holding a copy loaded before that mesh existed — wrote
	// the file back without it. On the next restart the mesh had no identity,
	// so a fresh one was minted, and that identity had no credential and never
	// could: nobody had signed for it. It announced to a mesh that refused it,
	// and the only visible symptom was a node that could see its peers while
	// none of them could see it.
	if disk, err := readStateFile(path); err == nil {
		sf = mergeStateFiles(disk, sf)
	}

	raw, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	// A unique temporary name as well as the lock. The lock orders writers in
	// this process; the name means a second process — an admin command run
	// while the daemon is up — cannot delete the file this one is about to
	// rename. writeFileAtomic does both, and syncs, which is what this used to
	// get wrong.
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// NextSeq increments and persists the sequence number.
func (s *State) NextSeq() (uint64, error) {
	s.Seq++
	if err := s.Save(); err != nil {
		return 0, err
	}
	return s.Seq, nil
}

// --- config file I/O ---
//
// The format is a minimal subset of TOML: `key = value`, `#` comments, and
// string arrays. Hand-rolled rather than pulling a dependency, because the
// config is a handful of flat scalar fields and always will be — if it ever
// grows nesting, swap in a real TOML library.

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (Config, error) {
	// Resolved here rather than at every call site: this is the function that
	// touches the filesystem, so it is the one place the pre-rename fallback
	// can be applied without every command remembering to.
	path = ConfigPath(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	c, err := parseConfig(string(raw))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LoadConfigUnvalidated reads a config without checking it.
//
// For the one caller that must read a config it knows is incomplete: set-key
// operates on a `prepare`d file whose key is still the placeholder, and
// LoadConfig would refuse it for exactly that reason.
func LoadConfigUnvalidated(path string) (Config, error) {
	path = ConfigPath(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	c, err := parseConfig(string(raw))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func parseConfig(text string) (Config, error) {
	c := DefaultConfig()
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("line %d: expected key = value", n+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// mesh.<label>.<field>, the multi-mesh form (ADR-015). Checked before
		// the switch so a mesh may be called anything without colliding with a
		// top-level option name.
		if label, field, ok := parseMeshKey(key); ok {
			if err := c.setMeshField(label, field, val, n+1); err != nil {
				return c, err
			}
			continue
		}

		switch key {
		case "network_key":
			c.NetworkKey = unquote(val)
		case "name":
			c.Name = unquote(val)
		case "interface":
			c.Interface = unquote(val)
		case "preset":
			c.Preset = unquote(val)
		case "delivery_port":
			var p uint16
			if _, err := fmt.Sscanf(unquote(val), "%d", &p); err != nil {
				return c, fmt.Errorf("line %d: delivery_port: %w", n+1, err)
			}
			c.DeliveryPort = p
		case "entry_nodes":
			c.EntryNodes = parseArray(val)
		case "cluster_id":
			var cid uint16
			if _, err := fmt.Sscanf(val, "%d", &cid); err != nil {
				return c, fmt.Errorf("line %d: cluster_id: %w", n+1, err)
			}
			c.ClusterID = cid
		case "mode":
			c.Mode = unquote(val)
		case "admin_pk":
			c.AdminPK = unquote(val)
		case "admin_keys":
			c.AdminKeys = parseArray(val)
		case "relay":
			c.Relay = unquote(val) == "true"
		case "relay_addr":
			c.RelayAddr = unquote(val)
		case "relay_token":
			c.RelayToken = unquote(val)
		case "relay_blind":
			c.RelayBlind = parseArray(val)
		case "socket_group":
			c.SocketGroup = unquote(val)
		case "status_file_group":
			c.StatusFileGroup = unquote(val)
		case "status_file":
			c.StatusFile = unquote(val)
		case "ui_listen":
			// Removed. Kept as a case that ignores its value, because an
			// unknown key is fatal here — deleting it outright would stop the
			// daemon starting on any config that still names it, which is a
			// worse outcome than a setting that quietly does nothing.
			//
			// It served the status JSON over a loopback HTTP port for a viewer
			// that could use neither the unix socket nor the status file.
			// Nothing ever did: Basecamp reads the socket and the phone reads
			// neither. What it did produce was a port on a VPN daemon carrying
			// every mutating endpoint until that was noticed.
		case "manage_hosts":
			c.ManageHosts = unquote(val) == "true"
		case "hosts_suffix":
			c.HostsSuffix = unquote(val)
		case "listen_port":
			var p uint16
			if _, err := fmt.Sscanf(val, "%d", &p); err != nil {
				return c, fmt.Errorf("line %d: listen_port: %w", n+1, err)
			}
			c.ListenPort = p
		case "announce_services":
			c.AnnounceServices = unquote(val) == "true"
		case "announce_bound":
			c.AnnounceBound = unquote(val) == "true"
		case "announce_revocations":
			c.QuietRevocations = unquote(val) == "false"
		case "port_mapping":
			c.PortMapping = unquote(val) == "true"
		case "advertise":
			c.Advertise = parseArray(val)
		case "services":
			c.Services = parseArray(val)
		default:
			return c, fmt.Errorf("line %d: unknown key %q", n+1, key)
		}
	}
	return c, nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

// formatArray renders a string slice as the TOML-subset array parseArray reads.
func formatArray(vals []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vals {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", v)
	}
	b.WriteByte(']')
	return b.String()
}

func parseArray(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := unquote(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// WriteConfig writes a config file, creating the parent directory.
func WriteConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := RenderConfig(c)
	if err != nil {
		return err
	}
	// Atomically, because this file holds the network key: a write interrupted
	// halfway leaves a config that fails Validate, and a config that fails
	// Validate is a daemon that will not start. See writeFileAtomic.
	if err := writeFileAtomic(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// RenderConfig produces the file WriteConfig would write.
//
// Separate so a caller can show somebody the result before replacing their
// only config — `shrooms config flatten --dry-run` is the reason it exists.
func RenderConfig(c Config) (string, error) {
	var b strings.Builder
	b.WriteString("# logos-vpn configuration\n")
	b.WriteString("# The network key is the only secret. Anyone holding it is a mesh member.\n\n")
	// Only when there is a top-level mesh. A flattened config describes every
	// mesh under mesh.<label>, and writing network_key = "" would name a mesh
	// with no key rather than no mesh.
	if c.NetworkKey != "" {
		fmt.Fprintf(&b, "network_key = %q\n", c.NetworkKey)
	}
	fmt.Fprintf(&b, "name        = %q\n", c.Name)
	fmt.Fprintf(&b, "interface   = %q\n", c.Interface)
	fmt.Fprintf(&b, "listen_port = %d\n", c.ListenPort)
	fmt.Fprintf(&b, "preset      = %q\n", c.Preset)
	if c.ClusterID != 0 {
		b.WriteString("\n# Overrides the cluster the preset selects. Only needed when a fleet has\n")
		b.WriteString("# migrated and the library has not caught up (logos.dev is on cluster 3).\n")
		b.WriteString("# Careful: a non-zero value also activates a legacy cluster-to-network\n")
		b.WriteString("# mapping, where 2 means logos.dev regardless of the preset.\n")
		fmt.Fprintf(&b, "cluster_id  = %d\n", c.ClusterID)
	}
	if len(c.EntryNodes) > 0 {
		b.WriteString("\n# Explicit bootstrap addresses, used instead of the preset's.\n")
		fmt.Fprintf(&b, "entry_nodes = %s\n", formatArray(c.EntryNodes))
	}
	b.WriteString("\n# Pin the rendezvous node's TCP port, so this node can publish a dialable\n")
	b.WriteString("# address other members bootstrap from (ADR-031). Only useful on a Core\n")
	b.WriteString("# node that is also a relay, and therefore publicly reachable. Unset means\n")
	b.WriteString("# the library picks, which is right for anything nobody dials.\n")
	if c.DeliveryPort != 0 {
		fmt.Fprintf(&b, "delivery_port = %d\n", c.DeliveryPort)
	} else {
		b.WriteString("# delivery_port = 30304\n")
	}
	b.WriteString("\n# Core relays for the whole cluster (~20 MB/h measured idle, most of it\n")
	b.WriteString("# other applications' traffic); Edge subscribes and forwards nothing\n")
	b.WriteString("# (~3 MB/h). Use Edge on anything metered or battery-powered.\n")
	fmt.Fprintf(&b, "mode        = %q\n", c.Mode)
	if c.Relay {
		b.WriteString("\n# This node forwards traffic for peers that cannot reach each other.\n")
		b.WriteString("relay = \"true\"\n")
	}
	if c.RelayAddr != "" {
		b.WriteString("\n# Pins a relay that is a member of this mesh, overriding discovery. Not\n")
		b.WriteString("# normally needed: member relays are found from their announces.\n")
		fmt.Fprintf(&b, "relay_addr  = %q\n", c.RelayAddr)
	}
	if len(c.RelayBlind) > 0 {
		b.WriteString("\n# Relays run by people who are not on this mesh. They cannot announce\n")
		b.WriteString("# themselves - no network key, no delivery node - so they are configured.\n")
		b.WriteString("# This device registers with at most two, in the order given.\n")
		b.WriteString("relay_blind = [")
		for i, one := range c.RelayBlind {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", one)
		}
		b.WriteString("]\n")
	}
	if c.RelayToken != "" {
		b.WriteString("\n# Handed out by whoever runs a blind relay. Not a secret protecting\n")
		b.WriteString("# anything of ours: it says an operator agreed to carry us.\n")
		fmt.Fprintf(&b, "relay_token = %q\n", c.RelayToken)
	}
	if c.StatusFile != "" {
		b.WriteString("\n# Write the status JSON here for a monitoring view.\n")
		fmt.Fprintf(&b, "status_file = %q\n", c.StatusFile)
		b.WriteString("# The group allowed to read it. The daemon runs as root, so without\n")
		b.WriteString("# this only root can read the file and a desktop view cannot.\n")
		if c.StatusFileGroup != "" {
			fmt.Fprintf(&b, "status_file_group = %q\n", c.StatusFileGroup)
		} else {
			b.WriteString("# status_file_group = \"your-username\"\n")
		}
	}
	b.WriteString("\n# Keep /etc/hosts current as peers come and go, so `ssh vps.mesh` works\n")
	b.WriteString("# without re-running anything. Off by default: this edits a system file.\n")
	if c.ManageHosts {
		b.WriteString("manage_hosts = \"true\"\n")
	} else {
		b.WriteString("# manage_hosts = \"true\"\n")
	}
	b.WriteString("\n# Let this mesh's peers see which service names this device publishes,\n")
	b.WriteString("# so their roster can show what the mesh offers. Off by default: a member\n")
	b.WriteString("# can already find them by scanning, but the names carry intent, and a\n")
	b.WriteString("# mesh shared with other people is where that matters.\n")
	if c.AnnounceServices {
		b.WriteString("announce_services = \"true\"\n")
	} else {
		b.WriteString("# announce_services = \"true\"\n")
	}
	b.WriteString("\n# Also tell them which ports are listening on this device's mesh address.\n")
	b.WriteString("# Those are reachable by members and by nobody else — binding a service to\n")
	b.WriteString("# the mesh address is what makes it mesh-only — so this lists what is\n")
	b.WriteString("# already there. Discovered rather than declared, so it announces whatever\n")
	b.WriteString("# happens to be bound.\n")
	if c.AnnounceBound {
		b.WriteString("announce_bound = \"true\"\n")
	} else {
		b.WriteString("# announce_bound = \"true\"\n")
	}
	b.WriteString("\n# Repeat what this mesh's admin has withdrawn: once an epoch, and once when\n")
	b.WriteString("# a peer appears. Every receiver checks the admin signature itself, so\n")
	b.WriteString("# repeating is always safe, and it is what makes a revocation reach a node\n")
	b.WriteString("# that was offline when it was published. Turn it off to save a few small\n")
	b.WriteString("# messages on a metered uplink.\n")
	if c.QuietRevocations {
		b.WriteString("announce_revocations = \"false\"\n")
	} else {
		b.WriteString("# announce_revocations = \"false\"\n")
	}
	b.WriteString("\n# Ask the router (PCP, NAT-PMP) to open this node's port, so a machine\n")
	b.WriteString("# behind a home NAT can be dialled without a forwarding rule. Best effort:\n")
	b.WriteString("# a router that refuses costs one request at startup.\n")
	if c.PortMapping {
		b.WriteString("# port_mapping = \"false\"\n")
	} else {
		b.WriteString("port_mapping = \"false\"\n")
	}
	b.WriteString("\n# Extra endpoints to announce. Usually unnecessary: interface addresses\n")
	b.WriteString("# are announced automatically and a NATed node learns its public address\n")
	b.WriteString("# from its peers. Set this only for a port forward, or a cloud instance\n")
	b.WriteString("# whose public IP is not on any local interface.\n")
	if len(c.Advertise) == 0 {
		b.WriteString("# advertise = [\"203.0.113.4:51820\"]\n")
	} else {
		b.WriteString("advertise = [")
		for i, a := range c.Advertise {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", a)
		}
		b.WriteString("]\n")
	}

	if len(c.AdminKeys) > 0 {
		b.WriteString("\n# The admin keys this mesh trusts to sign membership. Public values,\n")
		b.WriteString("# fixed when the mesh was minted: the mesh id commits to the set.\n")
		fmt.Fprintf(&b, "admin_keys = %s\n", formatArray(c.AdminKeys))
	}

	// Additional meshes (ADR-015). The parser has read these since multi-mesh
	// landed and this did not write them, so a config built in memory and
	// written out came back with no meshes at all — reported, accurately and
	// unhelpfully, as "network_key is not set".
	if len(c.MeshSet) > 0 {
		labels := make([]string, 0, len(c.MeshSet))
		for label := range c.MeshSet {
			labels = append(labels, label)
		}
		sort.Strings(labels) // stable across writes, so diffs mean something
		for _, label := range labels {
			m := c.MeshSet[label]
			b.WriteString("\n# A mesh this node belongs to. Its own key, identity, interface and port.\n")
			fmt.Fprintf(&b, "mesh.%s.key   = %q\n", label, m.NetworkKey)
			if m.Disabled {
				fmt.Fprintf(&b, "mesh.%s.enabled = \"false\"\n", label)
			}
			if m.Relay {
				fmt.Fprintf(&b, "mesh.%s.relay = \"true\"\n", label)
			}
			if len(m.AdminKeys) > 0 {
				fmt.Fprintf(&b, "mesh.%s.admin_keys = %s\n", label, formatArray(m.AdminKeys))
			}
			if len(m.Services) > 0 {
				fmt.Fprintf(&b, "mesh.%s.services = %s\n", label, formatArray(m.Services))
			}
			// Pinned by `shrooms mesh rename`, so that renaming a mesh cannot
			// move its interface or its port. Absent means derived from
			// position, which is what every config did before.
			if m.Interface != "" {
				fmt.Fprintf(&b, "mesh.%s.iface = %q\n", label, m.Interface)
			}
			if m.ListenPort != 0 {
				fmt.Fprintf(&b, "mesh.%s.port  = %q\n", label, strconv.FormatUint(uint64(m.ListenPort), 10))
			}
			// Written only when set, like every other per-mesh key: the
			// default is off, and a config full of "false" reads as though
			// somebody decided each one.
			if m.AnnounceServices {
				fmt.Fprintf(&b, "mesh.%s.announce_services = \"true\"\n", label)
			}
			if m.AnnounceBound {
				fmt.Fprintf(&b, "mesh.%s.announce_bound = \"true\"\n", label)
			}
			// Inverted, matching how it is read: the useful default is on, so
			// the line exists only to turn repeating off.
			//
			// Parsed since it was added and never written until now, so a node
			// that set it per mesh lost the setting the next time anything
			// rewrote the config — silently, and in the direction of more
			// traffic on a link somebody was counting bytes on.
			if m.QuietRevocations {
				fmt.Fprintf(&b, "mesh.%s.announce_revocations = \"false\"\n", label)
			}
			// This mesh's own public endpoint. Not inherited from the device,
			// because the value names a port and only one mesh listens there.
			if len(m.Advertise) > 0 {
				fmt.Fprintf(&b, "mesh.%s.advertise = %s\n", label, formatArray(m.Advertise))
			}
			// Relay overrides. Absent means the device's settings apply, which
			// is the useful default; "none" is how a mesh opts out without
			// naming a relay of its own.
			if m.RelayNone {
				fmt.Fprintf(&b, "mesh.%s.relay_blind = %q\n", label, "none")
			} else if len(m.RelayBlind) > 0 {
				fmt.Fprintf(&b, "mesh.%s.relay_blind = %s\n", label, formatArray(m.RelayBlind))
			}
			if m.RelayAddr != "" {
				fmt.Fprintf(&b, "mesh.%s.relay_addr = %q\n", label, m.RelayAddr)
			}
			if m.RelayToken != "" {
				fmt.Fprintf(&b, "mesh.%s.relay_token = %q\n", label, m.RelayToken)
			}
			// Which mesh holds the device keys from before multi-mesh. Stated
			// rather than inferred from the config's shape, so the shape can
			// stop mattering.
			if m.InheritsIdentity {
				fmt.Fprintf(&b, "mesh.%s.inherits_identity = \"true\"\n", label)
			}
		}
	}

	b.WriteString("\n# The group allowed to use the control socket. The daemon runs as root,\n")
	b.WriteString("# so without this every `shrooms status` needs sudo.\n")
	if c.SocketGroup != "" {
		fmt.Fprintf(&b, "socket_group = %q\n", c.SocketGroup)
	} else {
		b.WriteString("# socket_group = \"your-username\"\n")
	}

	b.WriteString("\n# Publish local ports on the mesh under their own names. \"immich:2283\"\n")
	b.WriteString("# makes this machine's port 2283 reachable from every device as\n")
	b.WriteString("# immich.<this-device>.mesh:2283 — no port to remember, and it works even\n")
	b.WriteString("# when the application binds 0.0.0.0 and would never accept an IPv6\n")
	b.WriteString("# connection, because the daemon forwards to loopback. Add \"->port\" or\n")
	b.WriteString("# \"->host:port\" when the application listens somewhere else.\n")
	if len(c.Services) == 0 {
		b.WriteString("# services = [\"immich:2283\", \"jellyfin:8096\"]\n")
	} else {
		fmt.Fprintf(&b, "services = %s\n", formatArray(c.Services))
	}

	return b.String(), nil
}
