package main

import (
	"encoding/json"
	"errors"
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
func runtimeHandlers(mux *http.ServeMux, log *slog.Logger, cfgPath string, tail *logtail.Ring, restart chan<- error) {
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
			// And refuse to restart into a config that will not load.
			//
			// Exiting is the one operation that cannot be taken back from here:
			// if the daemon does not come up, the mesh it was carrying is the
			// way in to fix it, and on a remote machine that means no way in at
			// all. So the config is read the way the next start will read it,
			// first, while there is still something to answer with.
			if _, err := state.LoadConfig(cfgPath); err != nil {
				http.Error(w, "not restarting: the config on disk would not load, "+
					"and this daemon is what you would fix it through — "+err.Error(),
					http.StatusConflict)
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
			// Deliberately not flushed. A flush commits the response before the
			// handler returns, which makes Go send it chunked — and the module
			// reading this takes everything after the headers as the body, so
			// the chunk framing would land in front of the JSON and a restart
			// that worked would report "the daemon said nothing readable". Left
			// to the ordinary path, Go buffers it, sets Content-Length, and
			// writes it when the handler returns, which is well inside the
			// pause below.
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
	//
	// GET returns what is CONFIGURED, which is not what /status reports.
	// Status shows what is running, and the two differ — a service that is
	// switched off, that failed to bind, or that was added since the last
	// reload. An editing tool that read the running list and wrote it back
	// would delete every configured service that happened not to be running,
	// silently, as the ordinary result of adding an unrelated one.
	mux.HandleFunc("/config/services", readOrWrite(configuredServices(cfgPath), writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			was := cfg.Services
			cfg.Services = in.Services
			if _, err := cfg.ServiceSpecs(); err != nil {
				cfg.Services = was
				return "", err
			}
			return fmt.Sprintf("%d service(s) published", len(in.Services)), nil
		})))

	// Whether this device forwards traffic for peers of a mesh that cannot
	// reach each other (ADR-013).
	//
	// Missing until now, which was a gap rather than a decision: it is per
	// mesh, carrying traffic for your own machines and for somebody else's
	// being different choices, and the only way to set it was a text editor
	// and a restart. A mesh with no relay is invisible until somebody on
	// mobile data cannot reach anybody.
	mux.HandleFunc("/config/relay", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			if err := setMeshBool(cfg, in.Label,
				&cfg.Relay, func(m *state.Mesh) *bool { return &m.Relay },
				in.Enabled); err != nil {
				return "", err
			}
			where := in.Label
			if where == "" {
				where = state.DefaultLabel
			}
			if in.Enabled {
				return "this device will forward for peers of " + where +
					" that cannot reach each other; it starts on the next restart", nil
			}
			return "this device will stop forwarding for " + where +
				" on the next restart", nil
		}))

	// Whether the router is asked to open this node's port (ADR-024).
	//
	// Global rather than per mesh: it is one UDP port, and the request is made
	// once for it. On by default, and worth being able to switch off from a UI
	// because it does ask to be reachable from the internet — which is a
	// decision somebody may want to take back without finding a config file.
	mux.HandleFunc("/config/portmap", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			cfg.PortMapping = in.Enabled
			if in.Enabled {
				return "the router will be asked for a way in; it takes effect on the next restart", nil
			}
			return "the router will not be asked; a mapping already granted lapses on its own", nil
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
			// Per mesh, because telling your own machines what you run and
			// telling somebody else's are different decisions — which the
			// config has always modelled and this endpoint did not, setting
			// the first mesh's flag whatever it was asked about.
			if err := setMeshBool(cfg, in.Label,
				&cfg.AnnounceServices,
				func(m *state.Mesh) *bool { return &m.AnnounceServices },
				in.Enabled); err != nil {
				return "", err
			}
			applyAnnounce(rl)
			if in.Enabled {
				return "peers will see the names of services published here", nil
			}
			return "peers will see nothing about what is published here", nil
		}))

	// And the ports that happen to be listening on the mesh address (ADR-026).
	//
	// Its own switch rather than a mode of the one above, because the two
	// disclose different things: services are declared one line at a time by
	// somebody who meant it, while these are discovered — whatever is bound,
	// including the thing you started for ten minutes and forgot.
	mux.HandleFunc("/config/announce-bound", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			if err := setMeshBool(cfg, in.Label,
				&cfg.AnnounceBound,
				func(m *state.Mesh) *bool { return &m.AnnounceBound },
				in.Enabled); err != nil {
				return "", err
			}
			applyAnnounce(rl)
			if in.Enabled {
				return "peers will see which ports are listening on this device's mesh address", nil
			}
			return "peers will see nothing about what is bound here", nil
		}))

	// Whether this node repeats what the mesh's admin has withdrawn.
	//
	// Inverted on the wire: the request says "announce revocations", the config
	// stores "quiet". On by default, because a revocation only means something
	// if it reaches nodes that were offline when it was published, and no node
	// is the designated repeater — the admin key is deliberately not on any
	// daemon (ADR-018), so every holder is equally the failover.
	//
	// Takes effect immediately in the sense that matters: the next epoch and
	// the next peer to appear.
	mux.HandleFunc("/config/announce-revocations", writeSetting(log, cfgPath,
		func(cfg *state.Config, in settingRequest) (string, error) {
			quiet := !in.Enabled
			if err := setMeshBool(cfg, in.Label,
				&cfg.QuietRevocations,
				func(m *state.Mesh) *bool { return &m.QuietRevocations },
				quiet); err != nil {
				return "", err
			}
			if in.Enabled {
				return "this node will repeat what has been revoked, so a peer that was offline learns it", nil
			}
			return "this node will not repeat revocations; another node that does must be reachable", nil
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

// applyAnnounce pushes a disclosure change onto the running meshes.
//
// Best effort and deliberately unreported: the config is written either way,
// so the worst case is that the change lands at the next restart like every
// other setting here — which is what it did before this existed. Failing the
// request because the live update did not take would turn a working write into
// an error.
func applyAnnounce(rl *reloader) {
	if rl == nil {
		return
	}
	if err := rl.ApplyAnnounce(); err != nil {
		rl.log.Warn("could not apply the announce change to the running mesh",
			"err", err, "hint", "it takes effect on the next restart")
	}
}

// setMeshBool writes one per-mesh flag, wherever the config keeps it.
//
// The first mesh's settings live at the top level — that is what the
// single-mesh config form means — and the rest live under [mesh.<label>]. A UI
// should not have to know which, and every caller that did know got it wrong
// for the first mesh at least once.
func setMeshBool(cfg *state.Config, label string, top *bool,
	pick func(*state.Mesh) *bool, v bool) error {

	if label == "" || (label == state.DefaultLabel && cfg.NetworkKey != "") {
		*top = v
		return nil
	}
	m, ok := cfg.MeshSet[label]
	if !ok {
		return fmt.Errorf("no mesh called %q", label)
	}
	*pick(&m) = v
	cfg.MeshSet[label] = m
	return nil
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

// rejected marks a change the caller got wrong, as opposed to one this daemon
// could not carry out — the difference between 400 and 500, which used to be
// visible in the handler's shape and is now inside a callback.
type rejected struct{ error }

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

		// Load, change and write as one operation under a lock. Two settings
		// changed at once used to lose one of them: both handlers loaded the
		// same config, and whichever wrote second had never seen the other's
		// change. Nothing reported it — the setting simply went back.
		var msg string
		err := state.UpdateConfig(cfgPath, func(cfg *state.Config) error {
			m, err := apply(cfg, in)
			if err != nil {
				return rejected{err}
			}
			// Validated as a whole before it is written: a config that does
			// not load is a daemon that will not start, and finding that out
			// at the next reboot is the worst possible time.
			if err := cfg.Validate(); err != nil {
				return rejected{err}
			}
			msg = m
			return nil
		})
		if err != nil {
			var bad rejected
			if errors.As(err, &bad) {
				http.Error(w, bad.error.Error(), http.StatusBadRequest)
				return
			}
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
// (**Joining another mesh from the socket** was on this list and is now built:
// /join, in joinmore.go, in the socket-group tier. It is the most consequential
// endpoint in that tier, and a reader consulting this block to size the socket's
// surface was being told the opposite.)
//
// **Issuing a credential.** An invite is two halves (ADR-017): the daemon holds
// the exchange, and the admin key signs the credential. The socket can do the
// first and must not be able to do the second — that separation is what makes
// group access to this socket a bounded grant rather than a way to admit
// anybody. So a desktop invite flow needs the passphrase, in the user session,
// which means running the CLI rather than teaching the socket to sign. Recorded
// in ADR-025 rather than left as a surprise.

// readOrWrite answers GET from one handler and everything else from another.
func readOrWrite(get, rest http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		rest(w, r)
	}
}

// configuredServices reports the declarations in the config file, for the mesh
// named by ?mesh= or the top-level one.
//
// From the file rather than from the running publisher, deliberately: this is
// what an editing tool must read before writing back, and the running list
// omits anything that is configured and not currently up.
func configuredServices(cfgPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := state.LoadConfig(cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := cfg.Services
		if label := r.URL.Query().Get("mesh"); label != "" {
			m, ok := cfg.MeshSet[label]
			if !ok {
				http.Error(w, "no mesh called "+label, http.StatusNotFound)
				return
			}
			out = m.Services
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"services": out})
	}
}
