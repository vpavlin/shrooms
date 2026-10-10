package agent

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Sessions per mesh (docs/agents.md, "Meshes"): a session can be limited to
// some of the meshes its machine is on. From any other it does not exist —
// not listed, not opened, not asked, not sent files. A machine on a home mesh
// and an office one can then keep a session for the household that the
// office cannot see, and the other way round. Empty is every mesh, as before.

// MeshInfo is one of this machine's meshes: its label here, and its address.
type MeshInfo struct {
	Label string     `json:"label"`
	Addr  netip.Addr `json:"addr"`
}

// meshes is this machine's meshes, when the agent knows them.
func (m *Manager) meshes() []MeshInfo {
	if m.Meshes == nil {
		return nil
	}
	return m.Meshes()
}

// from says where a request came from: this machine (loopback, or one of
// its own mesh addresses), or a peer on a mesh, by the label this machine
// gives that mesh ("" when the caller is not a peer this agent knows).
func (h *handler) from(r *http.Request) (mesh string, local bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	if h.who != nil {
		if name := h.who(a); name != "" {
			_, mesh, _ := strings.Cut(name, ".")
			return mesh, false
		}
	}
	if a.IsLoopback() {
		return "", true
	}
	for _, mi := range h.m.meshes() {
		if mi.Addr == a {
			return "", true
		}
	}
	return "", false
}

// servesFrom is whether the session is there for a caller on mesh.
func (s *Session) servesFrom(mesh string, local bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return local || len(s.meshes) == 0 || mesh != "" && slices.Contains(s.meshes, mesh)
}

// get is a session as the caller may see it: missing when it is not there
// for the mesh the request came from.
func (h *handler) get(r *http.Request, name string) (*Session, bool) {
	s, ok := h.m.Get(name)
	if !ok {
		return nil, false
	}
	mesh, local := h.from(r)
	if !s.servesFrom(mesh, local) {
		return nil, false
	}
	return s, true
}

// visible is whether a session by that name is there for the caller.
func (h *handler) visible(r *http.Request, name string) bool {
	_, ok := h.get(r, name)
	return ok
}

// SetMeshes sets the meshes the session is there for; none is all of them.
func (s *Session) SetMeshes(labels []string, known []MeshInfo, by string) error {
	clean := []string{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || slices.Contains(clean, l) {
			continue
		}
		if known != nil && !slices.ContainsFunc(known, func(mi MeshInfo) bool { return mi.Label == l }) {
			return fmt.Errorf("this machine is not on a mesh called %q", l)
		}
		clean = append(clean, l)
	}
	s.mu.Lock()
	s.meshes = clean
	s.record("setting", by, map[string][]string{"meshes": clean})
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}

// meshList is GET /v1/meshes: this machine's meshes, for the apps to offer.
func (h *handler) meshList(w http.ResponseWriter, r *http.Request) {
	out := []string{}
	for _, mi := range h.m.meshes() {
		out = append(out, mi.Label)
	}
	writeJSON(w, http.StatusOK, map[string]any{"meshes": out})
}
