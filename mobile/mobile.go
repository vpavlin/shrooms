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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	dnssrv "github.com/vpavlin/logos-vpn/internal/dns"
	"github.com/vpavlin/logos-vpn/internal/identity"
	"github.com/vpavlin/logos-vpn/internal/mesh"
	"github.com/vpavlin/logos-vpn/internal/state"
	"github.com/vpavlin/logos-vpn/internal/waku"
	"github.com/vpavlin/logos-vpn/internal/wg"
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
	overlay string
	prefix  string

	mesh   *mesh.Mesh
	dev    *wg.Device
	cancel context.CancelFunc
	done   chan struct{}
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

// Start brings the mesh up on a TUN descriptor from VpnService.Builder.
//
// The descriptor is dup'd, because Go's os.File takes ownership and would close
// the caller's copy — leaving Android holding a closed interface.
func Start(tunFd int, configDir string, p Protector, l Logger) error {
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
	tunDev, _, err := tun.CreateUnmonitoredTUNFromFD(dupped)
	if err != nil {
		syscall.Close(dupped)
		return fmt.Errorf("tun from fd %d: %w", tunFd, err)
	}

	dev, err := wg.NewDevice(tunDev, st.Identity.WGPriv, cfg.ListenPort,
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
	selfAddr := identity.OverlayAddr(mustKey(cfg), st.Identity.DevicePub)
	var dnsConn net.PacketConn
	if pc, derr := dnssrv.Listen(selfAddr); derr != nil {
		log.Warn("name resolution unavailable", "err", derr)
	} else {
		dnsConn = pc
		log.Info("name resolution up", "address", selfAddr, "suffix", cfg.HostsSuffix)
	}

	ctx, cancel := context.WithCancel(context.Background())
	nk, _ := cfg.Key()
	s := &session{
		name:    cfg.Name,
		overlay: identity.OverlayAddr(nk, st.Identity.DevicePub).String(),
		prefix:  nk.Prefix().String(),
		mesh:    m, dev: dev, cancel: cancel, done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		if err := m.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("mesh stopped", "err", err)
		}
	}()
	if dnsConn != nil {
		resolver := &dnssrv.Server{Suffix: cfg.HostsSuffix, Lookup: m.Lookup}
		go func() {
			if err := resolver.Serve(ctx, dnsConn); err != nil && ctx.Err() == nil {
				log.Error("name resolution stopped", "err", err)
			}
		}()
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
	snap := snapshot(s.mesh)
	snap.Name, snap.Overlay, snap.Prefix = s.name, s.overlay, s.prefix
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
