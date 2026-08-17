package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The loopback port used to be handed the same mux as the unix socket, so every
// mutating endpoint was on it: /config/*, /join, /leave, /reload, /restart. The
// socket decides who may call those by file mode and SO_PEERCRED, and neither
// is available over TCP — no ConnContext is set, so peer-cred is absent and
// only the root-gated handlers failed closed. The group-tier ones were not
// gated at all.
//
// This test states the boundary as a property rather than a convention: the UI
// mux answers /status and nothing else. It builds the two muxes the way the
// daemon does, because what matters is which handler landed on which listener.
func TestUIListenerServesOnlyStatus(t *testing.T) {
	status := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	}

	uiMux := readOnlyMux(status)

	// Reading is allowed.
	w := httptest.NewRecorder()
	uiMux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /status = %d, want 200", w.Code)
	}

	// Everything that changes something is not merely refused — it is not
	// there. A 404 means no handler was ever registered, which is a stronger
	// statement than a handler that checks and declines.
	for _, path := range []string{
		"/config/name", "/config/services", "/config/relay", "/config/mesh",
		"/config/portmap", "/config/announce", "/config/announce-bound", "/config/mode",
		"/join", "/leave", "/reload", "/restart", "/revoke", "/grant",
		"/invite/hold", "/invite/reply", "/members", "/logs",
	} {
		w := httptest.NewRecorder()
		uiMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Errorf("POST %s on the UI listener = %d, want 404 (not registered)", path, w.Code)
		}
	}
}

// A browser can reach a loopback port from any page: plain HTTP, no Origin
// check, and a text/plain POST does not trigger a preflight. So even the one
// endpoint that is served must refuse to be used as a verb it is not.
func TestUIStatusRefusesWrites(t *testing.T) {
	status := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	uiMux := readOnlyMux(status)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := httptest.NewRecorder()
		uiMux.ServeHTTP(w, httptest.NewRequest(method, "/status", strings.NewReader("{}")))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /status = %d, want 405", method, w.Code)
		}
	}
}
