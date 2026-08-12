package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// controlFixture writes a two-mesh config and serves the write endpoints
// against it, returning the mux and the path so a test can check what landed on
// disk — which is the point of these endpoints: the file is what a restart
// reads, so a change that is not in it did not happen.
func controlFixture(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	first, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}

	cfg := state.DefaultConfig()
	cfg.Name = "laptop"
	cfg.NetworkKey = first.String()
	cfg.MeshSet = map[string]state.Mesh{
		"test": {Label: "test", NetworkKey: second.String()},
	}
	if err := state.WriteConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	log := slog.New(slog.DiscardHandler)
	controlHandlers(mux, log, path, nil)
	mux.HandleFunc("/leave", leaveHandler(log, path))
	return mux, path
}

func post(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

func reload(t *testing.T, path string) state.Config {
	t.Helper()
	cfg, err := state.LoadConfig(path)
	if err != nil {
		t.Fatalf("the config no longer loads: %v", err)
	}
	return cfg
}

func TestSetName(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/config/name", `{"name":"Living Room NAS"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	// Sanitised on the way in, so what is stored is what will resolve.
	if got := reload(t, path).Name; got != "living-room-nas" {
		t.Errorf("name is %q", got)
	}
}

// A name that sanitises to nothing must be refused rather than stored, or the
// device ends up with no name at all and no explanation.
func TestSetNameRefusesTheUnusable(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/config/name", `{"name":"???"}`); w.Code == 200 {
		t.Error("accepted a name with nothing usable in it")
	}
	if got := reload(t, path).Name; got != "laptop" {
		t.Errorf("the old name was lost anyway: %q", got)
	}
}

func TestSetMode(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/config/mode", `{"mode":"Edge"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := reload(t, path).Mode; got != state.ModeEdge {
		t.Errorf("mode is %q", got)
	}
	if w := post(t, mux, "/config/mode", `{"mode":"Sideways"}`); w.Code == 200 {
		t.Error("accepted a mode that is not one")
	}
}

// A malformed service must not reach the file: the daemon parses these at
// startup, so an invalid one is a service that silently stops existing at the
// next restart.
func TestSetServicesRejectsAMalformedSpec(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/config/services", `{"services":["immich:2283"]}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := reload(t, path).Services; len(got) != 1 || got[0] != "immich:2283" {
		t.Fatalf("services are %v", got)
	}
	if w := post(t, mux, "/config/services", `{"services":["immich:not-a-port"]}`); w.Code == 200 {
		t.Error("accepted a service that cannot be parsed")
	}
	if got := reload(t, path).Services; len(got) != 1 {
		t.Errorf("a rejected change was written anyway: %v", got)
	}
}

func TestSwitchAMeshOffAndOn(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/config/mesh", `{"label":"test","enabled":false}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !reload(t, path).MeshSet["test"].Disabled {
		t.Error("the mesh is still enabled")
	}
	if w := post(t, mux, "/config/mesh", `{"label":"test","enabled":true}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if reload(t, path).MeshSet["test"].Disabled {
		t.Error("the mesh did not come back on")
	}
}

// The mesh a device was built around has nowhere in the config to say "off",
// and switching it off would mean a device that is on but does nothing.
func TestTheFirstMeshCannotBeSwitchedOff(t *testing.T) {
	mux, _ := controlFixture(t)
	if w := post(t, mux, "/config/mesh", `{"label":"default","enabled":false}`); w.Code == 200 {
		t.Error("switched off the mesh the config is written around")
	}
}

func TestLeaveRemovesOnlyThatMesh(t *testing.T) {
	mux, path := controlFixture(t)
	if w := post(t, mux, "/leave", `{"label":"test"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	cfg := reload(t, path)
	if _, still := cfg.MeshSet["test"]; still {
		t.Error("the mesh is still in the config")
	}
	if cfg.NetworkKey == "" {
		t.Error("leaving one mesh took the original with it")
	}

	// And leaving the original is refused, because that is "forget
	// everything" wearing the same button.
	if w := post(t, mux, "/leave", `{"label":"default"}`); w.Code == 200 {
		t.Error("left the mesh the device was built around")
	}
}

// Whatever else changes, the file must still load: a config that does not parse
// is a daemon that will not start, and the next reboot is the worst time to
// find out.
func TestTheConfigStaysLoadable(t *testing.T) {
	mux, path := controlFixture(t)
	post(t, mux, "/config/name", `{"name":"nas"}`)
	post(t, mux, "/config/mode", `{"mode":"Edge"}`)
	post(t, mux, "/config/services", `{"services":["immich:2283","jellyfin:8096"]}`)
	post(t, mux, "/config/mesh", `{"label":"test","enabled":false}`)

	cfg := reload(t, path)
	if cfg.Name != "nas" || cfg.Mode != state.ModeEdge || len(cfg.Services) != 2 {
		t.Errorf("the last write lost an earlier one: %+v", cfg)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// Every endpoint answers in the same shape, so a caller has one thing to parse.
func TestResponsesAreJSON(t *testing.T) {
	mux, _ := controlFixture(t)
	w := post(t, mux, "/config/name", `{"name":"nas"}`)
	body, _ := io.ReadAll(w.Body)
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response is not JSON: %s", body)
	}
	if out["result"] == "" {
		t.Errorf("no result in %s", body)
	}
}

// Announcing services is off until asked, and the endpoint is the only way to
// ask from a UI. Worth its own test because the default is the security
// property: a mesh shared with other people discloses nothing until somebody
// decides otherwise (ADR-023).
func TestAnnounceServicesIsOffUntilAsked(t *testing.T) {
	mux, path := controlFixture(t)
	if reload(t, path).AnnounceServices {
		t.Fatal("announcing was on before anybody asked")
	}
	if w := post(t, mux, "/config/announce", `{"enabled":true}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !reload(t, path).AnnounceServices {
		t.Error("announcing did not turn on")
	}
	if w := post(t, mux, "/config/announce", `{"enabled":false}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if reload(t, path).AnnounceServices {
		t.Error("announcing did not turn off again")
	}
}
