// Package clip is the shared clipboard (ADR-083): a short text sent from one
// device to others on the same mesh, named one by one or all of a mesh, and
// kept a while for picking up.
//
// It rides the mesh and nothing else. A clip goes from the sender's overlay
// address to each recipient's, over WireGuard, as one HTTP request; the
// recipient knows who sent it by the address it came from, which WireGuard
// only accepts from the peer that holds it. Nothing goes over Waku, and a clip
// reaches only the devices it names: there is no broadcast filtered on arrival.
//
// The package does not know how it is reached. The daemon gives it kernel
// sockets; the phone, whose app is outside its own tunnel, a userspace stack.
package clip

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// Port is where every device takes clips, on each of its overlay addresses.
const Port = 9181

// MaxText bounds one clip. A URL, a key, an ID: this is not file transfer.
const MaxText = 64 << 10

const (
	// DefaultTTL is how long a clip waits for its recipients, and is kept by
	// them after.
	DefaultTTL = 24 * time.Hour
	// SecretTTL is the same for a secret: a key should not lie about.
	SecretTTL = time.Hour
	// MaxTTL caps what a sender may ask a recipient to keep.
	MaxTTL = 7 * 24 * time.Hour
	// takenSecret is how long a secret stays once copied: long enough to
	// copy it again after a slip, not long enough to forget it is there.
	takenSecret = 5 * time.Minute
	// keep bounds each list; the oldest go first.
	keep = 50
)

// Mesh is one mesh this device is on.
type Mesh struct {
	Label string
	Self  string     // this device's name on it
	Addr  netip.Addr // this device's overlay address on it
}

// Peer is another device on one of those meshes.
type Peer struct {
	Mesh   string
	Name   string
	Addr   netip.Addr
	Online bool
}

// Directory says who is where, now. It is asked on every send and every
// arrival rather than kept, so a peer renamed or gone is never written to.
type Directory func() ([]Mesh, []Peer)

// Clip is one clip, as either side keeps it.
type Clip struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Secret  bool      `json:"secret,omitempty"`
	Mesh    string    `json:"mesh"`
	From    string    `json:"from,omitempty"` // received: who sent it
	To      []Target  `json:"to,omitempty"`   // sent: each recipient and how it went
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Taken   time.Time `json:"taken,omitzero"` // received: first copied
}

// Target is one recipient of a sent clip.
type Target struct {
	Name  string    `json:"name"`
	Sent  time.Time `json:"sent,omitzero"`
	Error string    `json:"error,omitempty"` // the last attempt's, while it waits
}

// Delivered is whether every recipient has it.
func (c Clip) Delivered() bool {
	for _, t := range c.To {
		if t.Sent.IsZero() {
			return false
		}
	}
	return true
}

// Request is what a user asks to send.
type Request struct {
	Text   string   `json:"text"`
	To     []string `json:"to,omitempty"` // device names; "name.mesh" picks the mesh too
	All    bool     `json:"all,omitempty"`
	Mesh   string   `json:"mesh,omitempty"`
	Secret bool     `json:"secret,omitempty"`
	TTL    int64    `json:"ttl,omitempty"` // seconds; 0 for the default
}

// wire is what travels. The sender's name is not in it: the recipient takes
// that from the address, which cannot be claimed.
type wire struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Secret  bool   `json:"secret,omitempty"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
}

var (
	ErrEmpty    = errors.New("nothing to send")
	ErrTooLong  = errors.New("too long for a clip")
	ErrNoTarget = errors.New("say who it is for: device names, or all of a mesh")
	ErrNotFound = errors.New("no such clip")
)

// resolve turns a request into one mesh and its recipients. A clip goes on one
// mesh: the one named, or the first that has every recipient.
func resolve(req Request, meshes []Mesh, peers []Peer) (string, []string, error) {
	if len(meshes) == 0 {
		return "", nil, errors.New("not on any mesh")
	}
	mesh := req.Mesh
	names := make([]string, 0, len(req.To))
	for _, n := range req.To {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		// "laptop.home": the device laptop on mesh home.
		if i := strings.LastIndexByte(n, '.'); i > 0 && hasMesh(meshes, n[i+1:]) {
			if mesh != "" && mesh != n[i+1:] {
				return "", nil, errors.New("one clip goes on one mesh: " + n + " is on another")
			}
			mesh, n = n[i+1:], n[:i]
		}
		names = append(names, n)
	}
	if mesh != "" && !hasMesh(meshes, mesh) {
		return "", nil, errors.New("not on a mesh called " + mesh)
	}
	if req.All {
		if mesh == "" {
			// "All" never means every mesh: the office mesh is not only mine.
			if len(meshes) > 1 {
				return "", nil, errors.New("all of which mesh? this device is on " + labels(meshes))
			}
			mesh = meshes[0].Label
		}
		seen := map[string]bool{}
		for _, n := range names {
			seen[n] = true
		}
		for _, p := range peers {
			if p.Mesh == mesh && !seen[p.Name] {
				seen[p.Name] = true
				names = append(names, p.Name)
			}
		}
		if len(names) == 0 {
			return "", nil, errors.New("nobody else is on " + mesh)
		}
		return mesh, names, nil
	}
	if len(names) == 0 {
		return "", nil, ErrNoTarget
	}
	candidates := []string{mesh}
	if mesh == "" {
		candidates = candidates[:0]
		for _, m := range meshes {
			candidates = append(candidates, m.Label)
		}
	}
	var missing string
	for _, m := range candidates {
		missing = ""
		for _, n := range names {
			if !onMesh(peers, m, n) {
				missing = n
				break
			}
		}
		if missing == "" {
			return m, dedupe(names), nil
		}
	}
	if mesh != "" {
		return "", nil, errors.New("no device called " + missing + " on " + mesh)
	}
	return "", nil, errors.New("no one mesh has all of " + strings.Join(names, ", "))
}

func hasMesh(meshes []Mesh, label string) bool {
	for _, m := range meshes {
		if m.Label == label {
			return true
		}
	}
	return false
}

func labels(meshes []Mesh) string {
	l := make([]string, len(meshes))
	for i, m := range meshes {
		l[i] = m.Label
	}
	return strings.Join(l, ", ")
}

func onMesh(peers []Peer, mesh, name string) bool {
	_, ok := find(peers, mesh, name)
	return ok
}

func find(peers []Peer, mesh, name string) (Peer, bool) {
	for _, p := range peers {
		if p.Mesh == mesh && p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ttlFor is the life a request asks for, within bounds.
func ttlFor(req Request) time.Duration {
	d := time.Duration(req.TTL) * time.Second
	switch {
	case d <= 0 && req.Secret:
		return SecretTTL
	case d <= 0:
		return DefaultTTL
	case d > MaxTTL:
		return MaxTTL
	}
	return d
}
