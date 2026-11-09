package agent

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A task list as a phone polls it: brief=1 keeps what a row shows, cut short,
// and the answer is gzipped for a client that asks (2026-11-09: 290 KB every
// few seconds drained a phone).
func TestTaskListBriefAndGzipped(t *testing.T) {
	r := newA2A(t)
	long := strings.Repeat("a long request ", 400) // ~6 KB
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-1", long, false))
	if code := update(t, r.local.URL, "proj:m-1", "done", strings.Repeat("a long answer ", 400)); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	get := func(path string, gz bool) ([]byte, http.Header) {
		req, _ := http.NewRequest("GET", r.local.URL+path, nil)
		if gz {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		// A transport that leaves the encoding alone, to see it as sent.
		resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b, resp.Header
	}
	full, _ := get("/v1/tasks", false)
	brief, _ := get("/v1/tasks?brief=1", false)
	if len(brief)*4 > len(full) {
		t.Fatalf("brief is %d bytes against %d in full", len(brief), len(full))
	}
	var l struct{ Tasks []a2aTask }
	if err := json.Unmarshal(brief, &l); err != nil || len(l.Tasks) != 1 {
		t.Fatalf("brief list: %v %s", err, brief)
	}
	tk := l.Tasks[0]
	if len(tk.History) != 1 || len([]rune(tk.History[0].Parts[0].Text)) > briefText+1 || !strings.HasPrefix(tk.History[0].Parts[0].Text, "a long request") {
		t.Errorf("the request in brief: %+v", tk.History)
	}
	if tk.Status.Message == nil || len([]rune(tk.Status.Message.Parts[0].Text)) > briefText+1 || len(tk.Artifacts) != 0 {
		t.Errorf("the answer in brief: %+v, artifacts %d", tk.Status.Message, len(tk.Artifacts))
	}
	// The full list is untouched by brief having cut its copy.
	whole := func(b []byte) bool {
		var l struct{ Tasks []a2aTask }
		return json.Unmarshal(b, &l) == nil && len(l.Tasks) == 1 && len(l.Tasks[0].History) == 1 &&
			l.Tasks[0].History[0].Parts[0].Text == long
	}
	if again, _ := get("/v1/tasks", false); !whole(again) {
		t.Error("a brief list cut the full one")
	}

	zipped, h := get("/v1/tasks", true)
	if h.Get("Content-Encoding") != "gzip" || len(zipped)*5 > len(full) {
		t.Fatalf("gzip: %q, %d bytes against %d", h.Get("Content-Encoding"), len(zipped), len(full))
	}
	zr, err := gzip.NewReader(strings.NewReader(string(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	if un, _ := io.ReadAll(zr); !whole(un) {
		t.Error("gzipped list does not unpack to the full list")
	}
	if _, h := get("/v1/sessions", true); h.Get("Content-Encoding") != "gzip" {
		t.Error("the session list is not gzipped")
	}
}
