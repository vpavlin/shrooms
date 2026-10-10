package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/invite"
)

// Inviting from an app that cannot sign itself (ADR-050).
//
// `shrooms invite` mints a token, holds the exchange through the daemon, signs
// the credential and replies. Basecamp can hold and reply already (ADR-033);
// these let it do the other two without knowing the token format or the
// credential's wire form, neither of which belongs in a C++ module:
//
//   - /invite/new mints a token and the URI its QR carries;
//   - /invite/draft builds the credential the admin key has to sign, and says
//     what to sign;
//   - /invite/reply takes the draft back with the signature (see
//     inviteHandlers) and finishes it, verified, before it is published.
//
// None of them admits anybody. A token is random bytes; a draft is unsigned and
// every peer refuses it; the signature that admits comes off a card the daemon
// never sees. So the socket group may call them, as it may hold an invite.
func inviteDraftHandlers(mux *http.ServeMux, cfgPath string, pick func(string) inviteHolder, boot func() string) {
	mux.HandleFunc("/invite/new", requireIdentified(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mesh string `json:"mesh"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil && err != io.EOF {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if pick(in.Mesh) == nil {
			http.Error(w, noSuchMesh(cfgPath, in.Mesh), http.StatusBadRequest)
			return
		}
		secret, err := invite.New()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b := ""
		if boot != nil {
			b = boot()
		}
		uri := secret.URIWithBoot(b)
		rows, _ := qrRows(uri) // best effort: the URI is shown either way
		writeJSON(w, map[string]any{
			"qr":    rows,
			"token": secret.String(),
			// What a person reads out or pastes, and what the QR carries.
			"grouped": groupToken(secret.String()),
			"uri":     uri,
			"ttl_s":   int(invite.DefaultTTL / time.Second),
		})
	}))

	mux.HandleFunc("/invite/draft", requireIdentified(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mesh      string `json:"mesh"`
			DevicePub string `json:"device_pub"`
			WGPub     string `json:"wg_pub"`
			SealPub   string `json:"seal_pub"`
			Name      string `json:"name"`
			LifeS     int64  `json:"life_s"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := pick(in.Mesh)
		if m == nil {
			http.Error(w, noSuchMesh(cfgPath, in.Mesh), http.StatusBadRequest)
			return
		}
		auth := m.Authority()
		if auth == nil {
			// Nothing to sign: a mesh with no admin keys admits with the
			// network key alone, and the reply carries no credential.
			writeJSON(w, map[string]any{"no_authority": true})
			return
		}
		dev, err1 := hex.DecodeString(in.DevicePub)
		wg, err2 := hex.DecodeString(in.WGPub)
		seal, err3 := hex.DecodeString(in.SealPub)
		if err1 != nil || err2 != nil || err3 != nil {
			http.Error(w, "device_pub, wg_pub and seal_pub are hex", http.StatusBadRequest)
			return
		}
		life := time.Duration(in.LifeS) * time.Second
		if life <= 0 || life > 366*24*time.Hour {
			life = cred.DefaultLife
		}
		c, digest, err := cred.Draft(auth, dev, wg, seal, in.Name, 0, time.Now(), life)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wire, err := c.MarshalBinary()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		keys := make([]string, 0, len(auth.Keys))
		for _, k := range auth.Keys {
			keys = append(keys, b32.EncodeToString(k))
		}
		writeJSON(w, map[string]any{
			"draft":  base64.StdEncoding.EncodeToString(wire),
			"digest": hex.EncodeToString(digest[:]),
			// The keys that may sign it, as admin_keys spells them, so the
			// caller can find which card account is this mesh's.
			"admin_keys": keys,
			"card_only":  auth.CardOnly(),
			"serial":     c.Serial,
			"not_after":  c.NotAfter,
		})
	}))
}
