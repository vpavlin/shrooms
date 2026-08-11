// Package rendezvous adapts a Logos Delivery node to the interfaces the
// protocol packages define.
//
// It exists so that internal/invite can stay free of cgo — which is what makes
// the enrolment protocol testable without a rendezvous node — while the CLI,
// the daemon and the Android binding all drive it through the same adapter
// rather than three copies of one.
package rendezvous

import (
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/waku"
)

// InviteTransport wraps a node for the joining half of an enrolment.
//
// The node's event channel has a single consumer, so this takes it over: use it
// on a node dedicated to the exchange, not on one a mesh is running.
func InviteTransport(node *waku.Node) invite.Transport {
	return &transport{node: node}
}

type transport struct {
	node *waku.Node
	msgs chan invite.Message
}

func (t *transport) Subscribe(topic string) error   { return t.node.Subscribe(topic) }
func (t *transport) Unsubscribe(topic string) error { return t.node.Unsubscribe(topic) }

func (t *transport) Send(topic string, payload []byte, ephemeral bool) (string, error) {
	return t.node.Send(topic, payload, ephemeral)
}

func (t *transport) Messages() <-chan invite.Message {
	if t.msgs == nil {
		t.msgs = make(chan invite.Message, 16)
		go func() {
			defer close(t.msgs)
			for ev := range t.node.Events() {
				msg, _, ok := waku.ParseMessage(ev.JSON)
				if !ok {
					continue
				}
				select {
				case t.msgs <- invite.Message{Topic: msg.ContentTopic, Payload: msg.Payload}:
				default:
					// Dropped rather than blocked: the shard carries other
					// applications' traffic, and an exchange that stalled
					// behind it would look like nobody answering.
				}
			}
		}()
	}
	return t.msgs
}
