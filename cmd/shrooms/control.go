package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/vpavlin/shrooms/internal/logtail"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
)

// Settings a desktop app can change, over the same socket it reads status from
// (ADR-025).
//
// These are the things the daemon can do entirely by itself: this device's own
// name, what it publishes, which meshes are running, and whether it forwards
// for others. None of them decides who belongs to a mesh — that needs the admin
// key, which is a passphrase-protected file the socket cannot reach — so they
// sit in the tier the socket group may use, and are gated by the file mode
// rather than by SO_PEERCRED.
//
// Written through the config file rather than applied in memory, deliberately.
// The config is what a restart reads, so anything applied only to the running
// process is a setting that quietly reverts, and that is a worse failure than
// one that needs a reload — it looks like it worked.

// runtimeHandlers registers the two endpoints that are about the daemon itself
// rather than about a mesh: what it has been saying, and starting it again.
//
// Both in the socket group's tier. The log carries what stderr carries — peer
// names, addresses, why a tunnel failed — every bit of which /status already
// names, and no secret, because the daemon logs none. The restart is the other
// half of the settings that say "on the next restart": a caller who may switch
// a mesh off may apply it.
func runtimeHandlers(mux *http.ServeMux, log *slog.Logger, tail *logtail.Ring, restart chan<- error) {
	// The recent log, for a UI that cannot reach the journal (ADR-025).
	//
	// Same tier as /status and for the same reason: it carries what stderr
	// carries — peer names, addresses, why a tunnel failed — all of which the
	// status payload already names, and no secret, because the daemon logs
	// none. `?since=<unix ms>` returns only what is newer, so a pane that polls
	// every couple of seconds sends back what it has rather than everything.
	if tail != nil {
		mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
			lines := tail.Lines()
			if s := r.URL.Query().Get("since"); s != "" {
				if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
					lines = tail.Since(ms)
				}
			}
			if lines == nil {
				lines = []logtail.Line{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"lines": lines})
		})
	}

	// Restarting the daemon, which is the other half of every setting that
	// says "on the next restart" (ADR-025).
	//
	// Half a feature otherwise: switching a mesh off writes the config and
	// then needs a terminal, which is exactly the terminal the desktop
	// controls existed to avoid. So the same tier as those settings — a
	// caller who may switch a mesh off may apply it.
	//
	// It refuses when nothing would start this process again. Exiting under
	// systemd is a five-second gap; exiting a daemon somebody ran in a
	// terminal is a mesh that stays down until they notice, and a button that
	// silently means "stop" is worse than no button. Same rule the rendezvous
	// watchdog learned the hard way.
	if restart != nil {
		mux.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST to restart", http.StatusMethodNotAllowed)
				return
			}
			if !restartable() {
				http.Error(w, "nothing would start this daemon again, so exiting "+
					"would leave the mesh down; restart it yourself "+
					"(systemctl restart shrooms)", http.StatusConflict)
				return
			}
			log.Info("restarting on request from the control socket")
			// Answered before exiting, so the caller sees a result rather than
			// a closed connection it has to interpret. The exit follows the
			// ordinary path: the same channel the watchdog uses, so shutdown
			// tears down tunnels and DNS registration exactly as it always
			// does.
			writeJSON(w, map[string]string{
				"result": "restarting; settings that needed one are applied when it comes back",
			})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			go func() {
				// A moment for the response to reach a caller that is
				// notoriously bad at reading one — the QML bridge does a
				// blocking read and a connection closed under it reports as a
				// failure, on the one operation whose whole point is that it
				// worked.
				time.Sleep(250 * time.Millisecond)
				restart <- errRestartRequested
			}()
		})
	}
}

// controlHandlers registers the write endpoints.
func controlHandlers(mux *http.ServeMux, log *slog.Logger, cfgPath string, rl *reloader) {
	// A device's name, as its peers see it.
	//
	// Takes effect on the next announce after a reload, and the response says
	// so: a name that has changed locally and not on anybody else's roster is
	// the confusing half of this operation.
	mux.HandleFunc("/config/name", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			// Sanitised the same way the resolver does, so what is stored is
			// what will actually resolve: a name that survives the config and
			// not DNS is a device nobody can reach by name.
			name := mesh.SanitiseName(in.Name)
			if name == "" {
				return "", fmt.Errorf("%q leaves nothing usable as a device name", in.Name)
			}
			cfg.Name = name
			return "name set to " + name + "; peers see it after the next announce", nil
		}))

	// Light node or relay node — how much of the rendezvous plane this device
	// carries for everybody else.
	mux.HandleFunc("/config/mode", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			switch in.Mode {
			case state.ModeCore, state.ModeEdge:
			default:
				return "", fmt.Errorf("mode %q is neither %s nor %s",
					in.Mode, state.ModeCore, state.ModeEdge)
			}
			cfg.Mode = in.Mode
			return "mode set to " + in.Mode + "; applies on the next restart", nil
		}))

	// What this device publishes on the mesh. Validated here rather than at the
	// next start, because a malformed spec that is only noticed on restart is a
	// service that silently stops existing.
	mux.HandleFunc("/config/services", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			was := cfg.Services
			cfg.Services = in.Services
			if _, err := cfg.ServiceSpecs(); err != nil {
				cfg.Services = was
				return "", err
			}
			return fmt.Sprintf("%d service(s) published", len(in.Services)), nil
		}))

	// Whether this device's peers are told what it publishes (ADR-023).
	//
	// A disclosure decision, and the one setting here that changes what other
	// people can see rather than what this device does — so it is worth being
	// reachable from a UI, where the wording can say what it actually changes:
	// a member can already reach these services by connecting to the address,
	// and this is about whether the names are listed for them.
	mux.HandleFunc("/config/announce", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			cfg.AnnounceServices = in.Enabled
			if in.Enabled {
				return "peers will see the names of services published here", nil
			}
			return "peers will see nothing about what is published here", nil
		}))

	// Whether a mesh runs, without leaving it. Being a member and using it are
	// different things (ADR-015).
	mux.HandleFunc("/config/mesh", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			if in.Label == "" {
				return "", fmt.Errorf("which mesh?")
			}
			if in.Label == state.DefaultLabel && cfg.NetworkKey != "" {
				// The single-mesh form has nowhere to say "off", and a device
				// with one mesh that is switched off is a device that is
				// switched off — which is what disconnecting is for.
				return "", fmt.Errorf("the first mesh cannot be switched off; stop the daemon instead")
			}
			m, ok := cfg.MeshSet[in.Label]
			if !ok {
				return "", fmt.Errorf("no mesh called %q", in.Label)
			}
			m.Disabled = !in.Enabled
			cfg.MeshSet[in.Label] = m
			if in.Enabled {
				return in.Label + " switched on; it starts on the next restart", nil
			}
			return in.Label + " switched off; it stops on the next restart", nil
		}))
}

// leaveHandler removes a mesh from the config.
//
// The config only: the identity, the credential and the state for that mesh
// stay where they are, so re-joining with a fresh invite gets the same address
// back. Leaving is "stop being a member", not "forget this ever happened", and
// the second one is what deleting the state directory is for.
func leaveHandler(log *slog.Logger, cfgPath string) http.HandlerFunc {
	return writeSetting(log, cfgPath, func(cfg *state.Config, in settingRequest) (string, error) {
		if in.Label == "" {
			return "", fmt.Errorf("which mesh?")
		}
		if in.Label == state.DefaultLabel && cfg.NetworkKey != "" {
			// Leaving the mesh a device was built around is "forget
			// everything", which is a different act with a different blast
			// radius, and not one to offer behind the same button.
			return "", fmt.Errorf("the first mesh cannot be left; clear the config instead")
		}
		if _, ok := cfg.MeshSet[in.Label]; !ok {
			return "", fmt.Errorf("no mesh called %q", in.Label)
		}
		delete(cfg.MeshSet, in.Label)
		return "left " + in.Label + "; it stops on the next restart", nil
	})
}

// settingRequest is every field any of the setting endpoints reads. One shape
// rather than four, because the alternative is four almost-identical decoders
// and a fifth that gets forgotten.
type settingRequest struct {
	Name     string   `json:"name,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Services []string `json:"services,omitempty"`
	Label    string   `json:"label,omitempty"`
	Enabled  bool     `json:"enabled,omitempty"`
}

// writeSetting is the common shape: decode, apply to a freshly read config,
// write it back, and say what happened.
//
// The config is re-read for every request rather than held in memory, because
// something else edits it — a person with a text editor, `shrooms join`, the
// Android bindings — and writing back a copy loaded at startup would silently
// discard whatever they did.
func writeSetting(log *slog.Logger, cfgPath string,
	apply func(*state.Config, settingRequest) (string, error)) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST a setting", http.StatusMethodNotAllowed)
			return
		}
		var in settingRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "unreadable request: "+err.Error(), http.StatusBadRequest)
			return
		}

		cfg, err := state.LoadConfigUnvalidated(cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		msg, err := apply(&cfg, in)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Validated as a whole before it is written: a config that does not
		// load is a daemon that will not start, and finding that out at the
		// next reboot is the worst possible time.
		if err := cfg.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := state.WriteConfig(cfgPath, cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("config changed over the control socket", "result", msg)
		writeJSON(w, map[string]string{"result": msg})
	}
}

// writeJSON is the one place a response is encoded, so every endpoint answers
// in the same shape.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// What is deliberately not here yet, and why.
//
// **Joining another mesh from the socket.** The daemon has everything it needs
// — a rendezvous connection to redeem the invite and the config to write — but
// a new mesh is a new WireGuard device, so it only runs after a restart. That
// is worth building; it is not worth half-building, because a join that appears
// to work and does nothing until the next reboot is the sort of thing people
// remember.
//
// **Issuing a credential.** An invite is two halves (ADR-017): the daemon holds
// the exchange, and the admin key signs the credential. The socket can do the
// first and must not be able to do the second — that separation is what makes
// group access to this socket a bounded grant rather than a way to admit
// anybody. So a desktop invite flow needs the passphrase, in the user session,
// which means running the CLI rather than teaching the socket to sign. Recorded
// in ADR-025 rather than left as a surprise.
