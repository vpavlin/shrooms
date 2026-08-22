package invite

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The joining half of the exchange, as protocol rather than as a command.
//
// Two things run it: `shrooms join --invite` on a machine with no daemon, and
// the daemon itself when it is waiting to be told which mesh it belongs to.
// Written against a small transport interface so that both share the protocol
// rather than a description of it, and so it can be tested without a rendezvous
// node — which is otherwise the only way to reach any of this.

// Message is one thing that arrived on a topic.
type Message struct {
	Topic   string
	Payload []byte
}

// Transport is the part of a rendezvous node the exchange needs.
//
// Deliberately not waku.Node: that type is bound to cgo, and an interface this
// small keeps the protocol testable in a package that compiles without the
// library.
type Transport interface {
	Subscribe(contentTopic string) error
	Unsubscribe(contentTopic string) error
	Send(contentTopic string, payload []byte, ephemeral bool) (string, error)
	Messages() <-chan Message
}

// RetryEvery is how often the request is repeated while waiting.
//
// The joining device is usually the one on the worse network, and the first
// publish often lands before its subscription has propagated. Repeating is
// safe: the inviter answers at most once however many requests it sees.
const RetryEvery = 5 * time.Second

// Redeem performs the joining side: publish a request, wait for the response
// meant for this device.
//
// The caller owns the transport and its lifetime. On return the invite topic is
// unsubscribed, whether or not it succeeded.
// RedeemForMesh runs the two-round exchange (ADR-017): ask which mesh this is,
// derive the identity this device will use *there*, and ask again for a
// credential naming it.
//
// The second round exists because of an ordering problem that has no other
// answer. A device cannot know which mesh it is joining until the response
// arrives, so it cannot derive a per-mesh identity (ADR-015) before it asks —
// and if it sends its base identity, the credential names that, and a device on
// two invited meshes shows the same key to both.
//
// derive is given the first response and returns the request to send second.
// It is a callback so that this package stays ignorant of how an identity is
// derived and of where state lives.
//
// Falls back to one round in the two cases where a second buys nothing: a
// holder that predates this and issued a credential immediately, and a mesh
// with no admin keys, where there is no credential to issue.
func RedeemForMesh(ctx context.Context, t Transport, s Secret, first *Request,
	derive func(*Response) (*Request, error)) (*Response, error) {

	if first == nil {
		return nil, errors.New("no request to send")
	}
	first.Deferred = true
	resp, err := Redeem(ctx, t, s, first)
	if err != nil {
		return nil, err
	}
	// Round two runs even when the mesh has no admin keys and so has no
	// credential to issue. It is the round that consumes the invite, and
	// stopping here left the token live for the rest of its window - on a
	// --no-admin mesh, where the network key alone is membership, that turned
	// "one device, once" into "anyone holding the token, until it expires".
	// The inviter was told nobody had used it.
	if len(resp.Credential) > 0 {
		return resp, nil
	}

	second, err := derive(resp)
	if err != nil {
		return nil, err
	}
	second.Deferred = false
	out, err := Redeem(ctx, t, s, second)
	if err != nil {
		// The mesh is known and usable at this point; only the credential is
		// missing, and on a mesh with admin keys that means no peer will admit
		// us. Say which half failed, because "the invite did not work" sends
		// people back to the token, which was fine.
		return nil, fmt.Errorf("the mesh answered but did not issue a credential: %w", err)
	}
	return out, nil
}

func Redeem(ctx context.Context, t Transport, s Secret, req *Request) (*Response, error) {
	if req == nil {
		return nil, errors.New("no request to send")
	}
	priv, pub, err := NewEphemeral()
	if err != nil {
		return nil, err
	}
	req.EphPub = pub
	req.Timestamp = time.Now().Unix()

	blob, err := SealRequest(s, req)
	if err != nil {
		return nil, err
	}

	name := s.Topic()
	if err := t.Subscribe(name); err != nil {
		return nil, fmt.Errorf("subscribe to the invite topic: %w", err)
	}
	defer t.Unsubscribe(name)

	send := func() error {
		if _, err := t.Send(name, blob, true); err != nil {
			return fmt.Errorf("send the request: %w", err)
		}
		return nil
	}
	if err := send(); err != nil {
		return nil, err
	}

	retry := time.NewTicker(RetryEvery)
	defer retry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-retry.C:
			if err := send(); err != nil {
				return nil, err
			}
		case msg, ok := <-t.Messages():
			if !ok {
				return nil, errors.New("the rendezvous node stopped")
			}
			if msg.Topic != name {
				continue
			}
			// Our own request comes back to us, as does anything a second
			// holder of the token publishes. Neither opens as a response for
			// this device, and neither is worth reporting.
			resp, err := OpenResponse(s, priv, msg.Payload, time.Now())
			if err != nil {
				continue
			}
			return resp, nil
		}
	}
}
