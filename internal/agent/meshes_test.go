package agent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// A session limited to the home mesh does not exist for a caller on another:
// not listed, not opened, not asked, not sent files, its tasks hidden. From
// the home mesh, and from this machine, it is all there.
func TestASessionIsOnlyOnItsMeshes(t *testing.T) {
	r := newA2A(t) // remote: pi5 on "default"; local: this machine
	home := httptest.NewServer(Handler(slog.New(slog.DiscardHandler), r.m, func(netip.Addr) string { return "phone.home" }))
	t.Cleanup(home.Close)
	r.m.Meshes = func() []MeshInfo {
		return []MeshInfo{{"default", netip.MustParseAddr("fd00::1")}, {"home", netip.MustParseAddr("fd00::2")}}
	}
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-1", "before", false))

	post := func(url, body string) int {
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(r.local.URL+"/v1/sessions/proj/settings", `{"meshes":["nowhere"]}`); code != http.StatusBadRequest {
		t.Errorf("a mesh this machine is not on: %d", code)
	}
	if code := post(r.local.URL+"/v1/sessions/proj/settings", `{"meshes":["home"]}`); code/100 != 2 {
		t.Fatalf("settings: %d", code)
	}
	listed := func(url string) bool {
		resp, err := http.Get(url + "/v1/sessions")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct{ Sessions []Info }
		json.NewDecoder(resp.Body).Decode(&l)
		return len(l.Sessions) == 1 && l.Sessions[0].Name == "proj"
	}
	if listed(r.remote.URL) || !listed(home.URL) || !listed(r.local.URL) {
		t.Errorf("listed from default/home/here: %v %v %v", listed(r.remote.URL), listed(home.URL), listed(r.local.URL))
	}
	get := func(url, path string) int {
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(r.remote.URL, "/v1/sessions/proj/history"); code != http.StatusNotFound {
		t.Errorf("opened from another mesh: %d", code)
	}
	if code := get(home.URL, "/v1/sessions/proj/history"); code != http.StatusOK {
		t.Errorf("not opened from its own mesh: %d", code)
	}
	if got := call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-2", "from the office", false)); got.Error == nil {
		t.Error("asked from another mesh")
	}
	tasks := func(url string) int {
		resp, err := http.Get(url + "/v1/tasks")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct{ Tasks []a2aTask }
		json.NewDecoder(resp.Body).Decode(&l)
		return len(l.Tasks)
	}
	if tasks(r.remote.URL) != 0 || tasks(home.URL) != 1 {
		t.Errorf("tasks from default/home: %d %d", tasks(r.remote.URL), tasks(home.URL))
	}
	if code := post(r.remote.URL+"/v1/tasks/proj:m-1/cancel", ""); code != http.StatusNotFound {
		t.Errorf("a task cancelled from another mesh: %d", code)
	}
	if code := post(r.remote.URL+"/v1/sessions/proj/rename", `{"name":"x"}`); code != http.StatusNotFound {
		t.Errorf("renamed from another mesh: %d", code)
	}
	// Back to every mesh.
	post(r.local.URL+"/v1/sessions/proj/settings", `{"meshes":[]}`)
	if !listed(r.remote.URL) {
		t.Error("not back on every mesh")
	}
}

// This machine is loopback or one of its own mesh addresses; an address the
// agent cannot name is no one, and on no mesh.
func TestACallerIsHereOrOnAMesh(t *testing.T) {
	m := newTestManager(t, t.TempDir())
	m.Meshes = func() []MeshInfo { return []MeshInfo{{"office", netip.MustParseAddr("fd00::1")}} }
	h := &handler{m: m, who: func(a netip.Addr) string {
		if a == netip.MustParseAddr("fd00::7") {
			return "pi5.office"
		}
		return ""
	}}
	for addr, want := range map[string]string{
		"127.0.0.1:1": "here", "[fd00::1]:1": "here", "[fd00::7]:1": "office", "[fd00::9]:1": "nobody",
	} {
		mesh, local := h.from(&http.Request{RemoteAddr: addr})
		got := map[bool]string{true: "here"}[local]
		if !local {
			got = mesh
			if got == "" {
				got = "nobody"
			}
		}
		if got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}
