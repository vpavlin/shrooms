package invite

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// fakeBus is a rendezvous plane with nobody else on it: whatever is sent comes
// back, the way a real one echoes a publisher's own message.
type fakeBus struct {
	mu     sync.Mutex
	msgs   chan Message
	sent   [][]byte
	subbed map[string]bool
	unsub  map[string]bool
}

func newBus() *fakeBus {
	return &fakeBus{
		msgs:   make(chan Message, 16),
		subbed: map[string]bool{},
		unsub:  map[string]bool{},
	}
}

func (b *fakeBus) Subscribe(topic string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subbed[topic] = true
	return nil
}

func (b *fakeBus) Unsubscribe(topic string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unsub[topic] = true
	return nil
}

func (b *fakeBus) Send(topic string, payload []byte, _ bool) (string, error) {
	b.mu.Lock()
	b.sent = append(b.sent, append([]byte(nil), payload...))
	b.mu.Unlock()
	// Echoed, because a real bus does: the sender is subscribed to the topic it
	// published on, and Redeem has to not mistake its own request for an answer.
	b.deliver(Message{Topic: topic, Payload: payload})
	return "hash", nil
}

func (b *fakeBus) deliver(m Message) {
	select {
	case b.msgs <- m:
	default:
	}
}

func (b *fakeBus) Messages() <-chan Message { return b.msgs }

func (b *fakeBus) lastSent() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) == 0 {
		return nil
	}
	return b.sent[len(b.sent)-1]
}

// The whole joining side, without a rendezvous node: publish a request, ignore
// the echo of it, open the response that was sealed to this device.
func TestRedeem(t *testing.T) {
	s, _ := New()
	bus := newBus()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The inviting side, once the request appears.
	go func() {
		deadline := time.After(4 * time.Second)
		for {
			select {
			case <-deadline:
				return
			default:
			}
			raw := bus.lastSent()
			if raw == nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			req, err := OpenRequest(s, raw, time.Now())
			if err != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			resp, err := SealResponse(s, req.EphPub, &Response{
				NetworkKey: bytes.Repeat([]byte{9}, 32),
				Credential: bytes.Repeat([]byte{5}, 300),
				Suffix:     "mesh",
				Timestamp:  time.Now().Unix(),
			})
			if err != nil {
				t.Error(err)
				return
			}
			bus.deliver(Message{Topic: s.Topic(), Payload: resp})
			return
		}
	}()

	got, err := Redeem(ctx, bus, s, &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		Name:      "phone",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.NetworkKey, bytes.Repeat([]byte{9}, 32)) {
		t.Error("wrong network key")
	}
	if len(got.Credential) != 300 || got.Suffix != "mesh" {
		t.Errorf("response came back wrong: %d bytes of credential, suffix %q",
			len(got.Credential), got.Suffix)
	}
	if !bus.unsub[s.Topic()] {
		t.Error("left the invite topic subscribed")
	}
}

// Nobody listening: the join has to give up rather than hang, and must not
// mistake its own echoed request for an answer.
func TestRedeemGivesUp(t *testing.T) {
	s, _ := New()
	bus := newBus()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := Redeem(ctx, bus, s, &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		Name:      "phone",
	})
	if err == nil {
		t.Fatal("redeemed an invite nobody answered")
	}
	if !bus.unsub[s.Topic()] {
		t.Error("left the invite topic subscribed after giving up")
	}
}

// A response sealed to somebody else's request must not be accepted, or a
// second holder of the token could hand this device a mesh of their choosing.
func TestRedeemIgnoresAResponseForAnotherDevice(t *testing.T) {
	s, _ := New()
	bus := newBus()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, otherPub, _ := NewEphemeral()
	blob, err := SealResponse(s, otherPub, &Response{
		NetworkKey: bytes.Repeat([]byte{9}, 32),
		Timestamp:  time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.deliver(Message{Topic: s.Topic(), Payload: blob})
	}()

	if _, err := Redeem(ctx, bus, s, &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		Name:      "phone",
	}); err == nil {
		t.Fatal("accepted a response sealed to another device")
	}
}
