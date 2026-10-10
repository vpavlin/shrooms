package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dropFile sends a file to proj the way another agent does: from pi5 (the
// rig's remote), naming its session.
func dropFile(t *testing.T, url, session, name, body, query string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/sessions/proj/drop?name="+name+query, strings.NewReader(body))
	if session != "" {
		req.Header.Set(FromSessionHeader, session)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func allowFiles(t *testing.T, url string, list ...string) {
	t.Helper()
	b := `{"accept_files_from":["` + strings.Join(list, `","`) + `"]}`
	if len(list) == 0 {
		b = `{"accept_files_from":[]}`
	}
	resp, err := http.Post(url+"/v1/sessions/proj/settings", "application/json", strings.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("settings: %d", resp.StatusCode)
	}
}

// Nothing is taken until its sender is allowed; the refused sender is kept
// for the apps to offer, and goes once allowed. Then the file is in a folder
// of its own for that sender, and the session is told.
func TestFilesComeOnlyFromAllowedSenders(t *testing.T) {
	r := newA2A(t)
	s, _ := r.m.Get("proj")
	code, body := dropFile(t, r.remote.URL, "jimmy", "notes.txt", "hello", "", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "files allow proj pi5/jimmy") {
		t.Fatalf("not allowed: %d %s", code, body)
	}
	if in := s.Info(); len(in.FileRequests) != 1 || in.FileRequests[0].From != "pi5/jimmy" || in.FileRequests[0].Name != "notes.txt" {
		t.Errorf("the refused sender is not offered: %+v", in.FileRequests)
	}

	// Ignored, the request goes and nothing is allowed; asked again, it is back.
	http.Post(r.local.URL+"/v1/sessions/proj/settings", "application/json", strings.NewReader(`{"ignore_file_request":"pi5/jimmy"}`))
	if in := s.Info(); len(in.FileRequests) != 0 || len(in.AcceptFilesFrom) != 0 {
		t.Errorf("after ignoring: %+v %+v", in.FileRequests, in.AcceptFilesFrom)
	}
	dropFile(t, r.remote.URL, "jimmy", "notes.txt", "hello", "", nil)

	allowFiles(t, r.local.URL, "pi5/jimmy")
	if in := s.Info(); len(in.FileRequests) != 0 || len(in.AcceptFilesFrom) != 1 {
		t.Errorf("after allowing: %+v %+v", in.FileRequests, in.AcceptFilesFrom)
	}
	code, body = dropFile(t, r.remote.URL, "jimmy", "notes.txt", "hello", "&note=read+this", nil)
	if code != http.StatusCreated {
		t.Fatalf("allowed: %d %s", code, body)
	}
	want := filepath.Join(r.m.dir, "uploads", "proj", "drop", "from-pi5_jimmy", "notes.txt")
	if got, err := os.ReadFile(want); err != nil || string(got) != "hello" {
		t.Fatalf("kept at %s: %q %v", want, got, err)
	}
	ev := waitFor(t, s, 0, func(e Event) bool {
		return e.Kind == "message" && strings.Contains(string(e.Data), "shrooms file from pi5/jimmy")
	})
	if !strings.Contains(string(ev.Data), want) || !strings.Contains(string(ev.Data), "read this") {
		t.Errorf("the note: %s", ev.Data)
	}
	// Sent again, it does not replace the first.
	dropFile(t, r.remote.URL, "jimmy", "notes.txt", "again", "&tell=0", nil)
	if got, _ := os.ReadFile(filepath.Join(filepath.Dir(want), "notes-1.txt")); string(got) != "again" {
		t.Errorf("a second file of the name: %q", got)
	}
	// Another session on the same machine is not allowed by this one.
	if code, _ := dropFile(t, r.remote.URL, "other", "x.txt", "x", "", nil); code != http.StatusForbidden {
		t.Errorf("another session: %d", code)
	}
}

// MACHINE/* allows every session there; a name cannot climb out of the folder;
// a file needs a sending session.
func TestAMachineCanBeAllowedAndNamesStayInTheirFolder(t *testing.T) {
	r := newA2A(t)
	allowFiles(t, r.local.URL, "pi5/*")
	if code, body := dropFile(t, r.remote.URL, "anyone", "..%2F..%2Fescape.txt", "x", "&tell=0", nil); code != http.StatusCreated ||
		!strings.Contains(body, filepath.Join("drop", "from-pi5_anyone", "escape.txt")) {
		t.Errorf("climbed out or refused: %d %s", code, body)
	}
	if code, _ := dropFile(t, r.remote.URL, "", "x.txt", "x", "", nil); code != http.StatusBadRequest {
		t.Errorf("no sending session: %d", code)
	}
	if err := r.m.sessions["proj"].SetAcceptFiles([]string{"nonsense"}, ""); err == nil {
		t.Error("took an entry that is not MACHINE/SESSION")
	}
}

// A file over the limit is refused and nothing is left behind; a sender's
// folder has a cap of its own.
func TestFilesHaveLimits(t *testing.T) {
	r := newA2A(t)
	defer func(a, b int64) { MaxDrop, MaxDropPerSender = a, b }(MaxDrop, MaxDropPerSender)
	MaxDrop, MaxDropPerSender = 10, 15
	allowFiles(t, r.local.URL, "pi5/jimmy")
	if code, _ := dropFile(t, r.remote.URL, "jimmy", "big.bin", strings.Repeat("x", 11), "&tell=0", nil); code == http.StatusCreated {
		t.Error("took a file over the limit")
	}
	if code, _ := dropFile(t, r.remote.URL, "jimmy", "a.bin", strings.Repeat("x", 10), "&tell=0", nil); code != http.StatusCreated {
		t.Fatalf("a file at the limit: %d", code)
	}
	if code, _ := dropFile(t, r.remote.URL, "jimmy", "b.bin", strings.Repeat("x", 8), "&tell=0", nil); code == http.StatusCreated {
		t.Error("the sender's folder went over its cap")
	}
	es, _ := os.ReadDir(filepath.Join(r.m.dir, "uploads", "proj", "drop", "from-pi5_jimmy"))
	if len(es) != 1 {
		t.Errorf("left behind: %d files", len(es))
	}
}

// From a cage, the sender is the session its proxy names, and the receiver
// must take from caged agents as well.
func TestACagedSenderNeedsAcceptCaged(t *testing.T) {
	r := newA2A(t)
	allowFiles(t, r.local.URL, "pi5/boxed")
	caged := map[string]string{cagedHeader: "boxed"}
	if code, body := dropFile(t, r.remote.URL, "claims-other", "x.txt", "x", "&tell=0", caged); code != http.StatusForbidden ||
		!strings.Contains(body, "caged") {
		t.Errorf("a caged sender without accept_caged: %d %s", code, body)
	}
	r.m.sessions["proj"].SetAcceptCaged(true, "")
	if code, body := dropFile(t, r.remote.URL, "claims-other", "x.txt", "x", "&tell=0", caged); code != http.StatusCreated ||
		!strings.Contains(body, "from-pi5_boxed") {
		t.Errorf("a caged sender, allowed: %d %s", code, body)
	}
	if !proxyAllowed(http.MethodPost, "/v1/sessions/x/drop", "") || proxyAllowed(http.MethodPost, "/v1/sessions/x/settings", "") {
		t.Error("the proxy's rule for files")
	}
}

// The apps list what a session was sent, and clear it.
func TestDroppedFilesAreListedAndCleared(t *testing.T) {
	r := newA2A(t)
	allowFiles(t, r.local.URL, "pi5/jimmy")
	dropFile(t, r.remote.URL, "jimmy", "a.txt", "aaa", "&tell=0", nil)
	resp, err := http.Get(r.remote.URL + "/v1/sessions/proj/drop")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Files []Dropped }
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Files) != 1 || got.Files[0].From != "pi5/jimmy" || got.Files[0].Name != "a.txt" || got.Files[0].Size != 3 {
		t.Fatalf("listed: %+v", got.Files)
	}
	del := func(hdr string) int {
		req, _ := http.NewRequest(http.MethodDelete, r.remote.URL+"/v1/sessions/proj/drop?from=pi5/jimmy&file=a.txt", nil)
		if hdr != "" {
			req.Header.Set(cagedHeader, hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := del("boxed"); code != http.StatusForbidden {
		t.Errorf("a cage deleted a file: %d", code)
	}
	if code := del(""); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if fs := r.m.sessions["proj"].Dropped(); len(fs) != 0 {
		t.Errorf("still there: %+v", fs)
	}
}
