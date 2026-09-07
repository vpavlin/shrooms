package rendezvous

import (
	"fmt"
	"testing"

	"github.com/vpavlin/shrooms/internal/waku"
)

// The Fed transport, which exists because the daemon had two readers on one
// channel and enrolment was therefore a coin toss. See Fed's own comment.
//
// A nil node throughout: Deliver, Close and the buffer are the whole of what
// the daemon depends on here, and none of them touch the node. Subscribe and
// Send do, and they are one-line pass-throughs to a library that cannot run in
// a unit test.

func event(topic, payload string) waku.Event {
	ints := ""
	for i, b := range []byte(payload) {
		if i > 0 {
			ints += ","
		}
		ints += fmt.Sprint(int(b))
	}
	return waku.Event{JSON: fmt.Sprintf(
		`{"eventType":"message_received","message":{"contentTopic":%q,"payload":[%s]}}`,
		topic, ints)}
}

func TestFedDeliversWhatItIsGiven(t *testing.T) {
	tr := Fed(nil)
	tr.Deliver(event("/invite/1", "hello"))

	select {
	case msg := <-tr.Messages():
		if msg.Topic != "/invite/1" || string(msg.Payload) != "hello" {
			t.Fatalf("got %q on %q", msg.Payload, msg.Topic)
		}
	default:
		t.Fatal("nothing arrived")
	}
}

// Fed is handed EVERY event on the node, including the fleet's own traffic and
// the connection-state changes that are not messages at all. Dropping those
// quietly is the point: the alternative is the daemon's fan-out having to know
// what an invite looks like.
func TestFedIgnoresWhatIsNotAMessage(t *testing.T) {
	tr := Fed(nil)
	tr.Deliver(waku.Event{JSON: `{"eventType":"connection_change"}`})
	tr.Deliver(waku.Event{JSON: `not json at all`})

	select {
	case msg := <-tr.Messages():
		t.Fatalf("a non-message arrived: %+v", msg)
	default:
	}
	if tr.Dropped() != 0 {
		t.Errorf("Dropped = %d; events that are not messages are not drops", tr.Dropped())
	}
}

// A full buffer must not block. Fed is called from the daemon's single reader,
// so blocking here stalls every mesh in the process — which is a worse failure
// than losing an invite message, and a much harder one to see.
func TestFedDropsRatherThanBlocks(t *testing.T) {
	tr := Fed(nil)
	for i := 0; i < buffered+10; i++ {
		tr.Deliver(event("/invite/1", "x"))
	}
	if tr.Dropped() != 10 {
		t.Errorf("Dropped = %d, want 10", tr.Dropped())
	}
}

// Close tells an exchange the events have stopped. Without it, a join running
// when the daemon shuts down sits until its own deadline instead of saying so:
// invite.Redeem reads the closed channel as "the rendezvous node stopped".
func TestFedCloseStopsAnExchange(t *testing.T) {
	tr := Fed(nil)
	tr.Close()
	if _, ok := <-tr.Messages(); ok {
		t.Fatal("Messages() still open after Close")
	}
	// Twice, because the daemon's fan-out defers it and a caller may also do
	// it: closing a closed channel panics.
	tr.Close()
}
