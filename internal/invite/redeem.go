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
