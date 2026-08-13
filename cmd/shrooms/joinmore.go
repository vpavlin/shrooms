package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/mesh"
	"github.com/vpavlin/shrooms/internal/state"
)

// Joining another mesh from a daemon that is already running (ADR-015,
// ADR-025).
//
// This was deliberately left out when the desktop controls landed, and the
// reason it was left out has since gone away. The objection was that a new
// mesh is a new WireGuard device, so it does not run until a restart, and "a
// join that appears to work and does nothing until the next reboot is the sort
// of thing people remember". That is still true — and there is now a restart
// button next to it, so the flow ends where the user expects rather than in a
// terminal.
//
// The waiting daemon's /join is a different operation despite the name: it
// writes this device's first mesh into a fresh config and re-execs into it.
// This one adds a mesh beside the ones already running, and must therefore
// preserve everything else in the config — which is why it is not that
// function with a flag.
//
// In the socket group's tier, with the rest of the desktop controls, and this
// is the most consequential thing in that tier: joining a mesh gives its
// members a tunnel to this device. It is bounded by needing a valid invite
// token, which the group member has to have been given by somebody already
// inside. Anybody who can reach this socket can already switch a mesh off,
// leave one, and read every peer's endpoints; granting the socket is a real
// decision, and ADR-025 says so.

// joinAnother redeems an invite into an additional mesh, writing it to the
// config for the next start.
func joinAnother(ctx context.Context, log *slog.Logger, tr invite.Transport,
	cfgPath string, st *state.State, token, name, label string, relay bool) (*joinResult, error) {

	label = mesh.SanitiseName(label)
	if label == "" {
		return nil, errors.New("an additional mesh needs a label to file it under")
	}
	if label == state.DefaultLabel {
		// Not a naming quibble: the label is what `laptop.<label>.mesh` and
		// `--mesh <label>` resolve through, and "default" already names the
		// mesh written in the single-mesh form. Two meshes answering to it is
		// a device that cannot say which one it means.
		return nil, fmt.Errorf("%q already names the mesh this device was built around; "+
			"give this one a label of its own", state.DefaultLabel)
	}

	cfg, err := state.LoadConfigUnvalidated(cfgPath)
	if err != nil {
		return nil, err
	}
	if _, taken := cfg.MeshSet[label]; taken {
		return nil, fmt.Errorf("this device is already in a mesh labelled %q", label)
	}
	if name == "" {
		name = cfg.Name
	}

	if tr == nil {
		// Checked before the token is even parsed: this is a property of the
		// daemon rather than of what was typed, so "your token is malformed"
		// would send somebody looking in the wrong place entirely.
		//
		// Nothing is started to work around it. Quietly opening a second
		// rendezvous node here would put this daemon on the fleet twice.
		return nil, errors.New("the rendezvous plane is not available, so an invite cannot be redeemed")
	}
	secret, err := invite.ParseToken(token)
	if err != nil {
		return nil, err
	}

	log.Info("redeeming an invite for another mesh", "label", label)

	// Always the two-round form: this is by definition not the device's first
	// mesh, so it announces under an identity derived for this mesh rather
	// than the base one (ADR-017). Using the base identity here would hand the
	// same public key to two different networks.
	resp, err := invite.RedeemForMesh(ctx, tr, secret,
		&invite.Request{DevicePub: st.Identity.DevicePub, WGPub: st.Identity.WGPub[:], Name: name},
		func(r *invite.Response) (*invite.Request, error) {
			return perMeshRequest(st, r, name)
		})
	if err != nil {
		return nil, fmt.Errorf("no answer — is `shrooms invite` still running there? (%w)", err)
	}

	nk, err := identity.NetworkKeyFromBytes(resp.NetworkKey)
	if err != nil {
		return nil, err
	}
	// The same mesh under two labels is two WireGuard devices deriving one
	// address, which presents as a mesh that half works.
	for _, existing := range cfg.Meshes() {
		if key, err := existing.Key(); err == nil && key.String() == nk.String() {
			return nil, fmt.Errorf("this device is already in that mesh, labelled %q", existing.Label)
		}
	}

	var adminKeys []string
	for _, k := range resp.AdminKeys {
		adminKeys = append(adminKeys, b32.EncodeToString(k))
	}

	if cfg.MeshSet == nil {
		cfg.MeshSet = map[string]state.Mesh{}
	}
	cfg.MeshSet[label] = state.Mesh{
		Label:      label,
		NetworkKey: nk.String(),
		AdminKeys:  adminKeys,
		Relay:      relay,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// The credential is verified and stored before the config is written. The
	// other order leaves a device configured for a mesh it cannot announce on,
	// which looks exactly like a mesh where nobody answers.
	res := &joinResult{
		Mesh:    label,
		Name:    name,
		Overlay: identity.OverlayAddr(nk, st.Identity.DevicePub).String(),
		Prefix:  nk.Prefix().String(),
	}
	if len(resp.Credential) > 0 {
		c, err := cred.UnmarshalCredential(resp.Credential)
		if err != nil {
			return nil, fmt.Errorf("the invite carried a malformed credential: %w", err)
		}
		if auth, err := cfg.MeshSet[label].Authority(); err != nil {
			return nil, err
		} else if auth != nil {
			if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
				return nil, fmt.Errorf("the credential in the invite does not verify: %w", err)
			}
		}
		// legacy=false: an additional mesh announces under its own derived
		// identity, and storing the credential against the base one would have
		// this device announce a key the credential does not name — which every
		// peer then refuses, correctly and silently.
		if err := st.SetMeshCredentialFor(state.NetworkID(nk), false, resp.Credential); err != nil {
			return nil, err
		}
		res.Credential = true
		res.Serial = c.Serial
		res.Expires = time.Unix(c.NotAfter, 0).Format(time.RFC3339)
	}

	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		return nil, err
	}
	log.Info("joined another mesh", "label", label, "prefix", res.Prefix,
		"overlay", res.Overlay, "credential", res.Credential)
	return res, nil
}

// joinHandler is the endpoint form.
//
// The response says the mesh is not running yet, in the daemon's own words,
// because that is the part a UI cannot know and the part that is surprising.
func joinHandler(log *slog.Logger, tr invite.Transport, cfgPath string, st *state.State) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST an invite", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			Token string `json:"token"`
			Name  string `json:"name"`
			Label string `json:"label"`
			// Mesh is the same field under the name the CLI and the waiting
			// daemon use for it. Both spellings are accepted because both
			// callers are real: `shrooms join --mesh work` hands its token to
			// whichever daemon is listening, and until now that was a 404 if
			// the daemon happened to be running.
			Mesh  string `json:"mesh"`
			Relay bool   `json:"relay"`
			WaitS int    `json:"wait_s"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.Label == "" {
			in.Label = in.Mesh
		}

		// The far side has to be sitting in `shrooms invite` at the same
		// moment, so this is the one control endpoint that legitimately takes
		// minutes. Bounded anyway: a request that hangs forever holds a
		// connection nobody will ever close.
		wait := time.Duration(in.WaitS) * time.Second
		if wait <= 0 || wait > 15*time.Minute {
			wait = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()

		res, err := joinAnother(ctx, log, tr, cfgPath, st, in.Token, in.Name, in.Label, in.Relay)
		if err != nil {
			log.Warn("join failed", "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"result": fmt.Sprintf("joined %s as %s; it starts on the next restart",
				res.Mesh, res.Overlay),
			"mesh":       res.Mesh,
			"overlay":    res.Overlay,
			"prefix":     res.Prefix,
			"credential": res.Credential,
			"expires":    res.Expires,
		})
	}
}
