// Package rendezvous adapts a Logos Delivery node to the interfaces the
// protocol packages define.
//
// It exists so that internal/invite can stay free of cgo — which is what makes
// the enrolment protocol testable without a rendezvous node — while the CLI,
// the daemon and the Android binding all drive it through the same adapter
// rather than three copies of one.
package rendezvous

import (
	"sync/atomic"

	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/waku"
)

// InviteTransport wraps a node for the joining half of an enrolment.
//
// The node's event channel has a single consumer, so this takes it over: use it
// on a node dedicated to the exchange, not on one a mesh is running.
//
// **Start it as soon as the node exists, not when the exchange begins.** The
// node's channel is bounded and its C callback drops events when it is full,
// so a node nobody is reading fills with the fleet's traffic within seconds and
// then discards everything that arrives — including the response the exchange
// is waiting for. That cost a working invite exchange once already.
func InviteTransport(node *waku.Node) invite.Transport {
	t := &transport{node: node}
	t.start()
	return t
}

type transport struct {
	node    *waku.Node
	msgs    chan invite.Message
	dropped atomic.Uint64
}

// buffered generously. The exchange reads promptly, but a Core node carries
// every shard in the cluster — measured at 745 messages where 693 belonged to
// another application — and a response dropped for want of a slot looks exactly
// like nobody answering.
const buffered = 512

// Dropped reports messages discarded because the reader was behind.
func (t *transport) Dropped() uint64 { return t.dropped.Load() }

func (t *transport) Subscribe(topic string) error   { return t.node.Subscribe(topic) }
func (t *transport) Unsubscribe(topic string) error { return t.node.Unsubscribe(topic) }

func (t *transport) Send(topic string, payload []byte, ephemeral bool) (string, error) {
	return t.node.Send(topic, payload, ephemeral)
}

func (t *transport) start() {
	t.msgs = make(chan invite.Message, buffered)
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
				// Dropped rather than blocked: blocking here would stall the
				// node's own reader, which puts the drop one level down where
				// it is invisible. Counted so it is not.
				t.dropped.Add(1)
			}
		}
	}()
}

func (t *transport) Messages() <-chan invite.Message { return t.msgs }
