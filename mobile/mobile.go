// Package mobile is the Kotlin-facing surface of the mesh, bound with
// `gomobile bind`.
//
// Deliberately narrow. gomobile supports a restricted type set — no maps, no
// slices of structs, no channels — and every type crossing the boundary becomes
// a generated Java class to keep in step. So the boundary is start, stop, a
// JSON snapshot, and setup; everything else stays inside Go, where it is
// already tested.
//
// Status is returned as JSON rather than as bound types precisely so that
// adding a field is not an API change on both sides. It is the same payload the
// CLI's `status --json` produces, so the app and the CLI read one schema.
package mobile

import (
	"context"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/vpavlin/shrooms/internal/cred"
	dnssrv "github.com/vpavlin/shrooms/internal/dns"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/rendezvous"
	"github.com/vpavlin/shrooms/internal/state"
	"github.com/vpavlin/shrooms/internal/v4"
	"github.com/vpavlin/shrooms/internal/waku"
	"github.com/vpavlin/shrooms/internal/wg"
)

// Protector is implemented by the Android side, forwarding to
// VpnService.protect.
//
// A socket created inside a VpnService routes through the tunnel. Ours carries
// the rendezvous and disco traffic the tunnel depends on, so an unprotected one
// makes the interface feed itself — and it fails silently. See ADR-016.
type Protector interface {
	Protect(fd int) bool
}

// Logger lets the app see what the daemon is doing without a log file.
type Logger interface {
	Log(level, message string)
}

var (
	mu      sync.Mutex
	running *session

	// node outlives a disconnect, deliberately.
	//
	// liblogosdelivery keeps process-global state that its destroy does not
	// release: creating a second node in the same process fails with
	// "Failed to initialize persistency instance - persistency already
	// initialized". On a phone, connect/disconnect/connect is the normal thing
	// to do, so the node is created once and only stopped and started again.
	//
	// The cost is that it is never destroyed while the process lives, which is
	// what Android does to processes anyway.
	node *waku.Node
)

type session struct {
	name    string
	suffix  string
	overlay string
	prefix  string

	// instances is one per mesh (ADR-015). The first is what the single-mesh
	// fields above describe, so an app that knows nothing about several meshes
	// sees what it always saw.
	instances []*meshInstance
	mux       *wg.Mux

	// st is the state this session loaded. Edits made while it runs must go
	// through it: a second State loaded from disk writes the whole file on its
	// next save, so anything it deleted comes back the moment a mesh advances
	// its sequence number.
	st *state.State

	cancel context.CancelFunc
	done   chan struct{}

	// dnsIntercept and dnsServer are kept so status can report whether queries
	// are arriving at all.
	//
	// "Names do not resolve" has three completely different causes that look
	// identical from the outside: the query never reaches us, it reaches us and
	// we refuse it, or we answer and the platform ignores the reply. Counting
	// at both layers separates them, and guessing between them wasted a day.
	dnsIntercept *dnssrv.Intercept
	dnsServer    *dnssrv.Server
}

// b32 matches how admin keys are written in a config: uppercase, unpadded.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// tap lets a join listen to the running node. See eventTap.
var tap eventTap

// meshInstance is one mesh running on the phone: its own WireGuard device and
// identity, on a virtual TUN carved out of the single descriptor Android gives.
type meshInstance struct {
	label   string
	mesh    *mesh.Mesh
	dev     *wg.Device
	aliases *v4.Table
	self    netip.Addr
	prefix  netip.Prefix
}

// Init creates a new mesh and returns its network key.
func Init(name, configDir string) (string, error) {
	cfg, _, err := setup(configDir, name, "")
	if err != nil {
		return "", err
	}
	return cfg.NetworkKey, nil
}

// Join configures this device as a member of an existing mesh.
func Join(key, name, configDir string) error {
	_, _, err := setup(configDir, name, key)
	return err
}

// JoinWithInvite redeems an invite token (ADR-017): the phone's way onto a mesh
// that admits devices by credential rather than by holding the network key.
//
// Blocking, so Kotlin must call it off the main thread. It runs a rendezvous
// node for the length of the exchange and throws it away — the app's own node
// is not running yet, since there is nothing to run it for until this returns.
//
// On success the device has a config, the mesh's admin keys, and a credential
// signed for the keys generated here. On failure it has written nothing.
func JoinWithInvite(token, name, configDir string, timeoutSeconds int) error {
	cfgPath, _ := paths(configDir)
	if _, err := os.Stat(cfgPath); err == nil {
		return errors.New("this device has already joined a mesh")
	}
	return joinInvite(token, name, "", configDir, timeoutSeconds)
}

// joinInvite is the exchange itself. label empty writes the single-mesh form;
// otherwise the mesh is added alongside whatever is already there.
func joinInvite(token, name, label, configDir string, timeoutSeconds int) error {
	cfgPath, stateDir := paths(configDir)
	secret, err := invite.ParseToken(token)
	if err != nil {
		return err
	}
	if timeoutSeconds <= 0 || timeoutSeconds > 900 {
		timeoutSeconds = 120
	}

	// The identity first: it is what the credential names, and generating it
	// here means the keys in the request are the keys this device keeps.
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if name == "" {
		name = "phone"
	}

	// The node this device is already using, if it has one.
	//
	// Starting a second one here did not work and could not: the library has a
	// single global event callback, so a join attempted while the mesh was up
	// simply never heard an answer. It also used the shipped fleet defaults
	// rather than this device's, which is a second way to hear nothing.
	mu.Lock()
	live := node
	running := running != nil
	mu.Unlock()

	var tr invite.Transport
	if live != nil && running {
		msgs, detach := tap.attach()
		defer detach()
		tr = tapTransport{node: live, msgs: msgs}
	} else {
		fleet := state.DefaultConfig()
		if onDisk, err := state.LoadConfigUnvalidated(cfgPath); err == nil {
			if onDisk.Preset != "" {
				fleet.Preset = onDisk.Preset
			}
			if onDisk.Mode != "" {
				fleet.Mode = onDisk.Mode
			}
			fleet.ClusterID = onDisk.ClusterID
			fleet.EntryNodes = onDisk.EntryNodes
		}
		own, err := waku.New(nodeConfig(fleet))
		if err != nil {
			return fmt.Errorf("rendezvous plane: %w", err)
		}
		defer own.Close()
		if err := own.Start(); err != nil {
			return fmt.Errorf("start rendezvous plane: %w", err)
		}
		tr = rendezvous.InviteTransport(own)
	}
	// Nothing arrives until the node has peers. The inviter is waiting at a
	// prompt, so a few seconds here costs nothing and asking too early costs a
	// retry interval.
	time.Sleep(3 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	base := &invite.Request{
		DevicePub: st.Identity.DevicePub,
		WGPub:     st.Identity.WGPub[:],
		Name:      name,
	}
	// An additional mesh takes two rounds: this device cannot derive the
	// identity it will use on a mesh it has not been told about yet (ADR-017).
	// Without it a phone on two invited meshes announces the same device key to
	// both, which is visible to anyone in both and is what per-mesh identities
	// exist to prevent. The first mesh keeps the base identity, because that is
	// what the single-mesh config form means.
	var resp *invite.Response
	if label != "" && label != state.DefaultLabel {
		resp, err = invite.RedeemForMesh(ctx, tr, secret, base,
			func(r *invite.Response) (*invite.Request, error) {
				nk, err := identity.NetworkKeyFromBytes(r.NetworkKey)
				if err != nil {
					return nil, err
				}
				ms, err := st.MeshState(state.NetworkID(nk), false)
				if err != nil {
					return nil, err
				}
				return &invite.Request{
					DevicePub: ms.Identity.DevicePub,
					WGPub:     ms.Identity.WGPub[:],
					Name:      name,
				}, nil
			})
	} else {
		resp, err = invite.Redeem(ctx, tr, secret, base)
	}
	if err != nil {
		return errors.New("no answer — is `shrooms invite` still running on the other machine?")
	}

	nk, err := identity.NetworkKeyFromBytes(resp.NetworkKey)
	if err != nil {
		return err
	}
	// An additional mesh keeps everything already configured and adds itself;
	// a first one writes the single-mesh form every config in the field uses.
	cfg := state.DefaultConfig()
	if existing, err := state.LoadConfig(cfgPath); err == nil {
		cfg = existing
	}
	// The device name is config-wide, so setting it here would rename this
	// device on every mesh it is already on — which is what happened: a phone
	// called "nothing" became "a063" on the mesh it had been using for weeks.
	if label == "" {
		cfg.Name = name
	}
	if label != "" {
		if cfg.MeshSet == nil {
			cfg.MeshSet = map[string]state.Mesh{}
		}
	} else {
		cfg.NetworkKey = nk.String()
	}
	if resp.Suffix != "" {
		cfg.HostsSuffix = resp.Suffix
	}
	var adminKeys []string
	for _, k := range resp.AdminKeys {
		if len(k) != ed25519.PublicKeySize {
			return errors.New("the invitation carried a malformed admin key")
		}
		adminKeys = append(adminKeys, b32.EncodeToString(k))
	}
	if label != "" {
		cfg.MeshSet[label] = state.Mesh{
			Label: label, NetworkKey: nk.String(), AdminKeys: adminKeys,
		}
	} else {
		cfg.AdminKeys = adminKeys
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// The credential is checked before anything is written, so a failed join
	// leaves the device exactly as it was rather than half-joined.
	if len(resp.Credential) > 0 {
		c, err := cred.UnmarshalCredential(resp.Credential)
		if err != nil {
			return fmt.Errorf("the invitation carried a malformed credential: %w", err)
		}
		// The authority of the mesh being joined, which on an additional mesh
		// is not the config's top-level one.
		auth, err := cfg.Authority()
		if label != "" {
			auth, err = cfg.MeshSet[label].Authority()
		}
		if err != nil {
			return err
		}
		if auth != nil {
			if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
				return fmt.Errorf("the credential does not verify: %w", err)
			}
		}
	}

	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if len(resp.Credential) > 0 {
		// Stored against the identity the credential actually names. The first
		// mesh keeps this device's base identity, which is what the single-mesh
		// config form means; an additional mesh uses the identity derived in
		// the second round of the exchange (ADR-017). Getting this flag wrong
		// derives a fresh identity beside a credential naming another, and
		// every peer then refuses this device — correctly, and silently.
		legacy := label == "" || label == state.DefaultLabel
		if err := st.SetMeshCredentialFor(state.NetworkID(nk), legacy, resp.Credential); err != nil {
			return err
		}
	}
	return nil
}

// InviteToken reports whether scanned text is an enrolment invite, and returns
// it in canonical form. Empty means it is not one — it may still be a network
// key, which InviteKey handles.
func InviteToken(scanned string) string {
	s, err := invite.ParseToken(scanned)
	if err != nil {
		return ""
	}
	return s.String()
}

// Configured reports whether this device has already been set up, so the app
// knows whether to show the join screen.
func Configured(configDir string) bool {
	_, err := os.Stat(filepath.Join(configDir, "config.toml"))
	return err == nil
}

// OverlayAddress returns this device's address on the mesh, or "" if not yet
// configured. Derived from the device key, so it is stable for the life of the
// installation.
func OverlayAddress(configDir string) string {
	cfg, st, err := load(configDir)
	if err != nil {
		return ""
	}
	nk, err := cfg.Key()
	if err != nil {
		return ""
	}
	return identity.OverlayAddr(nk, st.Identity.DevicePub).String()
}

// OverlayV4 returns this device's synthetic IPv4 address, or "" if the device
// is not configured (ADR-021).
//
// The VpnService needs it before anything starts: an address the interface does
// not hold is an address the operating system will not send us packets for.
func OverlayV4(configDir string) string {
	cfg, st, err := load(configDir)
	if err != nil {
		return ""
	}
	nk, err := cfg.Key()
	if err != nil {
		return ""
	}
	t := v4.NewTable(state.NetworkID(nk), v4.Entry{
		Overlay:   identity.OverlayAddr(nk, st.Identity.DevicePub),
		DevicePub: st.Identity.DevicePub,
	}, nil)
	return t.Self().String()
}

// liveState is the state the running session loaded, or a fresh one.
//
// Anything that edits state while the mesh is up must use this. Two State
// values over one file do not merge: each writes the whole thing, so an edit
// made through a second copy is undone by the first mesh to advance its
// sequence number — which is every 45 seconds.
func liveState(stateDir string) *state.State {
	mu.Lock()
	s := running
	mu.Unlock()
	if s != nil && s.st != nil {
		return s.st
	}
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return nil
	}
	return st
}

// SetMeshEnabled switches a mesh on or off without leaving it.
//
// Takes effect on the next connect: the tunnel's addresses and routes are
// built when it comes up.
func SetMeshEnabled(configDir, label string, enabled bool) error {
	cfgPath, _ := paths(configDir)
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		return fmt.Errorf("no mesh called %q, or it is the original one", label)
	}
	m.Disabled = !enabled
	cfg.MeshSet[label] = m
	return state.WriteConfig(cfgPath, cfg)
}

// LeaveMesh removes one mesh from this device, keeping the others.
//
// The way back from a join that went wrong. Without it a device with a broken
// mesh entry — a credential stored against the wrong identity, say — had no
// remedy but clearing the app's data, which throws away the identity of every
// other mesh with it.
//
// Refuses the last one: leaving that is "forget everything", which is what
// clearing app data is for, and doing it by accident is expensive.
func LeaveMesh(configDir, label string) error {
	cfgPath, stateDir := paths(configDir)
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	if len(cfg.Meshes()) < 2 {
		return errors.New("this is the only mesh; clear the app's data to start over")
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		return fmt.Errorf("no mesh called %q, or it is the original one", label)
	}
	nk, err := m.Key()
	if err == nil {
		// The state entry too, so re-joining starts clean rather than
		// inheriting whatever was wrong — through the running session's state
		// if there is one, because a second copy loaded from disk would be
		// overwritten wholesale the next time a mesh saved.
		if st := liveState(stateDir); st != nil {
			delete(st.Meshes, state.NetworkID(nk))
			_ = st.Save()
		}
	}
	delete(cfg.MeshSet, label)
	return state.WriteConfig(cfgPath, cfg)
}

// DeviceName is what this device calls itself on every mesh it is on.
func DeviceName(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	return cfg.Name
}

// SetDeviceName renames this device.
//
// One name for every mesh, because that is what the config holds and what the
// announce carries. Renaming takes effect on the next connect: the name rides
// the announce, and peers keep the old one until they hear a new one.
func SetDeviceName(configDir, name string) error {
	cfgPath, _ := paths(configDir)
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a device needs a name")
	}
	if name == cfg.Name {
		return nil
	}
	cfg.Name = name
	if err := cfg.Validate(); err != nil {
		return err
	}
	return state.WriteConfig(cfgPath, cfg)
}

// MeshesJSON lists every mesh this device belongs to, with the address and
// routes VpnService.Builder must install for it (ADR-015).
//
// One call rather than several, because the Builder needs all of it before the
// tunnel exists, and a phone with two meshes that routed only one would look
// like a mesh that silently does not work.
func MeshesJSON(configDir string) string {
	cfg, st, err := load(configDir)
	if err != nil {
		return "[]"
	}
	type entry struct {
		Label    string `json:"label"`
		Disabled bool   `json:"disabled,omitempty"`
		Overlay  string `json:"overlay"`
		Prefix   string `json:"prefix"`
		V4       string `json:"v4"`
		V4Block  string `json:"v4_block"`
	}
	var out []entry
	for _, m := range cfg.Meshes() {
		nk, err := m.Key()
		if err != nil {
			continue
		}
		networkID := state.NetworkID(nk)
		ms, err := st.MeshState(networkID, cfg.NetworkKey != "" && m.Label == state.DefaultLabel)
		if err != nil {
			continue
		}
		self := identity.OverlayAddr(nk, ms.Identity.DevicePub)
		aliases := v4.NewTable(networkID, v4.Entry{Overlay: self, DevicePub: ms.Identity.DevicePub}, nil)
		// Reported for a mesh that is switched off as well as one that is on.
		// The identity and the address exist either way — they are derived, not
		// allocated — and hiding them made a mesh look half-forgotten rather
		// than simply not running.
		out = append(out, entry{
			Label:    m.Label,
			Disabled: m.Disabled,
			Overlay:  self.String(),
			Prefix:   nk.Prefix().String(),
			V4:       aliases.Self().String(),
			V4Block:  aliases.Block().String(),
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// JoinAnotherWithInvite redeems an invite for an ADDITIONAL mesh, keeping the
// ones this device already has.
//
// label is what this phone calls it. There is no way to learn it later: mesh
// names are local and never travel on the wire.
func JoinAnotherWithInvite(token, label, name, configDir string, timeoutSeconds int) error {
	cfg, _, err := load(configDir)
	if err != nil {
		return fmt.Errorf("this device has no mesh yet; use JoinWithInvite: %w", err)
	}
	if label == "" {
		return errors.New("an additional mesh needs a name to tell it apart")
	}
	for _, m := range cfg.Meshes() {
		if m.Label == label {
			return fmt.Errorf("this device already has a mesh called %q", label)
		}
	}
	return joinInvite(token, name, label, configDir, timeoutSeconds)
}

// OverlayV4Range is the range the synthetic addresses come from, as
// "address/bits", for the route the VpnService has to install.
func OverlayV4Range() string {
	// The whole range: the app has one mesh, and routing the umbrella is
	// correct while that is true. A multi-mesh app must route each mesh's
	// block separately, as the daemon does.
	return v4.Prefix.String()
}

// NetworkKey returns the mesh key, for showing a QR code to another device.
func NetworkKey(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	return cfg.NetworkKey
}

// Mode returns the rendezvous node mode, "Core" or "Edge", or "" if the device
// is not configured yet.
func Mode(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	return cfg.Mode
}

// SetMode changes the node mode and persists it.
//
// Core relays for the whole cluster and cost ~20 MB/h measured idle, most of it
// other applications' traffic; Edge subscribes and forwards nothing, at
// ~3 MB/h. On a phone that difference is a data plan and a battery, so the
// setting has to be reachable without editing a file — which on Android is not
// a thing anyone can do.
//
// Takes effect on the next connect: the node is built at Start, and rebuilding
// it under a live tunnel would drop the tunnel to change a preference.
func SetMode(configDir, mode string) error {
	cfg, _, err := load(configDir)
	if err != nil {
		return err
	}
	switch mode {
	case state.ModeCore, state.ModeEdge:
	default:
		return fmt.Errorf("mode %q is not a node mode", mode)
	}
	cfg.Mode = mode
	cfgPath, _ := paths(configDir)
	return state.WriteConfig(cfgPath, cfg)
}

// AnnounceServices reports whether this device tells its peers which service
// names it publishes (ADR-023).
func AnnounceServices(configDir string) bool {
	cfg, _, err := load(configDir)
	if err != nil {
		return false
	}
	return cfg.AnnounceServices
}

// SetAnnounceServices turns that on or off.
//
// Reachable from the phone because it is a disclosure decision, and the person
// making it is holding the phone rather than editing a config file. It applies
// on the next connect, like the node mode: the list goes out on a timer the
// running session owns.
//
// A phone rarely publishes services itself, so this mostly matters as the
// setting people find while wondering why a peer's services are visible and
// theirs are not.
func SetAnnounceServices(configDir string, on bool) error {
	cfg, _, err := load(configDir)
	if err != nil {
		return err
	}
	cfg.AnnounceServices = on
	cfgPath, _ := paths(configDir)
	return state.WriteConfig(cfgPath, cfg)
}

// fwd is the live DNS forwarder, so its upstream list can be replaced without
// restarting the tunnel.
var fwd atomic.Pointer[forwarder]

// SetDNSServers replaces the upstream resolvers used for names that are not
// ours, as a comma-separated list from ConnectivityManager.
//
// Must be called whenever the underlying network changes. The list captured at
// connect belongs to the network the phone was on then; after roaming, every
// non-mesh query fails slowly against resolvers that are no longer reachable,
// and Android responds by dropping our resolver altogether — which takes .mesh
// names with it, since the same resolver serves them.
//
// Returns false if the list held nothing usable, in which case the previous one
// is kept: a phone between networks reports no resolvers for a moment, and
// forwarding to nothing is worse than forwarding to something stale.
func SetDNSServers(servers string) bool {
	f := fwd.Load()
	if f == nil {
		return false
	}
	return f.Set(servers)
}

// Start brings the mesh up on a TUN descriptor from VpnService.Builder.
//
// The descriptor is dup'd, because Go's os.File takes ownership and would close
// the caller's copy — leaving Android holding a closed interface.
func Start(tunFd int, configDir string, dnsServers string, p Protector, l Logger) error {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return errors.New("already running")
	}

	cfg, st, err := load(configDir)
	if err != nil {
		return err
	}
	log := slog.New(newBridge(l))

	dupped, err := dupFd(tunFd)
	if err != nil {
		return fmt.Errorf("dup tun fd: %w", err)
	}

	// CreateUnmonitoredTUNFromFD, not CreateTUNFromFile.
	//
	// The latter also opens a netlink socket to watch for route and MTU
	// changes, which an ordinary Android app has no privilege for: it fails
	// with EPERM, reported as "tun from fd: permission denied" on a descriptor
	// that is perfectly usable. The unmonitored variant is what
	// wireguard-android uses, and the monitoring is not wanted here anyway —
	// the interface is ours and its MTU is fixed.
	//
	// It takes ownership of the descriptor, which is why we hand it the dup
	// rather than Android's copy.
	rawTun, _, err := tun.CreateUnmonitoredTUNFromFD(dupped)
	if err != nil {
		syscall.Close(dupped)
		return fmt.Errorf("tun from fd %d: %w", tunFd, err)
	}

	// Answer DNS in the tun read path.
	//
	// The wrapper has to exist before the WireGuard device, but the resolver
	// needs the mesh, which needs the device. So the wrapper holds a pointer
	// that is filled in once the mesh exists; queries arriving in that window
	// are dropped rather than answered wrongly, and the client retries.
	// Answered on the service address, not this device's: a packet to an
	// address the interface holds is delivered locally and never reaches the
	// tun, so nothing could answer it there.
	// Only the meshes that are switched on. A mesh left in the config but not
	// running costs nothing; one running that nobody wants costs an announce
	// loop, a WireGuard device and the battery to carry them.
	meshes := cfg.Active()
	if len(meshes) == 0 {
		return errors.New("no meshes are enabled")
	}
	primary := meshes[0]
	primaryKey, err := primary.Key()
	if err != nil {
		return err
	}

	// The resolver answers on one address — the first mesh's — because Android
	// takes a single DNS server. It resolves names across every mesh, so the
	// one address is enough (see resolveAll).
	dnsAddr := dnssrv.ServiceAddr(primaryKey.Prefix())
	var resolver atomic.Pointer[dnssrv.Server]
	intercept := dnssrv.NewIntercept(rawTun, dnsAddr, func(q []byte) ([]byte, error) {
		r := resolver.Load()
		if r == nil {
			return nil, errors.New("resolver not ready")
		}
		return r.Answer(q)
	})

	// One descriptor, several meshes. VpnService hands back exactly one tun,
	// and a WireGuard device cannot be shared between meshes — one static key
	// each, one preshared key per peer — so the descriptor is multiplexed by
	// destination prefix instead. Below the DNS intercept, so a query is still
	// an ordinary packet when the intercept sees it.
	mux := wg.NewMux(intercept)

	var instances []*meshInstance
	closeAll := func() {
		for _, in := range instances {
			in.dev.Close()
		}
		mux.Close()
	}

	for i, mc := range meshes {
		nk, err := mc.Key()
		if err != nil {
			closeAll()
			return fmt.Errorf("mesh %q: %w", mc.Label, err)
		}
		networkID := state.NetworkID(nk)
		// The mesh written as network_key keeps the identity this device
		// already had; anything else derives its own.
		ms, err := st.MeshState(networkID, cfg.NetworkKey != "" && mc.Label == state.DefaultLabel)
		if err != nil {
			closeAll()
			return fmt.Errorf("mesh %q: %w", mc.Label, err)
		}
		self := identity.OverlayAddr(nk, ms.Identity.DevicePub)
		aliases := v4.NewTable(networkID, v4.Entry{Overlay: self, DevicePub: ms.Identity.DevicePub}, nil)

		// This mesh's slice of the tun: its own /48 and its own block of the
		// synthetic IPv4 range. Exact, so a packet belongs to one mesh or none.
		port := mux.Port(nk.Prefix(), aliases.Block())
		translated := v4.NewDevice(port, aliases, wg.DefaultMTU-40-20)

		// A port each: two WireGuard devices cannot share one, since a
		// transport packet names its device only by an index that device
		// allocated.
		listen := cfg.ListenPort + uint16(i)
		dev, err := wg.NewDevice(translated, ms.Identity.WGPriv, listen,
			device.NewLogger(device.LogLevelError, "[wg "+mc.Label+"] "))
		if err != nil {
			closeAll()
			return fmt.Errorf("mesh %q: wireguard: %w", mc.Label, err)
		}

		// Before any traffic. An unprotected socket is the failure that looks
		// like nothing working at all.
		fds := dev.Bind.SocketFds()
		if len(fds) == 0 {
			dev.Close()
			closeAll()
			return errors.New("no sockets to protect: the bind exposed none, so the tunnel would carry its own control traffic")
		}
		for _, fd := range fds {
			if !p.Protect(fd) {
				dev.Close()
				closeAll()
				return fmt.Errorf("VpnService.protect(%d) refused", fd)
			}
		}

		instances = append(instances, &meshInstance{
			label: mc.Label, dev: dev, aliases: aliases, self: self, prefix: nk.Prefix(),
		})
		log.Info("data plane up", "mesh", mc.Label, "port", listen,
			"overlay", self, "protected_sockets", len(fds))
	}

	// Reuse the node across reconnects; see the comment on the package
	// variable. Only the first connect creates one.
	if node == nil {
		n, err := waku.New(nodeConfig(cfg))
		if err != nil {
			closeAll()
			return fmt.Errorf("rendezvous plane: %w", err)
		}
		node = n
	}
	if err := node.Start(); err != nil {
		closeAll()
		return fmt.Errorf("start rendezvous plane: %w", err)
	}
	log.Info("rendezvous plane up", "preset", cfg.Preset)

	for i, in := range instances {
		mc := meshes[i]
		// Each mesh sees its own key, relay setting, admin keys and services;
		// the rest of the config is the device's and shared.
		meshCfg := cfg
		meshCfg.NetworkKey = mc.NetworkKey
		meshCfg.AdminKeys = mc.AdminKeys
		meshCfg.Relay = mc.Relay
		meshCfg.Services = mc.Services

		nk, _ := mc.Key()
		ms, err := st.MeshState(state.NetworkID(nk), cfg.NetworkKey != "" && mc.Label == state.DefaultLabel)
		if err != nil {
			_ = node.Stop()
			closeAll()
			return err
		}
		mst := st
		if ms.Identity != st.Identity {
			mst = st.View(ms)
		}

		m, err := mesh.New(log.With("mesh", mc.Label), meshCfg, mst, node, in.dev)
		if err != nil {
			_ = node.Stop()
			closeAll()
			return err
		}
		m.SetV4(in.aliases)
		// Always fed, even with one mesh: the dispatcher is also what lets an
		// enrolment listen in while the mesh is up (see eventTap).
		m.SetFed()
		in.mesh = m
	}

	// Names. On Android this is what makes `laptop.mesh` work at all: there is
	// no hosts file to write, and VpnService.Builder.addDnsServer is a
	// first-class API (ADR-013). Not fatal if the bind is refused — port 53 is
	// privileged, and losing names is smaller than losing the tunnel.
	// Refuse to answer DNS at all unless we can forward what is not ours.
	// Android sends us every query, so a resolver that only knows .mesh takes
	// the device's name resolution away — no browsing, no app updates.
	var forward func([]byte) ([]byte, error)
	if f, fw, ferr := newForwarder(dnsServers, p); ferr != nil {
		log.Warn("name resolution disabled: no usable upstream resolvers", "err", ferr)
	} else {
		forward = fw
		// Kept so SetDNSServers can replace the list when the phone changes
		// network. Without that the forwarder keeps querying the resolvers of
		// the network it just left.
		fwd.Store(f)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		st:        st,
		name:      cfg.Name,
		suffix:    cfg.HostsSuffix,
		overlay:   instances[0].self.String(),
		prefix:    instances[0].prefix.String(),
		instances: instances,
		mux:       mux,
		cancel:    cancel, done: make(chan struct{}),
		dnsIntercept: intercept,
	}

	// One reader on the node, feeding every mesh — and any enrolment in
	// progress. A channel delivers each event to exactly one reader, so
	// everything that wants them has to be fed from here.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-node.Events():
				if !ok {
					return
				}
				for _, in := range instances {
					in.mesh.Deliver(ev)
				}
				tap.feed(ev)
			}
		}
	}()

	var wgRun sync.WaitGroup
	for _, in := range instances {
		wgRun.Add(1)
		go func(in *meshInstance) {
			defer wgRun.Done()
			if err := in.mesh.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("mesh stopped", "mesh", in.label, "err", err)
			}
		}(in)
	}
	go func() {
		defer close(s.done)
		wgRun.Wait()
	}()

	if forward != nil {
		// No socket and no port: the intercept already holds the packets. The
		// lookups span every mesh, so one resolver address serves them all.
		srv := &dnssrv.Server{
			Suffix:   cfg.HostsSuffix,
			Lookup:   resolveAll(instances),
			Alias:    aliasAll(instances),
			Upstream: forward,
		}
		resolver.Store(srv)
		s.dnsServer = srv
		log.Info("name resolution up", "address", dnsAddr, "suffix", cfg.HostsSuffix,
			"meshes", len(instances))
	}
	running = s
	return nil
}

// Stop tears the mesh down. Safe to call when not running.
func Stop() error { return stopSession(true) }

// StopForRestart tears the meshes down but leaves the rendezvous node running.
//
// For rebuilding the tunnel after a config change — adding a mesh, switching
// one on — which is now a routine thing to do. Stopping and starting the node
// each time is the part that is not routine: the library keeps process-global
// state, and a restart of it has been seen to crash inside its own persistency
// setup. Leaving it up costs nothing that it was not already costing a second
// earlier, and removes the churn entirely.
func StopForRestart() error { return stopSession(false) }

func stopSession(stopNode bool) error {
	mu.Lock()
	s := running
	running = nil
	mu.Unlock()
	if s == nil {
		return nil
	}

	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		// Do not block the UI thread on a shutdown that is stuck.
	}
	// Stop, never destroy: destroying leaves state behind that stops the next
	// node being created at all.
	if stopNode && node != nil {
		_ = node.Stop()
	}
	for _, in := range s.instances {
		in.dev.Close()
	}
	return s.mux.Close()
}

// Running reports whether the mesh is up.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return running != nil
}

// StatusJSON returns a snapshot: self, peers, and rendezvous health. Returns
// "{}" when not running, so the caller never has to special-case nil.
func StatusJSON() string {
	mu.Lock()
	s := running
	mu.Unlock()
	if s == nil {
		return "{}"
	}
	snap := snapshotAll(s.instances, s.suffix)
	snap.Name, snap.Overlay, snap.Prefix = s.name, s.overlay, s.prefix
	snap.DNSName = mesh.DNSName(s.name, s.suffix)
	if s.dnsIntercept != nil {
		handled, failed := s.dnsIntercept.Stats()
		snap.DNS.Intercepted, snap.DNS.InterceptFailed = handled, failed
		snap.DNS.Missed = s.dnsIntercept.Missed()
	}
	if s.dnsServer != nil {
		c := s.dnsServer.Count()
		snap.DNS.Queries, snap.DNS.Answers = c.Queries, c.Answers
		snap.DNS.Refused, snap.DNS.Forwarded, snap.DNS.ForwardFailed = c.Refused, c.Forwarded, c.ForwardFail
		snap.DNS.NXDomain, snap.DNS.NoDataA, snap.DNS.NoDataOther = c.NXDomain, c.NoDataA, c.NoDataOther
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// InviteKey extracts a network key from a scanned QR code or pasted text.
//
// Parsing lives here rather than in Kotlin so the invitation format has one
// implementation: the CLI writes it, the app reads it, and neither can drift.
// Accepts a full invite URI or a bare key, because people paste bare keys.
func InviteKey(scanned string) (string, error) {
	key, _, err := state.ParseInvite(scanned)
	return key, err
}

// InviteMeshName returns the mesh name hint from an invitation, or "".
func InviteMeshName(scanned string) string {
	_, name, err := state.ParseInvite(scanned)
	if err != nil {
		return ""
	}
	return name
}
