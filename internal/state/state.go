// Package state handles on-disk configuration and persistent device state.
//
// Two files, deliberately separated by who writes them:
//
//   - config.toml   — human-edited. The network key and a few knobs.
//   - state.json    — daemon-owned. Device keys and the announce sequence
//     number. Never edited by hand.
package state

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// Default locations. Overridable so a developer can run several nodes on one
// machine without root.
const (
	DefaultConfigPath = "/etc/logos-vpn/config.toml"
	DefaultStateDir   = "/var/lib/logos-vpn"
)

// Config is the human-edited configuration.
type Config struct {
	// NetworkKey is the mesh secret, base32. In v1 it is a bearer credential:
	// holding it makes you a member.
	NetworkKey string

	// Name is self-asserted and appears in the signed announce. It
	// authenticates only as "the device holding key X calls itself this".
	Name string

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

	// Preset selects the Waku network. Use "logos.dev", NOT clusterId — only
	// the preset loads the fleet's entry nodes.
	Preset string

	// Mode is the Waku node mode: Core on always-on machines.
	Mode string

	// Relay makes this node forward traffic for peers that cannot reach each
	// other directly. Only useful on a node with a reachable address.
	Relay bool

	// RelayAddr is the relay to fall back to when no direct path exists.
	// Discovered via RelayAnnounce once that lands; configured for now.
	RelayAddr string

	// ManageHosts lets the daemon keep /etc/hosts current as the roster
	// changes, so peers stay reachable by name without re-running anything.
	//
	// Off by default: a VPN silently editing a system file that cloud-init,
	// NetworkManager and others also touch is a surprise, and it should be the
	// operator's choice. The DNS server (M6) removes the need for it.
	ManageHosts bool

	// HostsSuffix is the domain appended to peer names.
	HostsSuffix string

	// AdminPK is reserved for M5 (admin-signed credentials) and ignored while
	// empty. Present now so adding it later is not a config break.
	AdminPK string
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
		Interface:   "logos0",
		Preset:      "logos.dev",
		Mode:        "Core",
		HostsSuffix: "mesh",
	}
}

// Validate checks a loaded config.
func (c *Config) Validate() error {
	if c.NetworkKey == "" {
		return errors.New("network_key is not set — run `logos-vpn init` or `logos-vpn join`")
	}
	if _, err := identity.ParseNetworkKey(c.NetworkKey); err != nil {
		return fmt.Errorf("network_key: %w", err)
	}
	if c.Name == "" {
		return errors.New("name is empty")
	}
	if c.ListenPort == 0 {
		return errors.New("listen_port is 0")
	}
	if c.Interface == "" {
		return errors.New("interface is empty")
	}
	if c.Preset == "" {
		return errors.New("preset is empty")
	}
	return nil
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
}

// State is the daemon-owned persistent state.
type State struct {
	dir string

	Identity *identity.Identity

	// Seq is the announce sequence number. It MUST persist across restarts: a
	// device that restarts and resets Seq to 1 is rejected by every peer's
	// ReplayGuard until they forget it, which looks exactly like the device
	// having vanished.
	Seq uint64
}

// LoadOrCreateState reads device state, generating a fresh identity on first run.
func LoadOrCreateState(dir string) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "state.json")

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
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read state: %w", err)
	}

	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
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

	return &State{dir: dir, Identity: id, Seq: sf.Seq}, nil
}

// Save writes state atomically. The sequence number changes on every announce,
// so a torn write here would make the device look replayed to its peers.
func (s *State) Save() error {
	sf := stateFile{
		DevicePriv: base64.StdEncoding.EncodeToString(s.Identity.DevicePriv),
		WGPriv:     base64.StdEncoding.EncodeToString(s.Identity.WGPriv[:]),
		Seq:        s.Seq,
	}
	raw, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := filepath.Join(s.dir, "state.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
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

		switch key {
		case "network_key":
			c.NetworkKey = unquote(val)
		case "name":
			c.Name = unquote(val)
		case "interface":
			c.Interface = unquote(val)
		case "preset":
			c.Preset = unquote(val)
		case "mode":
			c.Mode = unquote(val)
		case "admin_pk":
			c.AdminPK = unquote(val)
		case "relay":
			c.Relay = unquote(val) == "true"
		case "relay_addr":
			c.RelayAddr = unquote(val)
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
		case "advertise":
			c.Advertise = parseArray(val)
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

	var b strings.Builder
	b.WriteString("# logos-vpn configuration\n")
	b.WriteString("# The network key is the only secret. Anyone holding it is a mesh member.\n\n")
	fmt.Fprintf(&b, "network_key = %q\n", c.NetworkKey)
	fmt.Fprintf(&b, "name        = %q\n", c.Name)
	fmt.Fprintf(&b, "interface   = %q\n", c.Interface)
	fmt.Fprintf(&b, "listen_port = %d\n", c.ListenPort)
	fmt.Fprintf(&b, "preset      = %q\n", c.Preset)
	fmt.Fprintf(&b, "mode        = %q\n", c.Mode)
	if c.Relay {
		b.WriteString("\n# This node forwards traffic for peers that cannot reach each other.\n")
		b.WriteString("relay = \"true\"\n")
	}
	if c.RelayAddr != "" {
		fmt.Fprintf(&b, "relay_addr  = %q\n", c.RelayAddr)
	}
	b.WriteString("\n# Keep /etc/hosts current as peers come and go, so `ssh vps.mesh` works\n")
	b.WriteString("# without re-running anything. Off by default: this edits a system file.\n")
	if c.ManageHosts {
		b.WriteString("manage_hosts = \"true\"\n")
	} else {
		b.WriteString("# manage_hosts = \"true\"\n")
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

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
