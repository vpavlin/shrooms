package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func publishAs(t *testing.T, base, query, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(base+"/v1/sessions/proj/publish?"+query, "application/octet-stream", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// A session publishes a file through its own agent, into an area and a
// folder, never the top level; with no file server the agent serves it
// itself, to the mesh only. Another device cannot publish through it.
func TestPublishKeptAndServedByTheAgent(t *testing.T) {
	r := newA2A(t)
	code, out := publishAs(t, r.local.URL, "area=projects&name=report.md", "# hello")
	if code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, out)
	}
	u, _ := out["url"].(string)
	if !strings.HasSuffix(u, "/files/projects/proj/report.md") {
		t.Errorf("url: %s", u)
	}
	// By name on each mesh, the session's own first.
	r.m.Meshes = func() []MeshInfo {
		return []MeshInfo{{Label: "review", Addr: netip.MustParseAddr("fd4a::1")}, {Label: "office", Addr: netip.MustParseAddr("fdb0::1")}}
	}
	r.m.Machines = func() ([]Machine, error) { return []Machine{{Name: "proteus", Addr: netip.MustParseAddr("fdb0::1")}}, nil }
	us := r.m.publishedURLs("tmp/x", []string{"office"})
	if len(us) != 2 || !strings.HasPrefix(us[0], "http://proteus.office.mesh:") || !strings.Contains(us[1], ".review.mesh:") || strings.Contains(us[0], "fdb0") {
		t.Errorf("urls: %v", us)
	}
	resp, err := http.Get(r.local.URL + "/files/projects/proj/report.md")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "# hello" {
		t.Errorf("served: %d %q", resp.StatusCode, b)
	}
	if code, _ := publishAs(t, r.remote.URL, "area=projects&name=x.md", "x"); code != http.StatusForbidden {
		t.Errorf("a device on the mesh published through this agent: %d", code)
	}
	for _, q := range []string{"area=root&name=x.md", "area=tmp&name=../x", "area=tmp&folder=a/../../b&name=x", "area=tmp&name=.hidden",
		"area=projects&folder=a/b/c/d/e&name=x"} {
		if code, out := publishAs(t, r.local.URL, q, "x"); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", q, code, out)
		}
	}
	s, _ := r.m.Get("proj")
	s.mu.Lock()
	s.cage = &Cage{Container: "c", Sealed: true}
	s.mu.Unlock()
	if code, _ := publishAs(t, r.local.URL, "area=tmp&name=x.md", "x"); code != http.StatusForbidden {
		t.Errorf("a sealed session published: %d", code)
	}
}

// With a file server, the agent PUTs the file there and checks it arrived
// whole; a server that shows something else is an error, not a link.
func TestPublishToTheFileServer(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	short := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			files[r.URL.Path] = b
			w.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			b, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if short {
				b = b[:1]
			}
			w.Header().Set("Content-Length", fmtInt(len(b)))
		}
	}))
	defer srv.Close()
	r := newA2A(t)
	r.m.FilesURL = srv.URL + "/"
	code, out := publishAs(t, r.local.URL, "area=releases&folder=whisperbox/0.5.3&name=wb.apk", "apk bytes")
	if code != http.StatusOK || out["url"] != srv.URL+"/releases/whisperbox/0.5.3/wb.apk" {
		t.Fatalf("publish: %d %v", code, out)
	}
	if !bytes.Equal(files["/releases/whisperbox/0.5.3/wb.apk"], []byte("apk bytes")) {
		t.Errorf("stored: %q", files)
	}
	short = true
	if code, out := publishAs(t, r.local.URL, "area=tmp&name=a.txt", "abc"); code == http.StatusOK {
		t.Errorf("a short copy was reported published: %v", out)
	}
}

// tmp/ keeps what the agent serves for two weeks.
func TestPublishedTmpIsPruned(t *testing.T) {
	r := newA2A(t)
	publishAs(t, r.local.URL, "area=tmp&name=old.txt", "x")
	publishAs(t, r.local.URL, "area=notes&name=kept.txt", "x")
	old := time.Now().Add(-15 * 24 * time.Hour)
	p := filepath.Join(r.m.publishedRoot(), "tmp", "proj", "old.txt")
	os.Chtimes(p, old, old)
	k := filepath.Join(r.m.publishedRoot(), "notes", "proj", "kept.txt")
	os.Chtimes(k, old, old)
	if n := r.m.prunePublishedTmp(time.Now()); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if _, err := os.Stat(k); err != nil {
		t.Error("a note outside tmp/ was removed")
	}
}

func fmtInt(n int) string { return strconv.Itoa(n) }
