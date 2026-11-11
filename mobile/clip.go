package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/clip"
)

// ClipListener is told of each clip that arrives, for a notification with
// Copy (ADR-083). The JSON is one clip.
type ClipListener interface {
	OnClip(clipJSON string)
}

var clips struct {
	once     sync.Once
	store    *clip.Store
	mu       sync.Mutex
	listener ClipListener
}

// SetClipListener says who to tell when a clip arrives; nil for nobody.
func SetClipListener(l ClipListener) {
	clips.mu.Lock()
	clips.listener = l
	clips.mu.Unlock()
}

// clipStore is the phone's one store, kept beside the config. It outlives
// sessions: a clip written while the tunnel is down goes when it is up.
func clipStore(configDir string) *clip.Store {
	clips.once.Do(func() {
		s := clip.New(filepath.Join(configDir, "clips.json"), clipDirectory, slog.Default())
		s.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			sess := running
			mu.Unlock()
			if sess == nil || sess.ustack == nil {
				return nil, errors.New("the mesh is not running")
			}
			return sess.ustack.DialContext(ctx, network, addr)
		}
		s.Arrived = func(c clip.Clip) {
			clips.mu.Lock()
			l := clips.listener
			clips.mu.Unlock()
			if l != nil {
				b, _ := json.Marshal(c)
				l.OnClip(string(b))
			}
		}
		clips.store = s
		go s.Run(context.Background(), 15*time.Second)
	})
	return clips.store
}

// clipDirectory is who is where on the running session; nobody when there is
// none.
func clipDirectory() ([]clip.Mesh, []clip.Peer) {
	mu.Lock()
	s := running
	mu.Unlock()
	if s == nil {
		return nil, nil
	}
	var meshes []clip.Mesh
	var peers []clip.Peer
	for _, in := range s.instances {
		meshes = append(meshes, clip.Mesh{Label: in.label, Self: s.name, Addr: in.self})
		for _, p := range snapshot(in.mesh, s.suffix, in.label).Peers {
			a, err := netip.ParseAddr(p.Overlay)
			if err != nil {
				continue
			}
			peers = append(peers, clip.Peer{Mesh: in.label, Name: p.Name, Addr: a, Online: p.Online})
		}
	}
	return meshes, peers
}

// serveClips takes clips on each mesh's address, through the session's
// userspace stack. Called once the session is running.
func serveClips(s *session, configDir string, log *slog.Logger) {
	store := clipStore(configDir)
	for _, in := range s.instances {
		ln, err := s.ustack.Listen(netip.AddrPortFrom(in.self, clip.Port))
		if err != nil {
			log.Warn("clips not taken", "mesh", in.label, "err", err)
			continue
		}
		s.clipListeners = append(s.clipListeners, ln)
		go (&http.Server{Handler: store.Handler(in.label), ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}).Serve(ln)
	}
	store.Kick()
}

type clipList struct {
	Received []clip.Clip  `json:"received"`
	Sent     []clip.Clip  `json:"sent"`
	Devices  []clipDevice `json:"devices"`
	Meshes   []string     `json:"meshes"`
}

type clipDevice struct {
	Mesh   string `json:"mesh"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// ClipsJSON is every clip here and the devices one can go to: the same shape
// as the daemon's /clips.
func ClipsJSON(configDir string) string {
	var out clipList
	out.Received, out.Sent = clipStore(configDir).List()
	meshes, peers := clipDirectory()
	for _, m := range meshes {
		out.Meshes = append(out.Meshes, m.Label)
	}
	for _, p := range peers {
		out.Devices = append(out.Devices, clipDevice{p.Mesh, p.Name, p.Online})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ClipSend sends a clip: requestJSON is {"text", "to": [...], "all", "mesh",
// "secret", "ttl"}. Returns the clip.
func ClipSend(configDir, requestJSON string) (string, error) {
	var req clip.Request
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	c, err := clipStore(configDir).Send(req)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(c)
	return string(b), nil
}

// ClipTake is a received clip being copied; a secret goes soon after.
func ClipTake(configDir, id string) (string, error) {
	c, err := clipStore(configDir).Take(id)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(c)
	return string(b), nil
}

// ClipDelete forgets a clip, sent or received.
func ClipDelete(configDir, id string) error {
	return clipStore(configDir).Delete(id)
}
