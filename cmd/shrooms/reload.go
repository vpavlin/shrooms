package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/service"
	"github.com/vpavlin/shrooms/internal/state"
)

// Picking up config changes without a restart.
//
// Only what can be changed safely, which today means the published services.
// Everything else — the network key, the port, the interface, the rendezvous
// fleet — is wired into the tunnel or the node at startup, and pretending to
// reload it would be worse than saying it needs a restart. So this says so,
// naming the settings that were ignored rather than silently keeping them.
//
// Services are the piece worth doing first: they are the setting people edit,
// they are contained (a listener per name), and republishing costs nothing to
// anything else.

// cmdReload asks a running daemon to re-read its config.
func cmdReload(args []string) error {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := socketClient(*sock, 30*time.Second).Post("http://unix/reload", "application/json", nil)
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w", *sock, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	fmt.Print(string(out))
	return nil
}

// reloader applies config changes to running instances.
type reloader struct {
	mu        sync.Mutex
	cfgPath   string
	log       *slog.Logger
	instances []*instance
	baseline  state.Config
}

// Reload re-reads the config and applies what it can. It returns a short
// description of what happened, for the caller to print or log.
func (r *reloader) Reload(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg, err := state.LoadConfig(r.cfgPath)
	if err != nil {
		return "", fmt.Errorf("reload: %w", err)
	}

	byLabel := map[string]state.Mesh{}
	for _, m := range cfg.Meshes() {
		byLabel[m.Label] = m
	}

	changed := 0
	for _, in := range r.instances {
		m, ok := byLabel[in.label]
		if !ok {
			// A mesh removed from the config keeps running until a restart.
			// Tearing down a tunnel from a reload is not a "non-critical
			// piece", whatever the config now says.
			r.log.Warn("mesh is no longer in the config; it keeps running until a restart",
				"mesh", in.label)
			continue
		}
		if slices.Equal(in.specs, m.Services) {
			continue
		}
		if err := r.republish(ctx, in, cfg, m); err != nil {
			return "", err
		}
		changed++
	}

	// What was ignored, named. A reload that silently keeps the old value is
	// how someone spends an afternoon wondering why their edit did nothing.
	var stale []string
	for _, s := range needsRestart(r.baseline, cfg) {
		stale = append(stale, s)
	}

	msg := fmt.Sprintf("reloaded: %d mesh(es) republished services", changed)
	if len(stale) > 0 {
		msg += fmt.Sprintf("; unchanged until a restart: %v", stale)
	}
	return msg, nil
}

// ApplyAnnounce pushes the announce flags from the config onto the running
// meshes, so a disclosure toggle takes effect when it is pressed.
//
// Every other setting here is written to the config and waits for a restart,
// which is the right default: most of them are wired into a WireGuard device
// or a rendezvous node at startup. These two are not. They gate what goes into
// the next announcement, and there is nothing to rebuild — so making somebody
// restart a daemon to stop disclosing something was both unnecessary and, for
// a disclosure control, the wrong way round.
func (r *reloader) ApplyAnnounce() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg, err := state.LoadConfigUnvalidated(r.cfgPath)
	if err != nil {
		return err
	}
	byLabel := map[string]state.Mesh{}
	for _, m := range cfg.Meshes() {
		byLabel[m.Label] = m
	}
	for _, in := range r.instances {
		m, ok := byLabel[in.label]
		if !ok {
			continue
		}
		if in.mesh.SetAnnounce(m.AnnounceServices, m.AnnounceBound) {
			r.log.Info("announcing changed", "mesh", in.label,
				"services", m.AnnounceServices, "bound", m.AnnounceBound)
		}
	}
	return nil
}

// republish swaps one mesh's published services.
func (r *reloader) republish(ctx context.Context, in *instance, cfg state.Config, m state.Mesh) error {
	specs, err := service.ParseSpecs(m.Services)
	if err != nil {
		return fmt.Errorf("mesh %q: services: %w", m.Label, err)
	}
	// Closed first, so the ports are free before the new publisher binds them.
	// Briefly interrupts connections to this node's services, which is the
	// honest cost of changing what they are.
	if in.services != nil {
		in.services.Close()
		in.services = nil
	}
	if len(specs) > 0 {
		in.services = service.Publish(ctx, in.self, mesh.DNSName(cfg.Name, cfg.HostsSuffix), specs,
			func(msg string, args ...any) { r.log.Info(msg, append(args, "mesh", m.Label)...) })
	}
	in.specs = append([]string(nil), m.Services...)
	r.log.Info("services reloaded", "mesh", m.Label, "count", len(specs))
	return nil
}

// needsRestart lists settings that changed and cannot be applied while running.
func needsRestart(was, now state.Config) []string {
	var out []string
	add := func(name string, differs bool) {
		if differs {
			out = append(out, name)
		}
	}
	add("network_key", was.NetworkKey != now.NetworkKey)
	add("listen_port", was.ListenPort != now.ListenPort)
	add("interface", was.Interface != now.Interface)
	add("preset", was.Preset != now.Preset)
	add("mode", was.Mode != now.Mode)
	add("cluster_id", was.ClusterID != now.ClusterID)
	add("entry_nodes", !slices.Equal(was.EntryNodes, now.EntryNodes))
	add("hosts_suffix", was.HostsSuffix != now.HostsSuffix)
	add("relay", was.Relay != now.Relay)
	add("advertise", !slices.Equal(was.Advertise, now.Advertise))
	add("admin_keys", !slices.Equal(was.AdminKeys, now.AdminKeys))
	// Every mesh, by label, rather than a count. Comparing only the number of
	// meshes meant a change to any per-mesh setting - mesh.work.key,
	// mesh.work.relay, mesh.work.admin_keys - was applied to nothing and named
	// in nothing: reload reported success and the edit vanished. The same for a
	// mesh replaced rather than added, which leaves the count unchanged.
	for label, a := range was.MeshSet {
		b, ok := now.MeshSet[label]
		if !ok {
			add("mesh."+label+" (removed)", true)
			continue
		}
		add("mesh."+label, !reflect.DeepEqual(a, b))
	}
	for label := range now.MeshSet {
		if _, ok := was.MeshSet[label]; !ok {
			add("mesh."+label+" (added)", true)
		}
	}
	return out
}
