package mobile

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	keycard "github.com/keycard-tech/keycard-go/v4"
	kio "github.com/keycard-tech/keycard-go/v4/io"
	ktypes "github.com/keycard-tech/keycard-go/v4/types"

	"github.com/vpavlin/shrooms/internal/cred"
)

// A Keycard as the mesh authority, driven from the phone (ADR-022,
// docs/keycard-on-mobile.md).
//
// The card protocol runs here, in Go, shared with everything else this project
// does with credentials. Android supplies one thing: a way to move bytes to the
// card, which is `IsoDep.transceive`. That is the whole of the platform-specific
// surface, and it is why this is not a second Keycard implementation — pairing,
// the secure channel, PIN handling and signing all live in keycard-go.
//
// Why in the mobile module rather than internal/: keycard-go costs about
// 3.6 MiB linked, which is 7% of the APK and would be 29% of the daemon. The
// daemon does not need it — on a desktop the card is driven by an external
// signer (ADR-022) — and mobile/ is a separate module, so a server that will
// never meet a card links none of this.

// CardTransport moves one APDU to the card and returns its response.
//
// Implemented in Kotlin as `IsoDep.transceive(ByteArray): ByteArray`, which is
// the same shape. Bound through gomobile, so the signature is deliberately
// []byte rather than anything richer.
type CardTransport interface {
	Transmit(apdu []byte) ([]byte, error)
}

// KeycardPath is where the authority key lives on the card.
//
// The same path loam-keycard uses, so a card already enrolled for a Loam
// identity presents the same key here. That is a convenience, not a
// requirement: the authority is whatever public key the mesh was minted with.
const KeycardPath = "m/44'/60'/0'/0"

// pairingFile is where the pairing for this card is kept.
//
// It has to be kept. A card has a small, fixed number of pairing slots — pair
// on every use and they are gone, permanently, and the card needs unpairing
// with the PUK to recover. So pairing happens once, in CardEnrol, and every
// signature afterwards restores it.
//
// The pairing key is not a secret that admits anybody: it opens an encrypted
// channel to the card, and the PIN is still required to sign. It is written
// 0600 all the same, because it is one of two things an attacker would need.
func pairingFile(configDir string) string {
	return filepath.Join(configDir, "keycard-pairing")
}

// cardSession is an open, authenticated conversation with the card.
type cardSession struct {
	cs *keycard.CommandSet
}

// openCard selects the applet and restores the stored pairing.
func openCard(t CardTransport, configDir string) (*cardSession, error) {
	if t == nil {
		return nil, errors.New("no card transport")
	}
	cs := keycard.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return nil, fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	raw, err := os.ReadFile(pairingFile(configDir))
	if err != nil {
		return nil, fmt.Errorf("this card has not been enrolled here: %w", err)
	}
	p, err := decodePairing(string(raw))
	if err != nil {
		return nil, err
	}
	cs.SetPairing(p)
	// Version-agnostic, for the same reason CardEnrol pairs that way: a card
	// running applet 4.0 or later opens its channel differently, and the V1
	// call fails there in a way that reads as the wrong card.
	if err := cs.AutoOpenSecureChannel(); err != nil {
		return nil, fmt.Errorf("could not open a secure channel — is this the card that was enrolled? %w", err)
	}
	return &cardSession{cs: cs}, nil
}

func encodePairing(p *ktypes.Pairing) string {
	key := p.Key()
	b := append([]byte{p.Index()}, key[:]...)
	return base64.StdEncoding.EncodeToString(b)
}

func decodePairing(s string) (*ktypes.Pairing, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 33 {
		return nil, errors.New("the stored pairing is corrupt; enrol the card again")
	}
	var key [32]byte
	copy(key[:], b[1:])
	return ktypes.NewPairing(key, b[0]), nil
}

// CardEnrol pairs this phone with the card, once, and returns the authority's
// public key in the form an admin_keys entry takes.
//
// The pairing password is the card's, set when it was initialised. It is used
// here and not stored: what is stored is the pairing it produces.
//
// `KeycardDefaultPairing` is the factory default for a card initialised without
// one being chosen — keycard-cli's `internal/secrets.go` defines it, and its
// examples pair with PIN 123456 and PUK 123456789012 alongside. A card somebody
// has set up properly will have its own.
//
// Paired through the Auto* calls rather than Pair/OpenSecureChannel, because
// those are the version 1 path. A card running applet 4.0 or later authenticates
// with an X.509 certificate against the Status CA and needs no pairing password
// at all; keycard-go's own README says to use the version-agnostic calls, and
// Sparrow reaches for autoPair for the same reason. Calling the V1 path directly
// meant a modern card could not be paired whatever password was typed — a
// failure that would have read as "wrong password" while burning slots.
func CardEnrol(t CardTransport, configDir, pairingPassword string) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	cs := keycard.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	if err := cs.AutoPairWithMode(pairingPassword, keycard.P2PairAny); err != nil {
		return "", fmt.Errorf("pairing refused — wrong pairing password, or no free "+
			"slots on the card (a card has %d): %w", keycard.PairingMaxClientCount, err)
	}
	if err := cs.AutoOpenSecureChannel(); err != nil {
		return "", fmt.Errorf("paired but could not open a secure channel: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(pairingFile(configDir), []byte(encodePairing(cs.Pairing())), 0o600); err != nil {
		return "", fmt.Errorf("paired but could not store the pairing: %w", err)
	}
	return cardPublicKey(cs)
}

// cardPublicKey exports the authority's public half, compressed.
func cardPublicKey(cs *keycard.CommandSet) (string, error) {
	_, pub, err := cs.ExportKey(true, false, true, KeycardPath)
	if err != nil {
		return "", fmt.Errorf("could not read the key at %s: %w", KeycardPath, err)
	}
	c, err := cred.CompressPoint(pub)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(c), nil
}

// CardPublicKey reads the authority key from an already-enrolled card.
func CardPublicKey(t CardTransport, configDir string) (string, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return "", err
	}
	return cardPublicKey(s.cs)
}

// cardSigner is cred.Signer backed by the card.
//
// One tap, one signature: the session is opened for the operation and closed
// with it, because between two signatures the phone may have been in a pocket
// and the card on a desk.
type cardSigner struct {
	t         CardTransport
	configDir string
	pin       string
	pub       []byte
}

// newCardSigner opens a session far enough to read the public key, so that a
// wrong PIN or an unenrolled card is reported before anything is half-done.
func newCardSigner(t CardTransport, configDir, pin string) (*cardSigner, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return nil, err
	}
	if err := s.cs.VerifyPIN(pin); err != nil {
		return nil, fmt.Errorf("PIN refused: %w", err)
	}
	hexPub, err := cardPublicKey(s.cs)
	if err != nil {
		return nil, err
	}
	pub, err := hex.DecodeString(hexPub)
	if err != nil {
		return nil, err
	}
	return &cardSigner{t: t, configDir: configDir, pin: pin, pub: pub}, nil
}

// Public is the compressed secp256k1 point, which is what an authority holds
// and what verifyKey dispatches on by length.
func (c *cardSigner) Public() ed25519.PublicKey { return c.pub }

// SignDigest asks the card. Every call is a fresh tap.
func (c *cardSigner) SignDigest(d [32]byte) ([]byte, error) {
	s, err := openCard(c.t, c.configDir)
	if err != nil {
		return nil, err
	}
	if err := s.cs.VerifyPIN(c.pin); err != nil {
		return nil, fmt.Errorf("PIN refused: %w", err)
	}
	sig, err := s.cs.SignWithPath(d[:], KeycardPath)
	if err != nil {
		return nil, fmt.Errorf("the card did not sign: %w", err)
	}
	return cred.CompactSig(sig.R(), sig.S())
}

var _ cred.Signer = (*cardSigner)(nil)

// CardSelfTest proves the whole signing path against a real card: pairing,
// PIN, the card's signature, and the two conversions on the way back.
//
// It signs a digest and checks the result with the same function a peer uses on
// a credential. That is the point — a card that returns something plausible and
// unverifiable is the failure this is looking for, and it cannot be found
// without hardware. Nothing is published and no credential is issued.
//
// Returns the authority key on success, so the caller can see which card it
// just proved.
func CardSelfTest(t CardTransport, configDir, pin string) (string, error) {
	s, err := newCardSigner(t, configDir, pin)
	if err != nil {
		return "", err
	}
	// A fixed digest rather than a random one: this is a self-test, and a
	// failure that only happens for some inputs is worth being able to repeat.
	var d [32]byte
	for i := range d {
		d[i] = byte(i)
	}
	sig, err := s.SignDigest(d)
	if err != nil {
		return "", err
	}
	if len(sig) != 64 {
		return "", fmt.Errorf("the card produced a %d-byte signature, want 64", len(sig))
	}
	if !cred.VerifyDigest(s.Public(), d, sig) {
		return "", errors.New("the card signed, but the signature does not verify against its own key — " +
			"the conversion between what the card returns and what this project checks is wrong")
	}
	return hex.EncodeToString(s.pub), nil
}
