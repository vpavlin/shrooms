package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/rendezvous"
	"github.com/vpavlin/shrooms/internal/state"
)

// A daemon with no mesh yet.
//
// Installing the package enables the unit, and until now a machine that had not
// joined anything ran a daemon that exited at "network_key is not set" and was
// restarted forever by systemd. Joining then meant `shrooms join` followed by
// `systemctl restart`, which is a step people forget and a state — configured
// but not running — that nothing reports.
//
// So a daemon without a key waits instead. It holds the control socket, nothing
// else: no TUN, no rendezvous node, and in particular no Core subscription
// spending 20 MB/h on a mesh that does not exist. A node is started only for
// the length of an enrolment.
//
// This is also the first half of ADR-015. "One daemon, many meshes" needs a
// daemon that can gain a mesh while running; the empty case is the same
// primitive with nothing in it yet.

// runWaiting serves the control socket until somebody says which mesh this is.
func runWaiting(ctx context.Context, log *slog.Logger, cfgPath, stateDir, sock string) error {
	// The identity exists from the start, so the keys a credential will name
	// are settled before the mesh is, and do not change when it arrives.
	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		return err
	}

	log.Info("waiting to be told which mesh this is",
		"socket", sock, "device", hex.EncodeToString(st.Identity.DevicePub[:8]))

	joined := make(chan struct{})
	mux := http.NewServeMux()

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statusPayload{Name: "", Waiting: true})
	})

	mux.HandleFunc("/join", requireRoot(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token   string `json:"token"`
			Key     string `json:"key"`
			Name    string `json:"name"`
			Port    uint16 `json:"port"`
			Relay   bool   `json:"relay"`
			WaitS   int    `json:"wait_s"`
			Advert  string `json:"advertise"`
			Verbose bool   `json:"-"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.Name == "" {
			in.Name = state.DefaultConfig().Name
		}
		if in.Port == 0 {
			in.Port = 51820
		}

		wait := time.Duration(in.WaitS) * time.Second
		if wait <= 0 || wait > 15*time.Minute {
			wait = 2 * time.Minute
		}
		jctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()

		res, err := joinHere(jctx, log, st, cfgPath, stateDir, in.Token, in.Key,
			in.Name, in.Port, in.Advert, in.Relay)
		if err != nil {
			log.Warn("join failed", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)

		// Restart into the mesh we just joined. Re-exec rather than building
		// the data plane here: startup wires the TUN, WireGuard, services and
		// the resolver in one order, and a second way of doing that is a second
		// thing to keep correct. Same pid, and systemd sees no exit.
		go func() {
			time.Sleep(250 * time.Millisecond) // let the response reach the caller
			close(joined)
		}()
	}))

	// `init` and `set-key` write a config directly rather than going through
	// the socket, and a waiting daemon would otherwise sit there while the
	// machine has a mesh — the same "configured but not running" state waiting
	// mode exists to remove.
	mux.HandleFunc("/reload", requireRoot(func(w http.ResponseWriter, _ *http.Request) {
		unset, err := configHasNoKey(cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if unset {
			http.Error(w, "there is still no mesh in the config", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		go func() {
			time.Sleep(250 * time.Millisecond)
			close(joined)
		}()
	}))

	srv, err := listenControl(ctx, log, sock, mux, state.Config{})
	if err != nil {
		return err
	}
	defer srv.Close()

	select {
	case <-ctx.Done():
		return nil
	case <-joined:
		srv.Close()
		os.Remove(sock)
		return reexec(log)
	}
}

// joinResult is what the daemon reports after joining.
type joinResult struct {
	Name       string `json:"name"`
	Overlay    string `json:"overlay"`
	Prefix     string `json:"prefix"`
	Credential bool   `json:"credential"`
	Serial     uint64 `json:"serial,omitempty"`
	Expires    string `json:"expires,omitempty"`
}

// joinHere writes the config for the mesh this device has been invited to, or
// told the key of.
func joinHere(ctx context.Context, log *slog.Logger, st *state.State, cfgPath, stateDir,
	token, key, name string, port uint16, advertise string, relay bool) (*joinResult, error) {

	if _, err := os.Stat(cfgPath); err == nil {
		if cfg, err := state.LoadConfig(cfgPath); err == nil && cfg.NetworkKey != "" {
			return nil, errors.New("this daemon already has a mesh")
		}
	}

	var (
		nk        identity.NetworkKey
		adminKeys []string
		credRaw   []byte
		err       error
	)

	switch {
	case token != "":
		secret, err := invite.ParseToken(token)
		if err != nil {
			return nil, err
		}
		// This machine's own fleet settings, not the shipped defaults.
		//
		// The invite is not allowed to say which fleet to use — a device that
		// could be told that could be told to use somebody else's — but the
		// local config may, and ignoring it was wrong: a node whose config
		// names a different preset or cluster than today's default would
		// publish its request onto a fleet the inviter is not on. Both sides
		// behave perfectly and never meet, which presents as a join that waits
		// out its timeout with nothing in either log.
		fleet := state.DefaultConfig()
		if onDisk, err := state.LoadConfigUnvalidated(cfgPath); err == nil {
			if onDisk.Preset != "" {
				fleet.Preset = onDisk.Preset
			}
			fleet.ClusterID = onDisk.ClusterID
			fleet.EntryNodes = onDisk.EntryNodes
			if onDisk.Mode != "" {
				fleet.Mode = onDisk.Mode
			}
		}
		node, err := startNode(nodeConfig(fleet))
		if err != nil {
			return nil, err
		}
		defer node.Close()

		// Logged, because a mismatch here is invisible otherwise: the only
		// symptom is that nobody answers.
		log.Info("redeeming an invite",
			"preset", fleet.Preset, "cluster", fleet.ClusterID, "mode", fleet.Mode)
		resp, err := invite.Redeem(ctx, rendezvous.InviteTransport(node), secret, &invite.Request{
			DevicePub: st.Identity.DevicePub,
			WGPub:     st.Identity.WGPub[:],
			Name:      name,
		})
		if err != nil {
			return nil, fmt.Errorf("no answer — is `shrooms invite` still running there? (%w)", err)
		}
		if nk, err = identity.NetworkKeyFromBytes(resp.NetworkKey); err != nil {
			return nil, err
		}
		for _, k := range resp.AdminKeys {
			adminKeys = append(adminKeys, b32.EncodeToString(k))
		}
		credRaw = resp.Credential

	case key != "":
		if nk, err = identity.ParseNetworkKey(key); err != nil {
			return nil, err
		}

	default:
		return nil, errors.New("give either an invite token or a network key")
	}

	cfg := state.DefaultConfig()
	cfg.NetworkKey = nk.String()
	cfg.Name = name
	cfg.ListenPort = port
	cfg.Relay = relay
	if advertise != "" {
		cfg.Advertise = []string{advertise}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return nil, err
	}
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return nil, err
	}
	if len(adminKeys) > 0 {
		if err := addAdminKeys(cfgPath, adminKeys); err != nil {
			return nil, err
		}
	}

	res := &joinResult{
		Name:    name,
		Overlay: identity.OverlayAddr(nk, st.Identity.DevicePub).String(),
		Prefix:  nk.Prefix().String(),
	}

	if len(credRaw) > 0 {
		c, err := cred.UnmarshalCredential(credRaw)
		if err != nil {
			return nil, fmt.Errorf("the invite carried a malformed credential: %w", err)
		}
		// Checked before it is stored: a credential that does not verify would
		// fail silently later, as a mesh where nobody talks to us.
		written, err := state.LoadConfig(cfgPath)
		if err != nil {
			return nil, err
		}
		auth, err := written.Authority()
		if err != nil {
			return nil, err
		}
		if auth != nil {
			if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
				return nil, fmt.Errorf("the credential in the invite does not verify: %w", err)
			}
		}
		if err := st.SetCredential(credRaw); err != nil {
			return nil, err
		}
		res.Credential = true
		res.Serial = c.Serial
		res.Expires = time.Unix(c.NotAfter, 0).Format(time.RFC3339)
	}

	log.Info("joined", "mesh", res.Prefix, "overlay", res.Overlay, "credential", res.Credential)
	return res, nil
}

// reexec replaces this process with a fresh one, now that there is a config to
// read. Same pid, so systemd sees a running service throughout.
func reexec(log *slog.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log.Info("restarting into the mesh")
	return syscall.Exec(exe, os.Args, os.Environ())
}

// configHasNoKey reports whether this machine has yet to join anything.
//
// A missing config counts: the package installs the unit before anyone has run
// `join`, and "no file" and "no key in the file" are the same situation to
// anyone looking at the machine.
func configHasNoKey(path string) (bool, error) {
	cfg, err := state.LoadConfigUnvalidated(path)
	if err != nil {
		// errors.Is rather than os.IsNotExist: the loader wraps it, and
		// os.IsNotExist does not unwrap — which is how a missing config came
		// back as a fatal error instead of "nothing to do yet".
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	// Meshes(), not NetworkKey alone: a config that names its meshes the
	// multi-mesh way has no network_key at all, and would otherwise wait
	// forever for a mesh it already has.
	if cfg.NetworkKey == state.KeyPlaceholder {
		return true, nil
	}
	return len(cfg.Meshes()) == 0, nil
}
