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

// holdTwoRounds plays the inviting side of the two-round exchange: answer a
// deferred request with the mesh and no credential, then answer whatever comes
// next with a credential naming the keys that request carried.
//
// It records those keys, because the whole point of the second round is which
// keys the credential ends up naming.
func holdTwoRounds(t *testing.T, s Secret, bus *fakeBus, admin []byte) *[]byte {
	t.Helper()
	got := new([]byte)
	go func() {
		seen := 0
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
			resp := &Response{
				NetworkKey: bytes.Repeat([]byte{9}, 32),
				AdminKeys:  [][]byte{admin},
				Suffix:     "mesh",
				Timestamp:  time.Now().Unix(),
			}
			if req.Deferred {
				if seen > 0 {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				seen++
			} else {
				// The second round: this is the request worth admitting.
				*got = append([]byte(nil), req.DevicePub...)
				resp.Credential = bytes.Repeat([]byte{5}, 300)
			}
			sealed, err := SealResponse(s, req.EphPub, resp)
			if err != nil {
				t.Error(err)
				return
			}
			bus.deliver(Message{Topic: s.Topic(), Payload: sealed})
			if resp.Credential != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return got
}

// The credential must name the keys derived for the mesh that answered, not the
// base identity the first round carried. That difference is the entire reason
// the second round exists (ADR-017).
func TestRedeemForMeshUsesTheDerivedIdentity(t *testing.T) {
	s, _ := New()
	bus := newBus()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := bytes.Repeat([]byte{1}, 32)
	derived := bytes.Repeat([]byte{2}, 32)
	admitted := holdTwoRounds(t, s, bus, bytes.Repeat([]byte{7}, 32))

	var sawMesh bool
	resp, err := RedeemForMesh(ctx, bus, s,
		&Request{DevicePub: base, WGPub: base, Name: "phone"},
		func(r *Response) (*Request, error) {
			// The mesh is known by now, which is what makes deriving possible.
			sawMesh = len(r.NetworkKey) == 32
			return &Request{DevicePub: derived, WGPub: derived, Name: "phone"}, nil
		})
	if err != nil {
		t.Fatalf("RedeemForMesh: %v", err)
	}
	if !sawMesh {
		t.Error("the second round was asked for without a network key to derive from")
	}
	if len(resp.Credential) == 0 {
		t.Error("no credential came back")
	}
	if !bytes.Equal(*admitted, derived) {
		t.Errorf("the credential was issued for %x, wanted the derived identity %x",
			*admitted, derived)
	}
}

// A holder that predates the second round issues a credential straight away.
// The joiner must take it rather than wait for a round that will never come.
func TestRedeemForMeshFallsBackToOneRound(t *testing.T) {
	s, _ := New()
	bus := newBus()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
			// Ignores Deferred entirely, as an older holder does.
			sealed, _ := SealResponse(s, req.EphPub, &Response{
				NetworkKey: bytes.Repeat([]byte{9}, 32),
				AdminKeys:  [][]byte{bytes.Repeat([]byte{7}, 32)},
				Credential: bytes.Repeat([]byte{5}, 300),
				Timestamp:  time.Now().Unix(),
			})
			bus.deliver(Message{Topic: s.Topic(), Payload: sealed})
			return
		}
	}()

	called := false
	resp, err := RedeemForMesh(ctx, bus, s,
		&Request{DevicePub: bytes.Repeat([]byte{1}, 32), WGPub: bytes.Repeat([]byte{1}, 32)},
		func(*Response) (*Request, error) { called = true; return nil, nil })
	if err != nil {
		t.Fatalf("RedeemForMesh: %v", err)
	}
	if called {
		t.Error("asked for a second round when the first already carried a credential")
	}
	if len(resp.Credential) == 0 {
		t.Error("dropped the credential the old holder sent")
	}
}
