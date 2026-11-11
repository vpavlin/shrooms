package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Store keeps this device's clips, sends what is waiting, and takes what
// arrives.
type Store struct {
	dir  Directory
	path string // where the clips that are not secret are kept; "" for nowhere
	log  *slog.Logger

	// Dial reaches a peer's overlay address. nil is the kernel's; the phone's
	// is a userspace stack (its app is outside its own tunnel).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Arrived is told of each new clip, for a notification.
	Arrived func(Clip)
	// now is the clock; a test's own.
	now func() time.Time

	mu       sync.Mutex
	received []Clip
	sent     []Clip
	kick     chan struct{}
	client   *http.Client
	// retry is when each recipient that failed is tried again, by mesh and
	// name. In memory: a restart trying everyone once more is no loss.
	retry map[string]backoff
}

// New opens the store kept at path, or one kept nowhere when path is "".
func New(path string, dir Directory, log *slog.Logger) *Store {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Store{dir: dir, path: path, log: log, now: time.Now, kick: make(chan struct{}, 1)}
	s.load()
	return s
}

// kept is the file's form.
type kept struct {
	Received []Clip `json:"received"`
	Sent     []Clip `json:"sent"`
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var k kept
	if json.Unmarshal(b, &k) != nil {
		s.log.Warn("clips unreadable; starting empty", "path", s.path)
		return
	}
	s.received, s.sent = k.Received, k.Sent
	s.pruneLocked()
}

// saveLocked writes what is not secret. A secret lives in memory only, on
// both sides: a restart losing one is the point.
func (s *Store) saveLocked() {
	if s.path == "" {
		return
	}
	plain := func(cs []Clip) []Clip {
		out := []Clip{}
		for _, c := range cs {
			if !c.Secret {
				out = append(out, c)
			}
		}
		return out
	}
	b, _ := json.Marshal(kept{Received: plain(s.received), Sent: plain(s.sent)})
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		s.log.Warn("clips not kept", "err", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.log.Warn("clips not kept", "err", err)
		return
	}
	os.Rename(tmp, s.path)
}

// pruneLocked drops what has expired, and the oldest past the bound.
func (s *Store) pruneLocked() bool {
	now := s.now()
	live := func(cs []Clip) []Clip {
		out := cs[:0]
		for _, c := range cs {
			if c.Expires.After(now) {
				out = append(out, c)
			}
		}
		if len(out) > keep {
			out = out[len(out)-keep:]
		}
		return out
	}
	n := len(s.received) + len(s.sent)
	s.received, s.sent = live(s.received), live(s.sent)
	return len(s.received)+len(s.sent) != n
}

// Send queues a clip for its recipients and starts sending it.
func (s *Store) Send(req Request) (Clip, error) {
	if len(bytes.TrimSpace([]byte(req.Text))) == 0 {
		return Clip{}, ErrEmpty
	}
	if len(req.Text) > MaxText {
		return Clip{}, ErrTooLong
	}
	meshes, peers := s.dir()
	mesh, names, err := resolve(req, meshes, peers)
	if err != nil {
		return Clip{}, err
	}
	now := s.now()
	c := Clip{ID: newID(), Text: req.Text, Secret: req.Secret, Mesh: mesh,
		Created: now, Expires: now.Add(ttlFor(req))}
	for _, n := range names {
		c.To = append(c.To, Target{Name: n})
	}
	s.mu.Lock()
	s.sent = append(s.sent, c)
	s.pruneLocked()
	s.saveLocked()
	s.mu.Unlock()
	s.Kick()
	return c, nil
}

// Online is a recipient coming online: whatever waits for it goes now, past
// any backoff from while it was away.
func (s *Store) Online(mesh, name string) {
	s.mu.Lock()
	delete(s.retry, mesh+"/"+name)
	s.mu.Unlock()
	s.Kick()
}

// Kick has the outbox try now: a clip was sent, or a peer came online.
func (s *Store) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// List is every clip here, newest first, received and sent.
func (s *Store) List() (received, sent []Clip) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pruneLocked() {
		s.saveLocked()
	}
	received, sent = slices.Clone(s.received), slices.Clone(s.sent)
	slices.Reverse(received)
	slices.Reverse(sent)
	return received, sent
}

// Take is a received clip being copied. A secret goes soon after.
func (s *Store) Take(id string) (Clip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := index(s.received, id)
	if i < 0 {
		return Clip{}, ErrNotFound
	}
	{
		c := &s.received[i]
		now := s.now()
		if c.Taken.IsZero() {
			c.Taken = now
		}
		if c.Secret && c.Expires.After(now.Add(takenSecret)) {
			c.Expires = now.Add(takenSecret)
		}
		s.saveLocked()
		return *c, nil
	}
}

// index finds a clip by its ID, or by the start of it as `shrooms clipboard list`
// shows it, when only one starts so.
func index(cs []Clip, id string) int {
	found := -1
	for i, c := range cs {
		if c.ID == id {
			return i
		}
		if len(id) >= 6 && strings.HasPrefix(c.ID, id) {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

// Latest is the newest clip received, for `shrooms clipboard paste` with no ID.
func (s *Store) Latest() (Clip, error) {
	r, _ := s.List()
	if len(r) == 0 {
		return Clip{}, ErrNotFound
	}
	return s.Take(r[0].ID)
}

// Delete forgets a clip, sent or received. A sent one still waiting is no
// longer sent.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := func(cs []Clip) ([]Clip, bool) {
		i := index(cs, id)
		if i < 0 {
			return cs, false
		}
		return slices.Delete(cs, i, i+1), true
	}
	var a, b bool
	s.received, a = drop(s.received)
	s.sent, b = drop(s.sent)
	if !a && !b {
		return ErrNotFound
	}
	s.saveLocked()
	return nil
}

// Handler takes clips arriving on one mesh's address.
func (s *Store) Handler(mesh string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/clip", func(w http.ResponseWriter, r *http.Request) {
		from, ok := s.sender(mesh, r.RemoteAddr)
		if !ok {
			http.Error(w, "not a member of this mesh", http.StatusForbidden)
			return
		}
		var m wire
		if err := json.NewDecoder(io.LimitReader(r.Body, MaxText*2)).Decode(&m); err != nil || m.ID == "" {
			http.Error(w, "bad clip", http.StatusBadRequest)
			return
		}
		if len(m.Text) > MaxText {
			http.Error(w, ErrTooLong.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		s.receive(mesh, from, m)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// sender is who sent a request, by the address it came from.
func (s *Store) sender(mesh, remote string) (string, bool) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return "", false
	}
	_, peers := s.dir()
	for _, p := range peers {
		if p.Mesh == mesh && p.Addr == ap.Addr().Unmap() {
			return p.Name, true
		}
	}
	return "", false
}

func (s *Store) receive(mesh, from string, m wire) {
	now := s.now()
	exp := time.Unix(m.Expires, 0)
	// The sender says how long; this side says how long at most.
	if exp.After(now.Add(MaxTTL)) {
		exp = now.Add(MaxTTL)
	}
	if !exp.After(now) {
		return
	}
	c := Clip{ID: m.ID, Text: m.Text, Secret: m.Secret, Mesh: mesh, From: from,
		Created: time.Unix(m.Created, 0), Expires: exp}
	s.mu.Lock()
	// A retry after a lost answer is the same clip, not a second one.
	if slices.ContainsFunc(s.received, func(x Clip) bool { return x.ID == c.ID && x.From == from }) {
		s.mu.Unlock()
		return
	}
	s.received = append(s.received, c)
	s.pruneLocked()
	s.saveLocked()
	s.mu.Unlock()
	s.log.Info("clip arrived", "from", from, "mesh", mesh, "secret", c.Secret)
	if s.Arrived != nil {
		s.Arrived(c)
	}
}

// Run sends what waits, until ctx ends: at once when kicked, and every
// interval for recipients that were away.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-t.C:
		}
	}
}

func (s *Store) httpClient() *http.Client {
	if s.client == nil {
		tr := &http.Transport{DialContext: s.Dial, DisableKeepAlives: true}
		s.client = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	}
	return s.client
}

// flush tries every recipient still waiting that is online, in parallel.
func (s *Store) flush(ctx context.Context) {
	type job struct {
		id   string
		name string
		addr netip.Addr
		body []byte
	}
	var jobs []job
	s.mu.Lock()
	if s.pruneLocked() {
		s.saveLocked()
	}
	// Nothing waiting is the usual case, and asking who is online is not free:
	// on the phone it is a status snapshot, every 15 seconds.
	waiting := slices.ContainsFunc(s.sent, func(c Clip) bool { return !c.Delivered() })
	if !waiting {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_, peers := s.dir()
	s.mu.Lock()
	for _, c := range s.sent {
		for _, t := range c.To {
			if !t.Sent.IsZero() {
				continue
			}
			p, ok := find(peers, c.Mesh, t.Name)
			if !ok || !p.Online {
				continue
			}
			if b, ok := s.retry[c.Mesh+"/"+t.Name]; ok && s.now().Before(b.next) {
				continue
			}
			body, _ := json.Marshal(wire{ID: c.ID, Text: c.Text, Secret: c.Secret,
				Created: c.Created.Unix(), Expires: c.Expires.Unix()})
			jobs = append(jobs, job{c.ID, t.Name, p.Addr, body})
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.post(ctx, j.addr, j.body)
			s.mark(j.id, j.name, err)
		}()
	}
	wg.Wait()
}

func (s *Store) post(ctx context.Context, addr netip.Addr, body []byte) error {
	url := "http://" + net.JoinHostPort(addr.String(), strconv.Itoa(Port)) + "/v1/clip"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

func (s *Store) mark(id, name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retry == nil {
		s.retry = map[string]backoff{}
	}
	for _, c := range s.sent {
		if c.ID == id {
			key := c.Mesh + "/" + name
			if err == nil {
				delete(s.retry, key)
			} else {
				s.retry[key] = s.retry[key].after(s.now())
			}
			break
		}
	}
	for i := range s.sent {
		if s.sent[i].ID != id {
			continue
		}
		for j := range s.sent[i].To {
			t := &s.sent[i].To[j]
			if t.Name != name {
				continue
			}
			if err != nil {
				t.Error = describe(err)
			} else {
				t.Sent, t.Error = s.now(), ""
				s.log.Info("clip delivered", "to", name)
			}
		}
		// A secret every recipient has is not kept here as well.
		if s.sent[i].Secret && s.sent[i].Delivered() {
			s.sent[i].Text = ""
		}
	}
	s.saveLocked()
}

// backoff spaces out tries at a recipient that keeps failing: one on an older
// shrooms refuses every 15 seconds for as long as the clip lives, otherwise.
type backoff struct {
	next time.Time
	wait time.Duration
}

func (b backoff) after(now time.Time) backoff {
	switch {
	case b.wait == 0:
		b.wait = 30 * time.Second
	case b.wait < 5*time.Minute:
		b.wait *= 2
	}
	if b.wait > 5*time.Minute {
		b.wait = 5 * time.Minute
	}
	b.next = now.Add(b.wait)
	return b
}

// describe says why a recipient does not have a clip yet, in words for the
// person who sent it.
func describe(err error) string {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "no clipboard there yet: it runs an older shrooms"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "did not answer"
	}
	return err.Error()
}
