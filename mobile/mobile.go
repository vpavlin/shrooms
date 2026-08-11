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
	"os"
	"path/filepath"
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

	mesh   *mesh.Mesh
	dev    *wg.Device
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
	cfgPath, stateDir := paths(configDir)
	if _, err := os.Stat(cfgPath); err == nil {
		return errors.New("this device has already joined a mesh")
	}
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

	node, err := waku.New(nodeConfig(state.DefaultConfig()))
	if err != nil {
		return fmt.Errorf("rendezvous plane: %w", err)
	}
	defer node.Close()
	if err := node.Start(); err != nil {
		return fmt.Errorf("start rendezvous plane: %w", err)
	}
	// Nothing arrives until the node has peers. The inviter is waiting at a
	// prompt, so a few seconds here costs nothing and asking too early costs a
	// retry interval.
	time.Sleep(3 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	resp, err := invite.Redeem(ctx, rendezvous.InviteTransport(node), secret, &invite.Request{
		DevicePub: st.Identity.DevicePub,
		WGPub:     st.Identity.WGPub[:],
		Name:      name,
	})
	if err != nil {
		return errors.New("no answer — is `shrooms invite` still running on the other machine?")
	}

	nk, err := identity.NetworkKeyFromBytes(resp.NetworkKey)
	if err != nil {
		return err
	}
	cfg := state.DefaultConfig()
	cfg.Name = name
	cfg.NetworkKey = nk.String()
	if resp.Suffix != "" {
		cfg.HostsSuffix = resp.Suffix
	}
	for _, k := range resp.AdminKeys {
		if len(k) != ed25519.PublicKeySize {
			return errors.New("the invitation carried a malformed admin key")
		}
		cfg.AdminKeys = append(cfg.AdminKeys, b32.EncodeToString(k))
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
		auth, err := cfg.Authority()
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
		if err := st.SetCredential(resp.Credential); err != nil {
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
	dnsAddr := dnssrv.ServiceAddr(mustKey(cfg).Prefix())
	var resolver atomic.Pointer[dnssrv.Server]
	intercept := dnssrv.NewIntercept(rawTun, dnsAddr, func(q []byte) ([]byte, error) {
		r := resolver.Load()
		if r == nil {
			return nil, errors.New("resolver not ready")
		}
		return r.Answer(q)
	})

	dev, err := wg.NewDevice(intercept, st.Identity.WGPriv, cfg.ListenPort,
		device.NewLogger(device.LogLevelError, "[wg] "))
	if err != nil {
		return fmt.Errorf("wireguard: %w", err)
	}

	// Before any traffic. An unprotected socket is the failure that looks like
	// nothing working at all.
	fds := dev.Bind.SocketFds()
	if len(fds) == 0 {
		dev.Close()
		return errors.New("no sockets to protect: the bind exposed none, so the tunnel would carry its own control traffic")
	}
	for _, fd := range fds {
		if !p.Protect(fd) {
			dev.Close()
			return fmt.Errorf("VpnService.protect(%d) refused", fd)
		}
	}
	log.Info("data plane up", "port", cfg.ListenPort, "protected_sockets", len(fds))

	// Reuse the node across reconnects; see the comment on the package
	// variable. Only the first connect creates one.
	if node == nil {
		n, err := waku.New(nodeConfig(cfg))
		if err != nil {
			dev.Close()
			return fmt.Errorf("rendezvous plane: %w", err)
		}
		node = n
	}
	if err := node.Start(); err != nil {
		dev.Close()
		return fmt.Errorf("start rendezvous plane: %w", err)
	}
	log.Info("rendezvous plane up", "preset", cfg.Preset)

	m, err := mesh.New(log, cfg, st, node, dev)
	if err != nil {
		_ = node.Stop()
		dev.Close()
		return err
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
	nk, _ := cfg.Key()
	s := &session{
		name:    cfg.Name,
		suffix:  cfg.HostsSuffix,
		overlay: identity.OverlayAddr(nk, st.Identity.DevicePub).String(),
		prefix:  nk.Prefix().String(),
		mesh:    m, dev: dev, cancel: cancel, done: make(chan struct{}),
		dnsIntercept: intercept,
	}
	go func() {
		defer close(s.done)
		if err := m.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("mesh stopped", "err", err)
		}
	}()
	if forward != nil {
		// No socket and no port: the intercept already holds the packets.
		srv := &dnssrv.Server{
			Suffix:   cfg.HostsSuffix,
			Lookup:   m.Lookup,
			Upstream: forward,
		}
		resolver.Store(srv)
		s.dnsServer = srv
		log.Info("name resolution up", "address", dnsAddr, "suffix", cfg.HostsSuffix)
	}
	running = s
	return nil
}

// Stop tears the mesh down. Safe to call when not running.
func Stop() error {
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
	if node != nil {
		_ = node.Stop()
	}
	return s.dev.Close()
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
	snap := snapshot(s.mesh, s.suffix)
	snap.Name, snap.Overlay, snap.Prefix = s.name, s.overlay, s.prefix
	snap.DNSName = mesh.DNSName(s.name, s.suffix)
	if s.dnsIntercept != nil {
		handled, failed := s.dnsIntercept.Stats()
		snap.DNS.Intercepted, snap.DNS.InterceptFailed = handled, failed
	}
	if s.dnsServer != nil {
		q, a, r, f, ff := s.dnsServer.Stats()
		snap.DNS.Queries, snap.DNS.Answers = q, a
		snap.DNS.Refused, snap.DNS.Forwarded, snap.DNS.ForwardFailed = r, f, ff
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
