package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// unpack reads a packed outbox: name → content.
func unpack(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

// The outbox is the cage's to fill: a link in it could point at this
// machine's keys. Regular files go; links (to a file or a folder) and
// anything else are left out and said so; nothing outside is ever read.
func TestAnOutboxNeverCarriesWhatItPointsAt(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600)
	secretDir := filepath.Join(t.TempDir(), "keys")
	os.MkdirAll(secretDir, 0o700)
	os.WriteFile(filepath.Join(secretDir, "token"), []byte("TOKEN"), 0o600)

	out := filepath.Join(dir, "outbox")
	os.MkdirAll(filepath.Join(out, "review", "shots"), 0o755)
	os.WriteFile(filepath.Join(out, "review", "index.html"), []byte("<h1>report</h1>"), 0o644)
	os.WriteFile(filepath.Join(out, "review", "shots", "a.png"), []byte("png"), 0o644)
	os.Symlink(secret, filepath.Join(out, "review", "report.txt"))
	os.Symlink(secretDir, filepath.Join(out, "keys"))
	syscall.Mkfifo(filepath.Join(out, "pipe"), 0o600)

	p, err := packOutbox(out, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := unpack(t, p.Data)
	if got["review/index.html"] != "<h1>report</h1>" || got["review/shots/a.png"] != "png" || p.Files != 2 {
		t.Errorf("the results: %d files, %v", p.Files, packedNames(got))
	}
	for name, body := range got {
		if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, "TOKEN") && name != "README-UNTRUSTED.txt" {
			t.Errorf("carried what a link pointed at, as %s", name)
		}
	}
	notice := got["README-UNTRUSTED.txt"]
	for _, want := range []string{"untrusted", "review/report.txt (a link: never followed)", "keys (a link: never followed)", "pipe (not a file)"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice does not say %q:\n%s", want, notice)
		}
	}
}

// For a task, only what it wrote while it ran; and within the limits.
func TestAnOutboxSendsWhatIsNewWithinItsLimits(t *testing.T) {
	out := t.TempDir()
	old := filepath.Join(out, "old.txt")
	os.WriteFile(old, []byte("before"), 0o644)
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	os.WriteFile(filepath.Join(out, "new.txt"), []byte("during"), 0o644)
	p, _ := packOutbox(out, time.Now().Add(-time.Minute))
	if got := unpack(t, p.Data); got["new.txt"] != "during" || got["old.txt"] != "" || p.Files != 1 {
		t.Errorf("since the task started: %v", packedNames(got))
	}
	if p, _ := packOutbox(out, time.Now().Add(time.Minute)); p.Files != 0 || p.Data != nil {
		t.Errorf("nothing new, yet packed %d", p.Files)
	}

	defer func(a int, b int64) { MaxOutboxFiles, MaxOutboxBytes = a, b }(MaxOutboxFiles, MaxOutboxBytes)
	MaxOutboxFiles, MaxOutboxBytes = 1, 1<<20
	p, _ = packOutbox(out, time.Time{})
	if p.Files != 1 || !strings.Contains(unpack(t, p.Data)["README-UNTRUSTED.txt"], "over the file limit") {
		t.Errorf("the file limit: %d files", p.Files)
	}
	MaxOutboxFiles, MaxOutboxBytes = 10, 3
	if p, _ := packOutbox(out, time.Time{}); p.Files != 0 {
		t.Errorf("the size limit: %d files", p.Files)
	}
}

// A sealed session's task, done for an agent: its results go to the asker
// as the session, and the asker's note says so.
func TestASealedSessionsResultsGoToTheAsker(t *testing.T) {
	r := newA2A(t)
	n := catchNotes(t, r.m)
	call(t, r.remote.URL+"/a2a/proj", "SendMessage", send("m-s", "review it", false))
	s, _ := r.m.Get("proj")
	for deadline := time.Now().Add(20 * time.Second); s.Info().State != Idle && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	out := t.TempDir()
	s.mu.Lock()
	s.cage = &Cage{Sealed: true, Outbox: out}
	s.mu.Unlock()
	os.WriteFile(filepath.Join(out, "report.md"), []byte("# findings"), 0o644)

	update(t, r.local.URL, "proj:m-s", "done", "reviewed")
	var drop, note string
	for i := 0; i < 2; i++ {
		got, ok := n.wait(t, 5*time.Second)
		if !ok {
			t.Fatalf("only got drop=%q note=%q", drop, note)
		}
		if strings.HasPrefix(got, "/v1/sessions/jimmy/drop") {
			drop = got
		} else {
			note = got
		}
	}
	if drop == "" || !strings.Contains(note, "Its results: sent proj-m-s.tar.gz (1 files) to jimmy") {
		t.Errorf("drop %q, note %q", drop, note)
	}
}

func packedNames(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
