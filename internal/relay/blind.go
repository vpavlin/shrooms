package relay

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/vpavlin/shrooms/internal/identity"
)

// A relay that forwards for a mesh it is not a member of (docs/blind-relays.md).
//
// Everything a relay does with a tunnel key is a map lookup — it never performs
// cryptography with one. That is what makes this possible without a new wire
// format for the frames that carry traffic: the same register and forward
// frames work unchanged, carrying a value the relay cannot interpret instead of
// one it could.
//
// Two keys doing two jobs, which is the whole design:
//
//	"may I use this relay?"  belongs to the relay. A token, or nothing at all
//	                         when the relay is open. It authenticates frames
//	                         and says nothing about who anybody is.
//
//	"which device is this?"  belongs to the mesh and never reaches the relay.
//	                         Devices register under a tag derived from the mesh
//	                         relay key, so only members can compute or address
//	                         one.
//
// The relay's table becomes flat — tag to address — and it never learns that
// meshes exist, let alone how many it carries or which devices belong together.

// OpenKey is the frame key of a relay that anybody may use.
//
// Public by construction, and that is not a weakness being tolerated: on an
// open relay the MAC is a checksum, not an authenticator, and every security
// property comes from elsewhere — the registrant's own signature, the
// first-claim-wins rule, and the routability check that stops the relay being
// pointed at a third party. Pretending a key everyone has provides
// authentication would be the actual danger.
func OpenKey() Key {
	return keyFrom("mesh/v1/relay/open", nil)
}

// TokenKey is the frame key of a relay that only token holders may use.
//
// The token is a relay-wide secret shared by everyone the operator agreed to
// carry — one token for a mesh, not one per device — so it is policy rather
// than safety. Safety is the routability check, which needs no secret at all.
func TokenKey(token string) Key {
	return keyFrom("mesh/v1/relay/token", []byte(token))
}

// Tag is the handle a device registers under on a blind relay.
//
// Derived from the *mesh* relay key, which the relay does not have, so the
// operator sees an opaque value: they cannot recover the tunnel key behind it,
// or learn anything about the mesh from the identifier.
//
// And derived from the relay's own address, which is what makes the value
// per-relay rather than merely per-mesh. Without that a device presents the
// same handle everywhere it goes, and two operators comparing notes can link a
// device — and by extension a whole mesh — across their relays. That property
// was claimed here before it was true; binding the address is what makes the
// claim honest.
//
// Both ends can compute it because both hold the mesh relay key, each other's
// tunnel keys, and the address of the relay they have both chosen — which they
// must agree on anyway, since a relay can only forward between peers registered
// with it.
//
// The address is used in its canonical form rather than as configured, so a
// relay written one way by one device and another way by the other still yields
// one tag.
func Tag(meshKey Key, at netip.AddrPort, wg identity.WGKey) identity.WGKey {
	salt := make([]byte, 0, len(wg)+64)
	salt = append(salt, wg[:]...)
	salt = append(salt, []byte(at.String())...)

	r := hkdf.New(sha256.New, meshKey[:], salt, []byte("mesh/v1/relay/tag"))
	var t identity.WGKey
	if _, err := r.Read(t[:]); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return t
}

func keyFrom(label string, secret []byte) Key {
	r := hkdf.New(sha256.New, append([]byte(label), secret...), nil, []byte(label))
	var k Key
	if _, err := r.Read(k[:]); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return k
}

const (
	// TypeChallenge is the relay asking a registrant to prove it receives at
	// the address it is registering from.
	TypeChallenge Type = 3
	// TypeConfirm is the answer, echoing the nonce.
	TypeConfirm Type = 4
)

// NonceLen is the size of a routability challenge.
//
// Sixteen bytes is far past guessing: an attacker gets one attempt per
// challenge and the challenge expires, so this is not even a work factor.
const NonceLen = 16

const (
	challengeLen = 1 + NonceLen + macLen
	confirmLen   = 1 + keyLen + NonceLen + stampLen + devicePubLen + sigLen + macLen
)

// EncodeChallenge builds the relay's nonce.
//
// Unsigned, because a blind relay has no identity anybody could check it
// against, and it does not need one: the nonce is not a claim. Its only job is
// to be unguessable and to arrive at the address being registered.
func EncodeChallenge(k Key, nonce [NonceLen]byte) []byte {
	buf := make([]byte, 0, challengeLen)
	buf = append(buf, byte(TypeChallenge))
	buf = append(buf, nonce[:]...)
	return append(buf, mac(k, buf)...)
}

// EncodeConfirm answers a challenge, signed by the same device that registered.
//
// It repeats the key and the device signature rather than relying on the relay
// remembering them, so the relay's pending state is a nonce and an address and
// nothing else — a table an attacker cannot make interesting by flooding it.
func EncodeConfirm(k Key, self identity.WGKey, nonce [NonceLen]byte,
	priv ed25519.PrivateKey, now time.Time) []byte {

	buf := make([]byte, 0, confirmLen)
	buf = append(buf, byte(TypeConfirm))
	buf = append(buf, self[:]...)
	buf = append(buf, nonce[:]...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(now.Unix()))
	buf = append(buf, priv.Public().(ed25519.PublicKey)...)
	buf = append(buf, ed25519.Sign(priv, buf)...)
	return append(buf, mac(k, buf)...)
}

// NewNonce returns an unguessable challenge.
func NewNonce() ([NonceLen]byte, error) {
	var n [NonceLen]byte
	_, err := rand.Read(n[:])
	return n, err
}

// decodeChallenge and decodeConfirm are the Decode cases for the two frames
// above, kept here so the routability exchange reads in one place.
func decodeChallenge(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) != challengeLen {
		return nil, fmt.Errorf("challenge frame is %d bytes, want %d", len(pkt), challengeLen)
	}
	if !verify(k, pkt[:1+NonceLen], pkt[1+NonceLen:]) {
		return nil, errors.New("authentication failed")
	}
	f := &Frame{Type: TypeChallenge}
	copy(f.Nonce[:], pkt[1:1+NonceLen])
	return f, nil
}

func decodeConfirm(k Key, pkt []byte) (*Frame, error) {
	if len(pkt) != confirmLen {
		return nil, fmt.Errorf("confirm frame is %d bytes, want %d", len(pkt), confirmLen)
	}
	signed := 1 + keyLen + NonceLen + stampLen + devicePubLen
	if !verify(k, pkt[:signed+sigLen], pkt[signed+sigLen:]) {
		return nil, errors.New("authentication failed")
	}
	f := &Frame{Type: TypeConfirm}
	copy(f.Key[:], pkt[1:1+keyLen])
	copy(f.Nonce[:], pkt[1+keyLen:1+keyLen+NonceLen])
	f.At = int64(binary.BigEndian.Uint64(pkt[1+keyLen+NonceLen : 1+keyLen+NonceLen+stampLen]))
	f.DevicePub = append([]byte(nil), pkt[1+keyLen+NonceLen+stampLen:signed]...)
	if !ed25519.Verify(ed25519.PublicKey(f.DevicePub), pkt[:signed], pkt[signed:signed+sigLen]) {
		return nil, ErrNotSignedByDevice
	}
	return f, nil
}

// Challenges that cost the relay no memory.
//
// The first version kept a table of outstanding challenges keyed by the address
// they were sent to. That is a remote memory-exhaustion vector, and an easy one:
// a register frame's signature covers the frame, not the address it came from,
// so an attacker generates one keypair, signs one register, and replays it from
// as many spoofed sources as their network permits. Each arrival allocated an
// entry that lived for ChallengeTTL. On an open relay the frame key is public
// by design, so nothing even had to be stolen.
//
// The fix is the one TCP reached for against SYN floods: put the state in the
// nonce. The challenge is a keyed hash over the things it is a challenge
// *about* — where it was sent, which handle, which device, and roughly when.
// Answering it proves the sender received it; recomputing proves it was ours,
// and neither needs anything remembered.
//
// The cookie key is generated per process and never leaves it. It has no
// meaning beyond this and does not survive a restart, so a challenge in flight
// across one is simply reissued.

// cookieEra is the granularity of the timestamp inside a challenge.
//
// A challenge is accepted in its own era and the one before, so the window a
// client actually gets is between one and two of these — comfortably more than
// a round trip, and short enough that a captured challenge is useless quickly.
const cookieEra = 15 * time.Second

// challengeFor derives the nonce for one registration attempt.
//
// Bound to the address, the handle and the device key together: a cookie minted
// for one of them must not answer for another, or an attacker could collect a
// challenge for their own handle and use it to claim somebody else's.
func challengeFor(cookieKey [32]byte, from netip.AddrPort, key identity.WGKey,
	devicePub []byte, era int64) [NonceLen]byte {

	h := hmac.New(sha256.New, cookieKey[:])
	h.Write([]byte(from.String()))
	h.Write(key[:])
	h.Write(devicePub)
	_ = binary.Write(h, binary.BigEndian, era)

	var out [NonceLen]byte
	copy(out[:], h.Sum(nil))
	return out
}

// validChallenge reports whether a nonce is one we would have issued recently.
func validChallenge(cookieKey [32]byte, from netip.AddrPort, key identity.WGKey,
	devicePub []byte, got [NonceLen]byte, now time.Time) bool {

	era := now.Unix() / int64(cookieEra/time.Second)
	for _, e := range [2]int64{era, era - 1} {
		want := challengeFor(cookieKey, from, key, devicePub, e)
		if subtle.ConstantTimeCompare(want[:], got[:]) == 1 {
			return true
		}
	}
	return false
}

// RelayIdentity derives the key a device signs to a blind relay with.
//
// Not the device's mesh identity, which is the whole point. A register frame
// carries the signing key in cleartext — the relay needs it to enforce
// first-claim-wins — so signing with the mesh identity hands every relay a
// stable 32-byte identifier for the device. Two operators comparing register
// frames then see byte-identical values, and the per-relay tag beside it
// achieves nothing: the plaintext next to it is a global name.
//
// That is exactly the hole the tag was supposed to close, left open by the
// field beside it. The derivation was audited; the message carrying it was not.
//
// So: a key per relay, derived from the mesh relay key and the relay's address.
// The relay can still tell one registration from another and still refuse a
// second device claiming a held handle, because within one relay this key is
// stable. Across relays it is unrelated, and it reveals nothing about the mesh
// identity it came from.
//
// Only the registering device needs to compute this. It is not a shared secret
// and never has to agree with anybody.
func RelayIdentity(meshKey Key, at netip.AddrPort, devicePub ed25519.PublicKey) ed25519.PrivateKey {
	r := hkdf.New(sha256.New, meshKey[:], devicePub, []byte("mesh/v1/relay/identity/"+at.String()))
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, seed); err != nil {
		panic(fmt.Sprintf("hkdf: %v", err))
	}
	return ed25519.NewKeyFromSeed(seed)
}
