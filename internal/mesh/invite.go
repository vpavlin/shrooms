package mesh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/invite"
)

// Holding an invite open, on the node that is already a member.
//
// The alternative was for `shrooms invite` to start a Logos Delivery node of
// its own, which worked and was unpleasant: three seconds of dialling, a page
// of library logging that cannot be turned off, and a second Core node joining
// the fleet for the sake of two messages. The daemon is already connected.
//
// The split is the same one the admin key exists for. The daemon holds the
// network key and the connection and never sees the admin key; the CLI holds
// the admin key and signs, and never sees the network key. Neither half can
// admit a device alone.

// invites are the invite topics this node is currently listening on, by content
// topic. Normally empty, and at most one entry outside a test.
type invites struct {
	mu sync.Mutex
	by map[string]*heldInvite
}

type heldInvite struct {
	secret invite.Secret
	reqs   chan *invite.Request
}

// Hold subscribes to an invite's topic and returns the first request that opens
// under the token.
//
// One request, then done: an invite admits one device, and the decision is
// local to this node because nobody else is listening on that topic. Returns
// ctx.Err() if the invite expires first.
func (m *Mesh) HoldInvite(ctx context.Context, s invite.Secret) (*invite.Request, error) {
	name := s.Topic()

	held := &heldInvite{secret: s, reqs: make(chan *invite.Request, 1)}
	m.inv.mu.Lock()
	if m.inv.by == nil {
		m.inv.by = make(map[string]*heldInvite)
	}
	if _, busy := m.inv.by[name]; busy {
		m.inv.mu.Unlock()
		return nil, errors.New("that invite is already open")
	}
	m.inv.by[name] = held
	m.inv.mu.Unlock()

	defer func() {
		m.inv.mu.Lock()
		delete(m.inv.by, name)
		m.inv.mu.Unlock()
		// Unsubscribing is best-effort: a stale subscription costs a little
		// traffic on one topic, where failing here would abort a completed
		// enrolment.
		if err := m.node.Unsubscribe(name); err != nil {
			m.log.Debug("could not unsubscribe from an invite topic", "err", err)
		}
	}()

	if err := m.node.Subscribe(name); err != nil {
		return nil, fmt.Errorf("subscribe to the invite topic: %w", err)
	}
	// The fleet is logged on both ends deliberately: if they differ, both sides
	// work perfectly and never meet, and this is the only place it shows.
	m.log.Info("holding an invite open", "preset", m.cfg.Preset, "cluster", m.cfg.ClusterID)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case req := <-held.reqs:
		return req, nil
	}
}

// answerDeferred replies to a first-round request with the mesh and no
// credential, so the joiner can derive the identity it will use here and ask
// again for a credential naming it (ADR-017).
//
// Answered by the daemon rather than surfaced to the CLI, because nothing in
// it needs the admin key: it is the network key, the admin *public* keys and
// the suffix, all of which this node already holds. The invite stays open for
// the second request, which is the one worth a person looking at.
func (m *Mesh) answerDeferred(s invite.Secret, req *invite.Request) {
	if err := m.ReplyInvite(s, req, nil); err != nil {
		m.log.Warn("could not answer the first round of an invite", "err", err)
		return
	}
	m.log.Info("told a joining device which mesh this is; waiting for its per-mesh keys",
		"name", req.Name)
}

// ReplyInvite seals a response under the token and publishes it.
//
// The mesh's own values — network key, admin keys, name suffix — are filled in
// here rather than by the caller, so the CLI never handles the network key and
// nothing but a well-formed invite response can be published on this topic.
func (m *Mesh) ReplyInvite(s invite.Secret, req *invite.Request, credential []byte) error {
	if req == nil {
		return errors.New("no request to reply to")
	}
	resp := &invite.Response{
		NetworkKey: m.nk[:],
		Credential: credential,
		Suffix:     m.cfg.HostsSuffix,
		Timestamp:  time.Now().Unix(),
	}
	if m.authority != nil {
		for _, k := range m.authority.Keys {
			resp.AdminKeys = append(resp.AdminKeys, append([]byte(nil), k...))
		}
	}
	blob, err := invite.SealResponse(s, req.EphPub, resp)
	if err != nil {
		return err
	}
	if _, err := m.node.Send(s.Topic(), blob, true); err != nil {
		return fmt.Errorf("publish the invite response: %w", err)
	}
	m.log.Info("admitted a device", "name", req.Name, "credential", len(credential) > 0)
	return nil
}

// handleInvite offers a message to any invite being held. Reports whether it
// belonged to one, so the ordinary control-plane path can skip it.
func (m *Mesh) handleInvite(contentTopic string, payload []byte, now time.Time) bool {
	m.inv.mu.Lock()
	held, ok := m.inv.by[contentTopic]
	m.inv.mu.Unlock()
	if !ok {
		return false
	}

	// The token is what opens it. Our own response comes back to us on the same
	// topic and does not open as a request, which is the only filtering needed.
	req, err := invite.OpenRequest(held.secret, payload, now)
	if err != nil {
		return true // ours by topic, not readable as a request: drop it quietly
	}
	// A first-round request is answered here and does not consume the invite:
	// it asks which mesh this is, and the answer holds no secret the token does
	// not already imply. The device comes back with keys derived for this mesh,
	// and that request is the one that admits it.
	//
	// A device that asks twice gets told twice, which costs two sealed messages
	// on a topic only it and this node are listening to.
	if req.Deferred {
		go m.answerDeferred(held.secret, req)
		return true
	}

	select {
	case held.reqs <- req:
	default: // already answered; an invite admits one device
	}
	return true
}
