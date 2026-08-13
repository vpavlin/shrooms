package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/cred"
)

func testAuthority(t *testing.T) (*cred.Authority, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := cred.NewAuthority(pub)
	if err != nil {
		t.Fatal(err)
	}
	return auth, priv
}

func devnull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// The whole point of the detached flow: a signature made somewhere else, read
// back, and checked before it is used.
func TestExternalSignerAcceptsAGoodSignature(t *testing.T) {
	auth, priv := testAuthority(t)
	d := sha256.Sum256([]byte("a credential"))
	sig := ed25519.Sign(priv, d[:])

	e := &externalSigner{
		auth: auth,
		out:  devnull(t),
		in:   bufio.NewReader(strings.NewReader(hex.EncodeToString(sig) + "\n")),
	}
	got, err := e.SignDigest(d)
	if err != nil {
		t.Fatalf("refused a valid signature: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(sig) {
		t.Error("returned something other than what was pasted in")
	}
}

// The case this exists to catch. A signature from the wrong key is plausible
// bytes of the right length, and without this check it becomes a credential
// that fails days later on somebody else's device.
func TestExternalSignerRefusesTheWrongKey(t *testing.T) {
	auth, _ := testAuthority(t)
	_, stranger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256([]byte("a credential"))
	sig := ed25519.Sign(stranger, d[:])

	e := &externalSigner{
		auth: auth,
		out:  devnull(t),
		in:   bufio.NewReader(strings.NewReader(hex.EncodeToString(sig) + "\n")),
	}
	if _, err := e.SignDigest(d); err == nil {
		t.Fatal("accepted a signature from a key the mesh does not trust")
	}
}

// A genuine signature over a different digest — the stale-clipboard case.
func TestExternalSignerRefusesTheWrongDigest(t *testing.T) {
	auth, priv := testAuthority(t)
	other := sha256.Sum256([]byte("a different credential"))
	sig := ed25519.Sign(priv, other[:])

	d := sha256.Sum256([]byte("a credential"))
	e := &externalSigner{
		auth: auth,
		out:  devnull(t),
		in:   bufio.NewReader(strings.NewReader(hex.EncodeToString(sig) + "\n")),
	}
	if _, err := e.SignDigest(d); err == nil {
		t.Fatal("accepted a signature over the wrong digest")
	}
}

// Tools print signatures in whatever shape suits them, and a whole block of
// output pasted in should work. What must never happen is a guess: anything
// that does not reduce to exactly 64 bytes is refused.
func TestParseDetachedSignature(t *testing.T) {
	r := strings.Repeat("ab", 32)
	s := strings.Repeat("cd", 32)

	for _, tc := range []struct {
		name, in string
		want     string
		wantErr  bool
	}{
		{"plain", r + s, r + s, false},
		{"0x prefixed", "0x" + r + s, r + s, false},
		{"r and s on separate lines", r + "\n" + s, r + s, false},
		{"a whole block of tool output",
			"Signature R: 0x" + r + "\nSignature S: 0x" + s + "\n", r + s, false},
		{"with a recovery byte", r + s + "01", r + s, false},
		{"too short", r, "", true},
		{"too long", r + s + r, "", true},
		{"nothing usable", "no hex here", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDetachedSignature(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused %q: %v", tc.in, err)
			}
			if hex.EncodeToString(got) != tc.want {
				t.Errorf("got %x, want %s", got, tc.want)
			}
		})
	}
}

// The scriptable path, exercised with a command rather than a person.
func TestExternalSignerRunsACommand(t *testing.T) {
	auth, priv := testAuthority(t)
	d := sha256.Sum256([]byte("a credential"))
	sig := hex.EncodeToString(ed25519.Sign(priv, d[:]))

	// printf rather than echo, because the digest is appended as the command's
	// last argument and echo would print that too — which the parser then sees
	// as 96 bytes of hex and refuses. That refusal is correct and worth
	// keeping: a signing command must print the signature and nothing else,
	// and one that prints more is caught rather than guessed at.
	e := &externalSigner{auth: auth, out: devnull(t), command: "printf " + sig}
	got, err := e.SignDigest(d)
	if err != nil {
		t.Fatalf("the command path failed: %v", err)
	}
	if hex.EncodeToString(got) != sig {
		t.Error("did not use what the command printed")
	}
}

// And the same refusal, asserted directly, since it is a safety property
// rather than an accident of how printf behaves.
func TestExternalSignerRefusesExtraOutput(t *testing.T) {
	auth, priv := testAuthority(t)
	d := sha256.Sum256([]byte("a credential"))
	sig := hex.EncodeToString(ed25519.Sign(priv, d[:]))

	// A tool that helpfully echoes the digest back alongside the signature.
	e := &externalSigner{auth: auth, out: devnull(t), command: "echo " + sig}
	if _, err := e.SignDigest(d); err == nil {
		t.Fatal("accepted output containing more than the signature")
	}
}

func TestExternalSignerReportsACommandThatFails(t *testing.T) {
	auth, _ := testAuthority(t)
	d := sha256.Sum256([]byte("x"))
	e := &externalSigner{auth: auth, out: devnull(t), command: "false"}
	if _, err := e.SignDigest(d); err == nil {
		t.Fatal("a failing signer reported success")
	}
}
