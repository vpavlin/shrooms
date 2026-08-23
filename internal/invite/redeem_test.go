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
	if len(got.Credential) != 300 {
		t.Errorf("response came back wrong: %d bytes of credential",
			len(got.Credential))
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

// holdRounds plays the inviting side and records every request it saw, so a
// test can assert which rounds actually happened. adminKeys may be empty, which
// is a --no-admin mesh: there is no credential to issue, and the second round
// exists purely to consume the invite.
//
// Unlike a real node it never stops answering deferred requests, because the
// point here is what the joining client does, not what the holder allows.
func holdRounds(t *testing.T, s Secret, bus *fakeBus, adminKeys [][]byte, meshOnlyFirst bool) *[]bool {
	t.Helper()
	rounds := new([]bool) // one entry per request seen: true if deferred
	go func() {
		deadline := time.After(4 * time.Second)
		// Answer each request once. The bus drops sends when its buffer is
		// full, so a holder that re-answers whatever lastSent still points at
		// starves the round it is waiting for.
		answered := map[string]bool{}
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
			if k := string(req.EphPub); answered[k] {
				time.Sleep(5 * time.Millisecond)
				continue
			} else {
				answered[k] = true
			}
			*rounds = append(*rounds, req.Deferred)
			resp := &Response{
				MeshID:    "testmeshid",
				AdminKeys: adminKeys,
				Timestamp: time.Now().Unix(),
			}
			// The first round must be answerable without the network key; an
			// older node sent it anyway, and both must work.
			if !req.Deferred || !meshOnlyFirst {
				resp.NetworkKey = bytes.Repeat([]byte{9}, 32)
			}
			if !req.Deferred && len(adminKeys) > 0 {
				resp.Credential = bytes.Repeat([]byte{5}, 300)
			}
			sealed, err := SealResponse(s, req.EphPub, resp)
			if err != nil {
				t.Error(err)
				return
			}
			bus.deliver(Message{Topic: s.Topic(), Payload: sealed})
			if !req.Deferred {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return rounds
}

func redeemBoth(t *testing.T, s Secret, bus *fakeBus) (*Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return RedeemForMesh(ctx, bus, s, &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		Name:      "phone",
	}, func(r *Response) (*Request, error) {
		return &Request{
			DevicePub: bytes.Repeat([]byte{3}, 32),
			WGPub:     bytes.Repeat([]byte{4}, 32),
			Name:      "phone",
		}, nil
	})
}

// A mesh with no admin keys has no credential to issue, and the exchange used
// to stop after the first round because of that. The first round deliberately
// does not consume the invite, so stopping there left the token live for the
// rest of its window — and on such a mesh the network key alone is membership,
// so one token admitted every device that presented it. The second round has to
// happen regardless of whether there is a credential waiting at the end of it.
func TestANoAdminMeshStillRunsTheRoundThatConsumesTheInvite(t *testing.T) {
	s, _ := New()
	bus := newBus()
	rounds := holdRounds(t, s, bus, nil, true)

	if _, err := redeemBoth(t, s, bus); err != nil {
		t.Fatalf("join a --no-admin mesh: %v", err)
	}
	if len(*rounds) < 2 {
		t.Fatalf("saw %d request(s); the invite was never consumed", len(*rounds))
	}
	if (*rounds)[len(*rounds)-1] {
		t.Error("the last request was still deferred; no round consumed the invite")
	}
}

// The first round is answered as often as it is asked and does not consume the
// invite, so it must not carry the mesh's secret. A joining device needs only
// the mesh id from it — enough to derive the identity it will use — and gets
// the network key in the second round, which is the one that admits it.
func TestTheFirstRoundNeedsNoNetworkKey(t *testing.T) {
	s, _ := New()
	bus := newBus()
	admin := bytes.Repeat([]byte{7}, 32)
	holdRounds(t, s, bus, [][]byte{admin}, true)

	resp, err := redeemBoth(t, s, bus)
	if err != nil {
		t.Fatalf("a first round with no network key must still work: %v", err)
	}
	if len(resp.Credential) == 0 {
		t.Error("the second round issued no credential")
	}
}

// A node running an older build answers the first round with the network key
// and no mesh id. A joining device must still be able to talk to it.
func TestAnOlderNodeSendingTheKeyFirstStillWorks(t *testing.T) {
	s, _ := New()
	bus := newBus()
	admin := bytes.Repeat([]byte{7}, 32)
	holdRounds(t, s, bus, [][]byte{admin}, false)

	if _, err := redeemBoth(t, s, bus); err != nil {
		t.Fatalf("older holder: %v", err)
	}
}

// A joining device's sealing key has to reach the issuer, or the credential it
// gets back cannot be version 2 and nothing can ever be addressed to it.
func TestTheRequestCarriesTheSealingKey(t *testing.T) {
	s, _ := New()
	bus := newBus()
	seal := bytes.Repeat([]byte{6}, 32)

	var got []byte
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
			got = append([]byte(nil), req.SealPub...)
			sealed, _ := SealResponse(s, req.EphPub, &Response{
				MeshID: "m", NetworkKey: bytes.Repeat([]byte{9}, 32),
				Credential: bytes.Repeat([]byte{5}, 300), Timestamp: time.Now().Unix(),
			})
			bus.deliver(Message{Topic: s.Topic(), Payload: sealed})
			return
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := Redeem(ctx, bus, s, &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		SealPub:   seal,
		Name:      "phone",
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, seal) {
		t.Errorf("the sealing key did not reach the issuer: got %x", got)
	}
}
