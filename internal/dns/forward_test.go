package dns

import (
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Android has no split-DNS: the VpnService resolver receives every query the
// device makes. Refusing what is not ours removes the phone's name resolution
// entirely — no browsing, no app updates — which is exactly what happened.
func TestForwardsNamesOutsideTheSuffix(t *testing.T) {
	forwarded := 0
	s := testServer()
	s.Upstream = func(q []byte) ([]byte, error) {
		forwarded++
		// Echo a NOERROR response so the caller sees a real answer.
		var p dnsmessage.Parser
		h, _ := p.Start(q)
		question, _ := p.Question()
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeSuccess})
		b.StartQuestions()
		b.Question(question)
		b.StartAnswers()
		b.AAAAResource(
			dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeAAAA, Class: question.Class, TTL: 60},
			dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("2001:db8::1").As16()},
		)
		return b.Finish()
	}

	reply, err := s.answer(query(t, "google.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if forwarded != 1 {
		t.Fatalf("forwarded %d queries, want 1", forwarded)
	}
	h, answers := parse(t, reply)
	if h.RCode != dnsmessage.RCodeSuccess || len(answers) != 1 {
		t.Errorf("rcode=%v answers=%d — the client would see this as a failure", h.RCode, len(answers))
	}
}

// Mesh names must never be forwarded: they exist nowhere else, and asking the
// world about them leaks the mesh's device names to whatever resolver the phone
// happens to be using.
func TestMeshNamesAreNeverForwarded(t *testing.T) {
	s := testServer()
	s.Upstream = func([]byte) ([]byte, error) {
		t.Error("forwarded a mesh name upstream")
		return nil, errors.New("should not happen")
	}
	if _, err := s.answer(query(t, "laptop.mesh.", dnsmessage.TypeAAAA)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.answer(query(t, "nosuch.mesh.", dnsmessage.TypeAAAA)); err != nil {
		t.Fatal(err)
	}
}

// An upstream that cannot be reached is SERVFAIL, not REFUSED: clients retry
// another resolver on SERVFAIL and commonly treat REFUSED as final.
func TestUpstreamFailureIsServfail(t *testing.T) {
	s := testServer()
	s.Upstream = func([]byte) ([]byte, error) { return nil, errors.New("network unreachable") }

	reply, err := s.answer(query(t, "google.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := parse(t, reply)
	if h.RCode != dnsmessage.RCodeServerFailure {
		t.Errorf("rcode = %v, want SERVFAIL", h.RCode)
	}
}

// With no upstream configured — the Linux daemon — refusing is still right.
func TestWithoutUpstreamStillRefuses(t *testing.T) {
	reply, err := testServer().answer(query(t, "google.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := parse(t, reply)
	if h.RCode != dnsmessage.RCodeRefused {
		t.Errorf("rcode = %v, want REFUSED", h.RCode)
	}
}
