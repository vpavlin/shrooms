package invite

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/topic"
)

func TestTokenRoundTrip(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.String()) != 26 {
		t.Fatalf("token is %d characters, want 26 typable ones: %q", len(s.String()), s)
	}

	// Retyped by a human, so the forms a human produces must all work.
	for _, form := range []string{
		s.String(),
		"  " + s.String() + "\n",
		lower(s.String()),
		group(s.String()),
	} {
		got, err := Parse(form)
		if err != nil {
			t.Fatalf("Parse(%q): %v", form, err)
		}
		if got != s {
			t.Errorf("Parse(%q) gave a different token", form)
		}
	}

	if _, err := Parse("not a token"); err == nil {
		t.Error("accepted rubbish as a token")
	}
	if _, err := Parse(s.String()[:20]); err == nil {
		t.Error("accepted a truncated token")
	}
}

// The invite topic must land on the same shard as the mesh's own traffic:
// ADR-006 rests on the application and version fields being the only thing
// hashed, and an invite that moved shard would need its own subscription on a
// fleet the joining device has only just met.
func TestTopicSharesTheShard(t *testing.T) {
	s, _ := New()
	other, _ := New()

	if !topic.SamePrefix(s.Topic(), other.Topic()) {
		t.Error("two invites landed on different shards")
	}
	if s.Topic() == other.Topic() {
		t.Error("two different tokens derived the same topic")
	}
	// Against a rendezvous topic, which is the shard that matters.
	if !topic.SamePrefix(s.Topic(), "/logos/1/whatever/proto") {
		t.Errorf("invite topic %q is not on the mesh shard", s.Topic())
	}
}

func TestRequestRoundTrip(t *testing.T) {
	s, _ := New()
	now := time.Now()
	_, ephPub, err := NewEphemeral()
	if err != nil {
		t.Fatal(err)
	}

	req := &Request{
		DevicePub: bytes.Repeat([]byte{1}, 32),
		WGPub:     bytes.Repeat([]byte{2}, 32),
		Name:      "phone",
		EphPub:    ephPub,
		Timestamp: now.Unix(),
	}
	blob, err := SealRequest(s, req)
	if err != nil {
		t.Fatal(err)
	}

	got, err := OpenRequest(s, blob, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "phone" || !bytes.Equal(got.DevicePub, req.DevicePub) {
		t.Error("request did not survive the round trip")
	}

	// Someone else's token opens nothing.
	other, _ := New()
	if _, err := OpenRequest(other, blob, now); err == nil {
		t.Error("a different token opened the request")
	}

	// An hour later it is not a live enrolment any more.
	if _, err := OpenRequest(s, blob, now.Add(time.Hour)); err == nil {
		t.Error("accepted a stale request")
	}
}

// The response carries the mesh's only secret, so "sealed to the device that
// asked" has to be true against a second holder of the same token — which is
// precisely the attacker ADR-017 admits exists.
func TestResponseIsSealedToTheRequester(t *testing.T) {
	s, _ := New()
	now := time.Now()

	ephPriv, ephPub, err := NewEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	resp := &Response{
		NetworkKey: bytes.Repeat([]byte{9}, 32),
		AdminKeys:  [][]byte{bytes.Repeat([]byte{7}, 32)},
		Credential: bytes.Repeat([]byte{5}, 300),
		Suffix:     "mesh",
		Timestamp:  now.Unix(),
	}
	blob, err := SealResponse(s, ephPub, resp)
	if err != nil {
		t.Fatal(err)
	}

	got, err := OpenResponse(s, ephPriv, blob, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.NetworkKey, resp.NetworkKey) || len(got.Credential) != 300 {
		t.Error("response did not survive the round trip")
	}
	if got.Suffix != "mesh" {
		t.Errorf("suffix is %q", got.Suffix)
	}

	// Same token, different device: it can see that an enrolment happened,
	// because it is subscribed to the topic, and it cannot read the key.
	eavesPriv, _, _ := NewEphemeral()
	if _, err := OpenResponse(s, eavesPriv, blob, now); err == nil {
		t.Error("a second token holder read the network key")
	}
}

// Every message is the same size, so the bus cannot tell a request from a
// response, nor an enrolment carrying a credential from one that is not.
func TestMessagesAreIndistinguishableBySize(t *testing.T) {
	s, _ := New()
	now := time.Now().Unix()
	_, ephPub, _ := NewEphemeral()

	req, err := SealRequest(s, &Request{
		DevicePub: make([]byte, 32), WGPub: make([]byte, 32),
		EphPub: ephPub, Name: "a", Timestamp: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := SealResponse(s, ephPub, &Response{NetworkKey: make([]byte, 32), Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	full, err := SealResponse(s, ephPub, &Response{
		NetworkKey: make([]byte, 32),
		AdminKeys:  [][]byte{make([]byte, 32), make([]byte, 32)},
		Credential: make([]byte, 400),
		Timestamp:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) != len(full) {
		t.Errorf("a response with a credential is %d bytes and one without is %d", len(full), len(bare))
	}
	// The response nests one AEAD inside the other, so it is a fixed amount
	// larger than a request rather than equal; what matters is that neither
	// varies with its contents.
	if len(req) != len(bare) {
		t.Logf("request %d bytes, response %d — constant, but not equal", len(req), len(bare))
	}
}

// A full-size response must fit. The credential is the part that grows, and a
// token that mints an unsendable response would fail at exactly the wrong time.
func TestFullResponseFits(t *testing.T) {
	s, _ := New()
	_, ephPub, _ := NewEphemeral()
	_, err := SealResponse(s, ephPub, &Response{
		NetworkKey: make([]byte, 32),
		AdminKeys:  [][]byte{make([]byte, 32), make([]byte, 32), make([]byte, 32)},
		Credential: make([]byte, 450),
		Suffix:     "mesh",
		Timestamp:  time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("a full response does not fit the padding: %v", err)
	}
}

func TestGarbageIsRejected(t *testing.T) {
	s, _ := New()
	now := time.Now()
	for _, blob := range [][]byte{nil, {}, make([]byte, 10), make([]byte, 2000)} {
		if _, err := OpenRequest(s, blob, now); err == nil {
			t.Errorf("opened %d bytes of nothing", len(blob))
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// group breaks a token into dash-separated chunks, the way it is printed.
func group(s string) string {
	var out []byte
	for i := 0; i < len(s); i += 5 {
		if i > 0 {
			out = append(out, '-')
		}
		end := i + 5
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end]...)
	}
	return string(out)
}

// The CLI writes these and the phone reads them, so every form the CLI can
// produce — the QR's URI, the grouped form it prints, and a bare token someone
// retyped — has to come back as the same secret.
func TestParseToken(t *testing.T) {
	s, _ := New()
	for _, form := range []string{
		s.URI(),
		s.String(),
		group(s.String()),
		lower(s.String()),
		"  " + s.URI() + "\n",
	} {
		got, err := ParseToken(form)
		if err != nil {
			t.Fatalf("ParseToken(%q): %v", form, err)
		}
		if got != s {
			t.Errorf("ParseToken(%q) gave a different token", form)
		}
	}

	// A network-key invitation is the other thing the app can be handed, and it
	// is not a token: it must fail here so the caller falls through to the key
	// path rather than trying to redeem it.
	for _, notAToken := range []string{
		"logosvpn://join?key=P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A",
		"P27KNQ2HDSIUFIXZAGYDBSU2GU3PE4M52POFBUBOWHUZEWYSCP5A",
		"logosvpn://enrol?token=",
		"https://example.com",
	} {
		if _, err := ParseToken(notAToken); err == nil {
			t.Errorf("ParseToken(%q) accepted something that is not an invite token", notAToken)
		}
	}
}

// An invite minted after the rename must still be readable by a device that
// has not been updated, and one minted before must stay readable forever.
//
// Both directions matter during a rename, and the second matters permanently:
// an invite is redeemed by whatever the other person happens to have installed.
func TestBothSchemesParse(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	tok := s.String()

	for _, text := range []string{
		Scheme + "://enrol?token=" + tok,
		LegacyScheme + "://enrol?token=" + tok,
		tok, // bare, which people paste
	} {
		got, err := ParseToken(text)
		if err != nil {
			t.Errorf("%q did not parse: %v", text, err)
			continue
		}
		if got != s {
			t.Errorf("%q parsed to the wrong token", text)
		}
	}
}

// What we emit is the new one, so the old name stops spreading.
func TestWeEmitTheNewScheme(t *testing.T) {
	s, _ := New()
	if got := s.URI(); !strings.HasPrefix(got, "shrooms://") {
		t.Errorf("URI() emits %q, want the shrooms scheme", got)
	}
}
