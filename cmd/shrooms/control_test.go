package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/logtail"
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

// --- the log tail and the restart button --------------------------------

func TestLogsEndpointServesTheTail(t *testing.T) {
	ring := logtail.NewRing(10)
	mux := http.NewServeMux()
	runtimeHandlers(mux, slog.New(slog.DiscardHandler), "", ring, nil)

	// A text handler to nowhere rather than slog.DiscardHandler: that one
	// reports Enabled=false for every level, so a tee in front of it records
	// nothing and this test would pass on an empty tail.
	log := slog.New(logtail.New(slog.NewTextHandler(io.Discard, nil), ring))
	log.Info("tunnel up", "peer", "k11")
	log.Warn("deaf", "mesh", "home")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /logs returned %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Lines []logtail.Line `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(got.Lines), w.Body.String())
	}
	if got.Lines[0].Msg != "tunnel up" || got.Lines[1].Attrs != "mesh=home" {
		t.Errorf("lines came back as %+v", got.Lines)
	}

	// A poller asks for what it has not seen. Everything it already has must
	// stay out, or the pane duplicates every line every two seconds.
	last := got.Lines[0].Time
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/logs?since="+strconv.FormatInt(last, 10), nil))
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, l := range got.Lines {
		if l.Time <= last {
			t.Errorf("since=%d returned a line stamped %d", last, l.Time)
		}
	}
}

// An empty tail must answer with an empty list rather than a JSON null: a
// viewer that does `for (i = 0; i < d.lines.length; i++)` throws on null, and
// the pane that shows the problem is the pane that fails to render.
func TestLogsEndpointEmptyIsAList(t *testing.T) {
	mux := http.NewServeMux()
	runtimeHandlers(mux, slog.New(slog.DiscardHandler), "", logtail.NewRing(4), nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs", nil))
	if !strings.Contains(w.Body.String(), `"lines":[]`) {
		t.Errorf("empty tail served %q", strings.TrimSpace(w.Body.String()))
	}
}

// goodConfig writes a config that loads, for the restart guard: /restart now
// refuses to exit into a config the next start would reject, since the daemon
// is what somebody would fix it through.
func goodConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := state.DefaultConfig()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg.NetworkKey = nk.String()
	if err := state.WriteConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestartRefusesWhenNothingWouldStartUsAgain(t *testing.T) {
	// Neither systemd variable set, and the test process is not pid 1.
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("NOTIFY_SOCKET", "")

	ch := make(chan error, 1)
	mux := http.NewServeMux()
	runtimeHandlers(mux, slog.New(slog.DiscardHandler), "", nil, ch)

	w := post(t, mux, "/restart", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("returned %d, want 409: %s", w.Code, w.Body.String())
	}
	// And crucially it did not exit anyway. A button that reports a refusal
	// and stops the daemon regardless is the worst of both.
	select {
	case err := <-ch:
		t.Fatalf("the daemon was told to stop anyway: %v", err)
	default:
	}
}

func TestRestartExitsWhenSomethingWouldStartUsAgain(t *testing.T) {
	t.Setenv("INVOCATION_ID", "pretend-systemd-ran-us")

	ch := make(chan error, 1)
	mux := http.NewServeMux()
	runtimeHandlers(mux, slog.New(slog.DiscardHandler), goodConfig(t), nil, ch)

	w := post(t, mux, "/restart", "")
	if w.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", w.Code, w.Body.String())
	}
	// The response comes first and the exit follows, deliberately, so the
	// caller sees a result instead of a closed connection.
	select {
	case err := <-ch:
		if !errors.Is(err, errRestartRequested) {
			t.Errorf("ended with %v, want the restart sentinel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was sent on the exit channel")
	}
}

func TestRestartRefusesGet(t *testing.T) {
	mux := http.NewServeMux()
	runtimeHandlers(mux, slog.New(slog.DiscardHandler), goodConfig(t), nil, make(chan error, 1))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/restart", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /restart returned %d, want 405", w.Code)
	}
}

// --- joining another mesh ------------------------------------------------

// The label checks, which are the whole of what this endpoint decides before
// it goes near the network. Each of these would otherwise fail minutes later,
// after a redemption that cannot be undone.
func TestJoinAnotherRefusesBadLabels(t *testing.T) {
	_, path := controlFixture(t)
	st := &state.State{}
	log := slog.New(slog.DiscardHandler)

	for _, tc := range []struct {
		name, label, want string
	}{
		{"empty", "", "needs a label"},
		{"default", state.DefaultLabel, "label of its own"},
		{"already joined", "test", "already in a mesh labelled"},
		{"sanitises to nothing", "///", "needs a label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := joinAnother(context.Background(), log, nil, path, st,
				"tok", "laptop", tc.label, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// A label nothing else has taken gets past the config checks and stops at the
// transport, which is where it should stop when there is no rendezvous
// connection — not by starting a second node of its own.
func TestJoinAnotherNeedsATransport(t *testing.T) {
	_, path := controlFixture(t)
	_, err := joinAnother(context.Background(), slog.New(slog.DiscardHandler), nil, path,
		&state.State{}, "tok", "laptop", "work", false)
	if err == nil || !strings.Contains(err.Error(), "rendezvous plane is not available") {
		t.Fatalf("got %v, want the missing-transport error", err)
	}
	// And nothing was written on the way to that error: a config that gained a
	// keyless mesh entry is a daemon that will not start.
	cfg := reload(t, path)
	if _, ok := cfg.MeshSet["work"]; ok {
		t.Error("a failed join left a mesh in the config")
	}
}

// --- the settings that had no UI ----------------------------------------

// Per-mesh flags live in two different places: the first mesh's at the top
// level (the single-mesh config form), the rest under [mesh.<label>]. Every
// caller that knew this got it wrong for the first mesh at least once, which
// is why the endpoints take a label and decide for themselves.
func TestPerMeshFlagsLandInTheRightPlace(t *testing.T) {
	mux, path := controlFixture(t)

	for _, tc := range []struct {
		name, url, body string
		check           func(state.Config) bool
	}{
		{"relay on the first mesh", "/config/relay", `{"enabled":true}`,
			func(c state.Config) bool { return c.Relay && !c.MeshSet["test"].Relay }},
		{"relay on a named mesh", "/config/relay", `{"label":"test","enabled":true}`,
			func(c state.Config) bool { return c.MeshSet["test"].Relay }},
		{"announce on the first mesh", "/config/announce", `{"enabled":true}`,
			func(c state.Config) bool { return c.AnnounceServices }},
		{"announce on a named mesh", "/config/announce", `{"label":"test","enabled":true}`,
			func(c state.Config) bool { return c.MeshSet["test"].AnnounceServices }},
		{"bound on a named mesh", "/config/announce-bound", `{"label":"test","enabled":true}`,
			func(c state.Config) bool { return c.MeshSet["test"].AnnounceBound }},
		{"port mapping off", "/config/portmap", `{"enabled":false}`,
			func(c state.Config) bool { return !c.PortMapping }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := post(t, mux, tc.url, tc.body); w.Code != http.StatusOK {
				t.Fatalf("returned %d: %s", w.Code, w.Body.String())
			}
			if !tc.check(reload(t, path)) {
				t.Error("the setting did not land where it belongs")
			}
		})
	}
}

// A label nobody has joined is a typo, not a request to create a mesh.
func TestPerMeshFlagRejectsUnknownLabel(t *testing.T) {
	mux, _ := controlFixture(t)
	w := post(t, mux, "/config/relay", `{"label":"nowhere","enabled":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nowhere") {
		t.Errorf("the refusal does not name the mesh: %q", w.Body.String())
	}
}
