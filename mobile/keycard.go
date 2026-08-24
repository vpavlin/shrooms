package mobile

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func CardEnrol(t CardTransport, configDir, pairingPassword, pin string) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	cs := keycard.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	// Asked before pairing rather than after: a card without this answers 6d00
	// to PAIR — an instruction it does not implement — and the error that comes
	// back otherwise blames the pairing password, which is never the reason.
	if info := cs.ApplicationInfo; info != nil && !info.HasSecureChannelCapability() {
		return "", errors.New("this card has no secure-channel capability, so it " +
			"cannot be paired at all. Check this card to see what it does have")
	}
	if err := cs.AutoPairWithMode(pairingPassword, keycard.P2PairAny); err != nil {
		return "", fmt.Errorf("%w", pairingError(err))
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
	// The PIN before the key, because EXPORT KEY needs an authenticated
	// session. Without it the card answers 6985 — conditions of use not
	// satisfied — and the message blamed the path, which is the one part that
	// was fine. The pairing above is already stored by this point, so a wrong
	// PIN here costs an attempt and not a slot.
	if err := cs.VerifyPIN(pin); err != nil {
		return "", fmt.Errorf("paired, and the PIN was refused — the pairing is "+
			"saved, so try \"Read key\" rather than pairing again: %w", err)
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
//
// Needs the PIN: EXPORT KEY is only allowed inside an authenticated session, and
// without one the card answers 6985.
func CardPublicKey(t CardTransport, configDir, pin string) (string, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return "", err
	}
	if err := s.cs.VerifyPIN(pin); err != nil {
		return "", fmt.Errorf("PIN refused: %w", err)
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

// pairingError says what a pairing failure means, where the card says 6a84.
//
// The library maps that status word to a readable error, but only inside Pair()
// — the version 1 path. AutoPairWithMode goes a different way and the status
// word comes back raw, as "bad response 6a84: PAIR step 1 failed", which names
// the step and not the problem.
//
// The problem is that the card is full. A Keycard has five pairing slots and
// they are consumed by every device that has ever paired with it, including
// this app on a previous install and any attempt that got half way. Nothing
// about it is a wrong password, which is what somebody will otherwise try next.
func pairingError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "6a84") ||
		errors.Is(err, keycard.ErrNoAvailablePairingSlots) {
		return fmt.Errorf("this card has no free pairing slots — it has %d and "+
			"they are all taken, by other devices or by earlier attempts from "+
			"this one. Free one with \"forget other devices\" if this phone is "+
			"already paired, or unpair elsewhere. The pairing password is not "+
			"the problem: %w", keycard.PairingMaxClientCount, err)
	}
	return fmt.Errorf("pairing refused — wrong pairing password, or the card "+
		"refused the request: %w", err)
}

// CardUnpairOthers frees every pairing slot except this phone's.
//
// Needs this phone to be paired already, because unpairing happens inside the
// secure channel a pairing opens — a card with no slots left and no pairing
// here cannot be recovered from this app, and needs keycard-cli or another tool
// that still holds one.
//
// Kept separate from enrolment and named for what it does to somebody else's
// pairing, because it is not undoable: every other device that was paired with
// this card stops being able to use it.
func CardUnpairOthers(t CardTransport, configDir string) (string, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return "", err
	}
	if err := s.cs.UnpairOthers(); err != nil {
		return "", fmt.Errorf("could not free the other pairing slots: %w", err)
	}
	return fmt.Sprintf("freed the other slots; this phone is still paired (%d in total)",
		keycard.PairingMaxClientCount), nil
}

// CardStatus reports what state a card is in, without pairing or a PIN.
//
// SELECT alone answers this: it costs no pairing slot, spends no PIN attempt,
// and needs no secret. That makes it the thing to run first on an unfamiliar
// card, and the thing that would have answered Vaclav's first card in one tap
// instead of a failed pairing and a raw status word.
//
// Four questions, in the order they stop you:
//
//   - initialised? Until INIT has been run there is no PIN, no PUK and no
//     pairing password, so pairing cannot work and no password is the right one.
//   - does it hold a key? A card can be initialised and empty. KeyUID is the
//     hash of the master public key and is absent when there is none, and
//     without one there is nothing at m/44'/60'/0'/0 to sign with.
//   - how many pairing slots are free? Five is the maximum and zero is what
//     "6a84" means.
//   - which applet version? Below 4.0 pairing uses a password; at 4.0 and above
//     it uses a certificate and the password is not asked for.
//
// Neither INIT nor key generation is done here. Both are one-time acts that
// decide what a card IS, they are irreversible, and doing them from a VPN's
// settings screen by accident is not a thing anybody should be able to do. Use
// keycard-cli or the Keycard app.
func CardStatus(t CardTransport) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	cs := keycard.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	info := cs.ApplicationInfo
	if info == nil {
		return "", errors.New("the card answered SELECT with nothing to read")
	}

	version := "unknown"
	if len(info.Version) >= 2 {
		version = fmt.Sprintf("%d.%d", info.Version[0], info.Version[1])
	}
	slots := "unknown"
	if len(info.AvailableSlots) >= 1 {
		free := int(info.AvailableSlots[0])
		slots = fmt.Sprintf("%d of %d free", free, keycard.PairingMaxClientCount)
		if free == 0 {
			slots += " — this is what 6a84 means"
		}
	}

	caps := capabilityList(info)

	switch {
	case !info.HasSecureChannelCapability():
		// A card that cannot open a secure channel cannot be paired, and the
		// PAIR instruction is not implemented — which is what "6d00" means, an
		// instruction the applet does not have. A Cash card is the usual reason
		// to be holding one of these.
		return fmt.Sprintf("applet %s — this card has NO SECURE CHANNEL "+
			"capability, so it cannot be paired and cannot sign for a mesh. "+
			"That is what 6d00 means: the applet does not implement PAIR. "+
			"Capabilities: %s", version, caps), nil
	case !info.Initialized:
		return fmt.Sprintf("applet %s, NOT INITIALISED — it has no PIN, PUK or "+
			"pairing password yet, so pairing cannot work. Initialise it with "+
			"keycard-cli or the Keycard app first (pairing slots: %s)",
			version, slots), nil
	case len(info.KeyUID) == 0:
		return fmt.Sprintf("applet %s, initialised but HOLDS NO KEY — pairing "+
			"will work and there is nothing to sign with. Generate or load a key "+
			"with keycard-cli or the Keycard app (pairing slots: %s)",
			version, slots), nil
	default:
		return fmt.Sprintf("applet %s, initialised, holds a key (%x), pairing "+
			"slots: %s, capabilities: %s", version,
			info.KeyUID[:min(4, len(info.KeyUID))], slots, caps), nil
	}
}

// capabilityList names what a card says it can do.
//
// Worth showing because the absence of one is the readable form of a status
// word: a card with no secure channel answers 6d00 to PAIR, and a card with no
// key management cannot be given a key. Both read as "the card refused" without
// this.
func capabilityList(info *ktypes.ApplicationInfo) string {
	var have []string
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"secure-channel", info.HasSecureChannelCapability()},
		{"key-management", info.HasKeyManagementCapability()},
		{"credentials", info.HasCredentialsManagementCapability()},
		{"ndef", info.HasNDEFCapability()},
	} {
		if c.ok {
			have = append(have, c.name)
		}
	}
	if len(have) == 0 {
		return "none reported"
	}
	return strings.Join(have, ", ")
}
