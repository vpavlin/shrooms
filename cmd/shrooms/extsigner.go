package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/vpavlin/shrooms/internal/cred"
)

// Signing without the private key ever being here (ADR-022).
//
// The admin key is the one secret in this system that never has to touch a
// participating device, and this takes that one step further: it does not have
// to touch the *admin's* device either. Shrooms computes the digest, something
// else signs it, and the signature is pasted back.
//
// That covers a Keycard without shrooms containing a card driver, and it covers
// an air-gapped machine, an HSM, or a person in another country, without
// containing anything for those either. The alternative — linking a card
// library — would have brought go-ethereum into the build for its secp256k1
// helpers and its logger, to support exactly one make of card.
//
// The signature is verified before it is used, which is the part that makes
// this safe to offer. A digest read off a terminal and typed back is a place
// where a wrong card, a stale clipboard or a buggy tool all produce plausible
// bytes, and a credential signed by the wrong key is not discovered here — it
// is discovered days later, on somebody else's device, as a device that cannot
// join.
type externalSigner struct {
	// auth is every key this mesh trusts. The signature must verify under one
	// of them, which means the caller does not have to say in advance which
	// key the far end holds — and a card that turns out to be the wrong card
	// fails here rather than silently.
	auth *cred.Authority

	// pub is the key written into admin_keys at mint time, when there is no
	// authority yet to check against.
	pub ed25519.PublicKey

	// command optionally produces the signature instead of a person. It runs
	// with the digest as its last argument and must print r‖s as hex.
	command string

	out *os.File
	in  *bufio.Reader
}

func (e *externalSigner) Public() ed25519.PublicKey { return e.pub }

// SignDigest obtains a signature over d from outside this process.
func (e *externalSigner) SignDigest(d [32]byte) ([]byte, error) {
	digest := hex.EncodeToString(d[:])

	var raw string
	var err error
	if e.command != "" {
		raw, err = e.runCommand(digest)
	} else {
		raw, err = e.ask(digest)
	}
	if err != nil {
		return nil, err
	}

	sig, err := parseDetachedSignature(raw)
	if err != nil {
		return nil, err
	}

	// Checked here, against every key the mesh trusts, before it becomes a
	// credential. See the type comment: this is the whole reason the detached
	// flow is safe to offer.
	for _, k := range e.trusted() {
		if cred.VerifyDigest(k, d, sig) {
			return sig, nil
		}
	}
	return nil, errors.New("that signature does not verify against any key this mesh trusts:\n" +
		"  the wrong card, the wrong key on the right card, or a signature over something else")
}

func (e *externalSigner) trusted() []ed25519.PublicKey {
	if e.auth != nil && len(e.auth.Keys) > 0 {
		return e.auth.Keys
	}
	if len(e.pub) > 0 {
		return []ed25519.PublicKey{e.pub}
	}
	return nil
}

// runCommand asks a program for the signature.
//
// The digest goes as the last argument rather than through a shell, so nothing
// in it can be interpreted — it is hex and could not be hostile anyway, but a
// signing path that invokes a shell is a signing path that can be surprised.
func (e *externalSigner) runCommand(digest string) (string, error) {
	fields := strings.Fields(e.command)
	if len(fields) == 0 {
		return "", errors.New("--sign-with is empty")
	}
	args := append(fields[1:], digest)
	out, err := exec.Command(fields[0], args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %w: %s", fields[0], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", fields[0], err)
	}
	return string(out), nil
}

// ask prints the digest and reads the signature back.
func (e *externalSigner) ask(digest string) (string, error) {
	w := e.out
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "\nSign this digest with the mesh's admin key:\n\n  %s\n\n", digest)
	fmt.Fprintf(w, "With a Keycard, for instance:\n\n  keycard-cli sign --hex %s\n\n", digest)
	fmt.Fprint(w, "Signature, as hex (r‖s, or whatever your tool prints): ")

	r := e.in
	if r == nil {
		r = stdin()
	}
	line, err := r.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", err
	}
	return line, nil
}

// parseDetachedSignature accepts what signing tools actually print.
//
// Tolerant on the way in and strict on the way out. Tools disagree about the
// shape of a secp256k1 signature — some print r and s on separate lines, some
// add the recovery byte Ethereum needs, some prefix 0x — and none of that
// changes the signature. What it must not do is guess: anything that does not
// reduce to exactly 64 bytes is refused rather than padded or truncated.
func parseDetachedSignature(raw string) ([]byte, error) {
	var b strings.Builder
	for _, f := range strings.Fields(raw) {
		f = strings.TrimPrefix(strings.TrimPrefix(f, "0x"), "0X")
		// Labels like "R:" or "Signature" are skipped rather than rejected, so
		// a whole block of tool output can be pasted in.
		if _, err := hex.DecodeString(f); err != nil {
			continue
		}
		b.WriteString(f)
	}
	s := b.String()
	if s == "" {
		return nil, errors.New("no hex in that; expected r‖s as 128 hex characters")
	}
	sig, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("that is not hex: %w", err)
	}
	switch len(sig) {
	case 64:
		return sig, nil
	case 65:
		// The trailing byte is a recovery id, which matters when the verifier
		// has to find the key and not when it already knows it.
		return sig[:64], nil
	default:
		return nil, fmt.Errorf("a signature is 64 bytes (r‖s); that is %d", len(sig))
	}
}
