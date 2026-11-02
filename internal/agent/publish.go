package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Publishing (ADR-079): a session puts a file where people and other agents
// read it, a URL on the mesh, instead of mailing it or pasting it into some
// service outside. The agent does the upload: to the mesh's file server when
// the machine has one (--files-url, any server that takes a PUT: dufs, a
// WebDAV share), else into a folder of its own that it serves on its mesh
// addresses, behind the same gate as everything else it serves.
//
// The place is chosen from a fixed set of areas, never the top level: the
// office file server's top level grew back to eighty entries in three weeks
// of agents writing wherever they liked (2026-11-02).

// PublishAreas are where a file may go; tmp/ is emptied of what is older than
// two weeks.
var PublishAreas = map[string]string{
	"projects": "work on something: drafts, builds, screenshots, data",
	"reviews":  "a review or audit and its evidence",
	"releases": "a build somebody installs (releases/APP/VERSION)",
	"notes":    "a note or report meant to be kept",
	"tmp":      "anything short-lived; removed after two weeks",
}

// MaxPublish is the most one file may be: the same as a file sent to a
// session, since a cage's proxy holds it in memory.
var MaxPublish = MaxDrop

// PublishTmpKeep is how long tmp/ keeps a file the agent serves itself.
var PublishTmpKeep = 14 * 24 * time.Hour

var publishSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,99}$`)

// publishPath is the relative path for area, folder and name, or why not.
func publishPath(area, folder, name string) (string, error) {
	if _, ok := PublishAreas[area]; !ok {
		return "", fmt.Errorf("area %q: one of projects, reviews, releases, notes, tmp", area)
	}
	parts := []string{area}
	for _, seg := range strings.Split(strings.Trim(folder, "/"), "/") {
		if seg == "" {
			continue
		}
		if !publishSegment.MatchString(seg) {
			return "", fmt.Errorf("folder %q: letters, digits, dots, dashes and underscores, separated by /", folder)
		}
		parts = append(parts, seg)
	}
	if len(parts) < 2 || len(parts) > 5 {
		return "", errors.New("name a folder in the area (what it belongs to: a project, a review, APP/VERSION), at most four deep")
	}
	if !publishSegment.MatchString(name) {
		return "", fmt.Errorf("file name %q: letters, digits, dots, dashes and underscores", name)
	}
	return path.Join(append(parts, name)...), nil
}

// Publish stores rel from body and says where it is read: one link, or, when
// the agent keeps it, one for each of its meshes, the session's own first.
func (m *Manager) Publish(rel string, body io.Reader, prefer []string) ([]string, int64, error) {
	tmp, err := os.CreateTemp(m.dir, ".publish-*")
	if err != nil {
		return nil, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(body, MaxPublish+1))
	if err != nil {
		return nil, 0, err
	}
	if n > MaxPublish {
		return nil, 0, fmt.Errorf("a published file may be at most %d MB", MaxPublish>>20)
	}
	if m.FilesURL != "" {
		u, err := m.publishRemote(rel, tmp, n)
		return []string{u}, n, err
	}
	dst := filepath.Join(m.publishedRoot(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, 0, err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return nil, 0, err
	}
	os.Chmod(dst, 0o644)
	return m.publishedURLs(rel, prefer), n, nil
}

// publishRemote PUTs the file to the file server and checks it is there,
// whole.
func (m *Manager) publishRemote(rel string, f *os.File, n int64) (string, error) {
	var segs []string
	for _, s := range strings.Split(rel, "/") {
		segs = append(segs, url.PathEscape(s))
	}
	u := strings.TrimRight(m.FilesURL, "/") + "/" + strings.Join(segs, "/")
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	req, _ := http.NewRequest(http.MethodPut, u, f)
	req.ContentLength = n
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("the file server (%s): %w", m.FilesURL, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("the file server refused it: %s", resp.Status)
	}
	head, err := c.Head(u)
	if err != nil {
		return "", fmt.Errorf("uploaded, but the file server does not show it: %w", err)
	}
	head.Body.Close()
	if got, _ := strconv.ParseInt(head.Header.Get("Content-Length"), 10, 64); head.StatusCode != http.StatusOK || got != n {
		return "", fmt.Errorf("uploaded %d bytes, but the file server shows %s with %s", n, head.Status, head.Header.Get("Content-Length"))
	}
	return u, nil
}

func (m *Manager) publishedRoot() string { return filepath.Join(m.dir, "published") }

// publishedURLs are where this agent serves rel: by this machine's name on
// each of its meshes (MACHINE.MESH.mesh, which every member resolves), the
// meshes in prefer first. An address would do only on its own mesh: proteus's
// first was one this laptop is not on, and the link opened nothing
// (2026-11-02). With no mesh, the agent's own socket's name.
func (m *Manager) publishedURLs(rel string, prefer []string) []string {
	port := m.Port
	if port == 0 {
		port = Port
	}
	name := ""
	if ms, err := m.machines(); err == nil && len(ms) > 0 {
		name, _, _ = strings.Cut(ms[0].Name, ".")
	}
	var first, rest []string
	for _, mi := range m.meshes() {
		if name == "" || mi.Label == "" {
			continue
		}
		u := "http://" + net.JoinHostPort(name+"."+mi.Label+".mesh", strconv.Itoa(port)) + "/files/" + rel
		if slices.Contains(prefer, mi.Label) {
			first = append(first, u)
		} else {
			rest = append(rest, u)
		}
	}
	if out := append(first, rest...); len(out) > 0 {
		return out
	}
	return []string{"http://" + net.JoinHostPort("localhost", strconv.Itoa(port)) + "/files/" + rel}
}

// prunePublishedTmp removes what tmp/ has kept past PublishTmpKeep.
func (m *Manager) prunePublishedTmp(now time.Time) int {
	root := filepath.Join(m.publishedRoot(), "tmp")
	n := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) > PublishTmpKeep && os.Remove(p) == nil {
			n++
		}
		return nil
	})
	return n
}

// isPublish is a session's publish, /v1/sessions/{name}/publish.
func isPublish(p string) bool {
	rest, ok := strings.CutPrefix(p, "/v1/sessions/")
	name, tail, _ := strings.Cut(rest, "/")
	return ok && name != "" && tail == "publish"
}

// publish: POST /v1/sessions/{name}/publish?area=&folder=&name=, the body
// the file. Only this machine's own sessions, through the socket or their
// cage's proxy; never a sealed one, whose results leave through its outbox.
func (h *handler) publish(w http.ResponseWriter, r *http.Request) {
	if !overSocket(r) {
		fail(w, http.StatusForbidden, errors.New("a session publishes through its own machine's agent"))
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	if c := r.Header.Get(cagedHeader); c != "" && c != s.Name() {
		fail(w, http.StatusForbidden, errors.New("a cage publishes as its own session only"))
		return
	}
	s.mu.Lock()
	sealed := s.cage != nil && s.cage.Sealed
	s.mu.Unlock()
	if sealed {
		fail(w, http.StatusForbidden, errors.New("a sealed session publishes nothing: its results leave through its outbox"))
		return
	}
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		folder = s.Name()
	}
	rel, err := publishPath(q.Get("area"), folder, q.Get("name"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	own := append([]string(nil), s.meshes...)
	s.mu.Unlock()
	us, n, err := h.m.Publish(rel, r.Body, own)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.mu.Lock()
	s.record("file", s.Name(), map[string]any{"published": us[0], "name": path.Base(rel), "size": n})
	s.mu.Unlock()
	h.log.Info("file published", "session", s.Name(), "url", us[0], "size", n)
	writeJSON(w, http.StatusOK, map[string]any{"url": us[0], "urls": us, "size": n})
}

// published serves what this agent keeps itself: GET /files/{path...}.
func (h *handler) published(w http.ResponseWriter, r *http.Request) {
	rel := path.Clean("/" + r.PathValue("path"))
	if strings.Contains(rel, "/.") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.m.publishedRoot(), filepath.FromSlash(rel)))
}
