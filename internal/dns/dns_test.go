package dns

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var laptop = netip.MustParseAddr("fd3b:ffe9:f81:81a7:18bc:69b1:9bb:7e69")

func testServer() *Server {
	return &Server{
		Suffix: "mesh",
		Lookup: func(host string) (netip.Addr, bool) {
			if host == "laptop" {
				return laptop, true
			}
			return netip.Addr{}, false
		},
	}
}

func query(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x1234, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  typ,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func parse(t *testing.T, reply []byte) (dnsmessage.Header, []dnsmessage.Resource) {
	t.Helper()
	var m dnsmessage.Message
	if err := m.Unpack(reply); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	return m.Header, m.Answers
}

func TestResolvesAMeshName(t *testing.T) {
	reply, err := testServer().answer(query(t, "laptop.mesh.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	h, answers := parse(t, reply)
	if h.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("rcode = %v", h.RCode)
	}
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	got := answers[0].Body.(*dnsmessage.AAAAResource).AAAA
	if netip.AddrFrom16(got) != laptop {
		t.Errorf("resolved to %v, want %v", netip.AddrFrom16(got), laptop)
	}
}

// The qualified form from ADR-015 must resolve too, since it is the one that
// survives two meshes holding the same device name.
func TestResolvesQualifiedName(t *testing.T) {
	reply, err := testServer().answer(query(t, "laptop.home.mesh.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	h, answers := parse(t, reply)
	if h.RCode != dnsmessage.RCodeSuccess || len(answers) != 1 {
		t.Fatalf("rcode=%v answers=%d", h.RCode, len(answers))
	}
}

// A query for a name that exists, of a type we have no records for, must be
// NOERROR with no answers. NXDOMAIN asserts the name does not exist at all, and
// a resolver that believes that will not go on to ask for AAAA — so getting
// this wrong makes every lookup fail on a dual-stack client.
func TestAQueryForExistingNameIsEmptyNotNXDOMAIN(t *testing.T) {
	reply, err := testServer().answer(query(t, "laptop.mesh.", dnsmessage.TypeA))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	h, answers := parse(t, reply)
	if h.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("rcode = %v, want NOERROR", h.RCode)
	}
	if len(answers) != 0 {
		t.Errorf("got %d answers for an A query on an IPv6-only mesh", len(answers))
	}
}

func TestUnknownMeshNameIsNXDOMAIN(t *testing.T) {
	reply, err := testServer().answer(query(t, "nosuch.mesh.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	h, _ := parse(t, reply)
	if h.RCode != dnsmessage.RCodeNameError {
		t.Errorf("rcode = %v, want NXDOMAIN", h.RCode)
	}
}

// Anything outside the suffix is REFUSED, never forwarded and never denied.
//
// A VPN that quietly becomes the system resolver sees every query the device
// makes. REFUSED says "not mine"; NXDOMAIN would assert the name does not exist
// anywhere, which we are in no position to claim.
func TestOutsideSuffixIsRefused(t *testing.T) {
	for _, name := range []string{"example.com.", "mesh.", "laptop.notmesh."} {
		reply, err := testServer().answer(query(t, name, dnsmessage.TypeAAAA))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h, answers := parse(t, reply)
		if h.RCode != dnsmessage.RCodeRefused {
			t.Errorf("%s: rcode = %v, want REFUSED", name, h.RCode)
		}
		if len(answers) != 0 {
			t.Errorf("%s: answered a name outside the mesh", name)
		}
	}
}

// Never claim recursion. Advertising it invites clients to send us the world's
// queries, which is the leak this design exists to avoid.
func TestNeverOffersRecursion(t *testing.T) {
	reply, err := testServer().answer(query(t, "laptop.mesh.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := parse(t, reply)
	if h.RecursionAvailable {
		t.Error("advertised recursion available")
	}
	if !h.Authoritative {
		t.Error("did not claim authority for a name it serves")
	}
}

func TestMalformedQueryIsIgnored(t *testing.T) {
	if _, err := testServer().answer([]byte{0x00, 0x01, 0x02}); err == nil {
		t.Error("accepted a malformed query")
	}
}

func TestStatsCounted(t *testing.T) {
	s := testServer()
	s.answer(query(t, "laptop.mesh.", dnsmessage.TypeAAAA))
	s.answer(query(t, "example.com.", dnsmessage.TypeAAAA))
	q, a, r, _, _ := s.Stats()
	if q != 2 || a != 1 || r != 1 {
		t.Errorf("queries=%d answers=%d refused=%d, want 2/1/1", q, a, r)
	}
}
