package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/mesh"
)

// Renewing and revoking from an app, with a card (ADR-051).
//
// /grant and /revoke were root's, with the reason written beside them: the
// socket decides who may ask, the admin signature inside decides whether
// anything happens. For a mesh whose admin keys are all on cards that reason
// is the whole story — whoever asks, only a card could have signed — which is
// the argument ADR-033 made for replying to an invite. For a mesh with a key
// file it is not: anybody who runs as that file's owner can sign. So the
// socket group may renew and revoke on a card-only mesh, with a draft from
// here and a signature from the card, and root alone may do it otherwise.

// isRootCaller is callerIsRoot, swappable so a test — which always runs as
// the daemon's own uid — can be the socket group.
var isRootCaller = callerIsRoot

// adminTarget is what these endpoints need from a running mesh.
type adminTarget interface {
	Authority() *cred.Authority
	Members() []mesh.Member
	Grant([]byte) error
	Revoke([]byte) error
}

// signedDraft is what an app sends back: the draft it was given and the
// signature a card made over its digest.
type signedDraft struct {
	Draft     string `json:"draft"`
	Signature string `json:"signature"`
}

// readAdminBody reads a /grant or /revoke body: the base64 blob the CLI has
// always sent, or a signed draft. The second form is the only one the socket
// group may use, and only on a card-only mesh.
func readAdminBody(r *http.Request, auth *cred.Authority, finish func([]byte, []byte) ([]byte, error)) ([]byte, int, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	body := strings.TrimSpace(string(raw))
	root := isRootCaller(r)
	if !strings.HasPrefix(body, "{") {
		if !root {
			return nil, http.StatusForbidden, errors.New("this needs root, or a draft from this daemon " +
				"signed by the mesh's card")
		}
		blob, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("not base64: %w", err)
		}
		return blob, 0, nil
	}
	if !root && !auth.CardOnly() {
		return nil, http.StatusForbidden, errors.New("this mesh's admin key is a file rather than a card, " +
			"so a signature proves nothing about who asked. That needs root")
	}
	var in signedDraft
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, http.StatusBadRequest, err
	}
	draft, err := base64.StdEncoding.DecodeString(in.Draft)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("draft is not base64: %w", err)
	}
	sig, err := hex.DecodeString(in.Signature)
	if err != nil || len(sig) == 0 {
		return nil, http.StatusBadRequest, errors.New("signature is hex, and needed")
	}
	out, err := finish(draft, sig)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	return out, 0, nil
}

// mayReadMembers says whether a caller may list a mesh's members: root, or
// the socket group on a card-only mesh, where it is what renewing from an app
// starts from. Public keys and names, all of which peers announce anyway.
func mayReadMembers(r *http.Request, auth *cred.Authority) error {
	if isRootCaller(r) || auth.CardOnly() {
		return nil
	}
	return errors.New("listing members is for renewing, which on this mesh needs its key file; that needs root")
}

func adminKeyNames(auth *cred.Authority) []string {
	out := make([]string, 0, len(auth.Keys))
	for _, k := range auth.Keys {
		out = append(out, b32.EncodeToString(k))
	}
	return out
}

// cardAdminHandlers registers the drafts an app signs with a card.
func cardAdminHandlers(mux *http.ServeMux, pick func(string) adminTarget) {
	// Who is due, and the credential each would be renewed with.
	mux.HandleFunc("/renew/draft", requireIdentified(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mesh    string `json:"mesh"`
			WithinS int64  `json:"within_s"`
			All     bool   `json:"all"`
			LifeS   int64  `json:"life_s"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil && err != io.EOF {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := pick(in.Mesh)
		if m == nil {
			http.Error(w, "no such mesh is running here", http.StatusNotFound)
			return
		}
		auth := m.Authority()
		if auth == nil {
			http.Error(w, "this mesh has no admin keys, so nothing expires", http.StatusBadRequest)
			return
		}
		if err := mayReadMembers(r, auth); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		within := time.Duration(in.WithinS) * time.Second
		if within <= 0 {
			within = renewWindow
		}
		life := time.Duration(in.LifeS) * time.Second
		if life <= 0 || life > 366*24*time.Hour {
			life = cred.DefaultLife
		}
		type draft struct {
			Name      string `json:"name"`
			DevicePub string `json:"device_pub"`
			NotAfter  int64  `json:"not_after,omitempty"`
			Draft     string `json:"draft"`
			Digest    string `json:"digest"`
		}
		now := time.Now()
		out := []draft{}
		for _, mem := range m.Members() {
			if !in.All && !mem.NotAfter.IsZero() && mem.NotAfter.Sub(now) > within {
				continue
			}
			c, d, err := cred.Draft(auth, mem.DevicePub, mem.WGPub, mem.SealPub, mem.Name, 0, now, life)
			if err != nil {
				continue
			}
			wire, err := c.MarshalBinary()
			if err != nil {
				continue
			}
			e := draft{Name: mem.Name, DevicePub: hex.EncodeToString(mem.DevicePub),
				Draft: base64.StdEncoding.EncodeToString(wire), Digest: hex.EncodeToString(d[:])}
			if !mem.NotAfter.IsZero() {
				e.NotAfter = mem.NotAfter.Unix()
			}
			out = append(out, e)
		}
		writeJSON(w, map[string]any{"drafts": out, "admin_keys": adminKeyNames(auth), "card_only": auth.CardOnly()})
	}))

	// The withdrawal of one device, to be signed.
	mux.HandleFunc("/revoke/draft", requireIdentified(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mesh      string `json:"mesh"`
			DevicePub string `json:"device_pub"`
			KeepS     int64  `json:"keep_s"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := pick(in.Mesh)
		if m == nil {
			http.Error(w, "no such mesh is running here", http.StatusNotFound)
			return
		}
		auth := m.Authority()
		if auth == nil {
			http.Error(w, "this mesh has no admin keys; a device leaves it only with a new network key",
				http.StatusBadRequest)
			return
		}
		dev, err := hex.DecodeString(in.DevicePub)
		if err != nil {
			http.Error(w, "device_pub is hex", http.StatusBadRequest)
			return
		}
		// As long as `shrooms admin revoke` keeps it by default: past the
		// longest credential a device could still hold.
		keep := time.Duration(in.KeepS) * time.Second
		if keep <= 0 {
			keep = cred.DefaultLife + 24*time.Hour
		}
		rv, d, err := cred.DraftRevocation(auth, dev, 0, time.Now(), keep)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wire, err := rv.MarshalBinary()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"draft": base64.StdEncoding.EncodeToString(wire),
			"digest": hex.EncodeToString(d[:]), "admin_keys": adminKeyNames(auth), "card_only": auth.CardOnly()})
	}))
}
