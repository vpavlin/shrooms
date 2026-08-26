package keycard

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	kc "github.com/keycard-tech/keycard-go/v4"
	kio "github.com/keycard-tech/keycard-go/v4/io"
	ktypes "github.com/keycard-tech/keycard-go/v4/types"

	"github.com/vpavlin/shrooms/internal/cred"
)

// Package keycard drives a Keycard: pairing, the secure channel, the PIN, and
// signing at this project's own derivation path (ADR-022).
//
// It lived in mobile/ while a phone was the only thing that could reach a card,
// and mobile/ is a separate Go module — so nothing on the desktop could import
// any of it. Moving it here is what lets `admin init` talk to a card over a
// reader instead of having a key copied out of an app by hand.
//
// Nothing in here knows what carries the bytes.
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

// Transport moves one APDU to the card and returns its response.
//
// Implemented in Kotlin as `IsoDep.transceive(ByteArray): ByteArray`, which is
// the same shape. Bound through gomobile, so the signature is deliberately
// []byte rather than anything richer.
type Transport interface {
	Transmit(apdu []byte) ([]byte, error)
}

// Path is where a mesh's authority key lives on the card.
//
// This used to be `m/44'/60'/0'/0` — the standard Ethereum path, and the one
// loam-keycard uses, so that a card enrolled for a Loam identity presented the
// same key here. That was a deliberate convenience and it was the wrong trade.
//
// A mesh's admin_keys is in every member's config. With the wallet path, that
// means everybody you share a mesh with can read your Loam/Ethereum identity's
// public key and look up whatever that address has ever done. The linkage only
// runs one way — rendezvous topics derive from the network key, not from the
// mesh id, so nobody can find your mesh from your wallet — but mesh to identity
// is the direction that matters when a mesh is shared with somebody outside the
// household, which is the case this project exists for.
//
// It also meant one key signing in two protocols. The digests are domain
// separated so neither can forge the other, but compromise would have been
// shared, and there is no reason to accept that for a convenience.
//
// **This cannot be changed once a mesh exists.** The mesh id is the hash of the
// admin key set and the overlay prefix derives from the id, so a different path
// is a different mesh, and every device would have to be re-enrolled and
// re-addressed.
//
// The purpose index is the first two bytes of SHA-256("shrooms"), 0xfb09 —
// arbitrary, but reproducible and written down rather than picked and forgotten.
// It is clear of every registered BIP purpose (44, 45, 47, 48, 49, 84, 86,
// 1852…), so no wallet restoring this mnemonic will scan it, offer it as an
// account, or spend from it.
//
// Every level is hardened, so an extended public key from anywhere above cannot
// derive this one or its siblings.
//
// The second level is the mesh, reserved rather than used: one card can hold a
// distinct authority per mesh (ADR-015), and two meshes sharing an admin key
// would be linkable to each other through admin_keys alone. Today everything
// mints at 0'.
const Path = "m/64265'/0'/0'"

// DefaultPairingPassword is what a card initialised without a chosen
// pairing password will have.
//
// keycard-cli's internal/secrets.go defines it, and the Keycard demo app offers
// it as "use default pairing password" — which is where somebody holding a card
// they set up with that app will have got theirs. Offered in the UI as a
// default rather than typed from memory, because it is not a secret in any
// useful sense and getting it wrong costs a pairing slot.
const DefaultPairingPassword = "KeycardDefaultPairing"

// cardKeyFile is where the authority key this phone enrolled with is kept.
//
// The public half only, and only so the settings screen can say which key this
// phone signs with without asking anybody to find a card and type a PIN. Losing
// it costs one tap to recover.
//
// Stored with the derivation path it came from, and refused if that path is not
// the one in use now. This is not tidiness. A key shown on that screen is a key
// somebody copies into admin_keys, the mesh id is the hash of admin_keys, and
// the overlay prefix derives from the mesh id — so a mesh minted from a stale
// key is a mesh whose card cannot sign for it, permanently, with re-enrolling
// every device as the only way out. The path changed once already; treating a
// key from the old one as absent costs a tap and prevents that.
func cardKeyFile(configDir string) string {
	return filepath.Join(configDir, "keycard-key")
}

// storedKey is what cardKeyFile holds.
type storedKey struct {
	Path string `json:"path"`
	Key  string `json:"key"`
}

// writeCardKey records the key and the path it was derived at.
func writeCardKey(configDir, key string) error {
	b, err := json.Marshal(storedKey{Path: Path, Key: key})
	if err != nil {
		return err
	}
	return os.WriteFile(cardKeyFile(configDir), b, 0o600)
}

// readCardKey returns the stored key, or empty if it was derived somewhere else.
//
// A file that does not parse is treated as absent rather than as an error: the
// first version of this stored bare hex with no path, and a bare hex key is
// exactly the case that must not be offered — it predates the path change, so
// it is the stale one.
func readCardKey(configDir string) string {
	raw, err := os.ReadFile(cardKeyFile(configDir))
	if err != nil {
		return ""
	}
	var sk storedKey
	if json.Unmarshal(raw, &sk) != nil {
		return ""
	}
	if sk.Path != Path {
		return ""
	}
	return strings.TrimSpace(sk.Key)
}

// pairingFile is where the pairing for this card is kept.
//
// It has to be kept. A card has a small, fixed number of pairing slots — pair
// on every use and they are gone, permanently, and the card needs unpairing
// with the PUK to recover. So pairing happens once, in Enrol, and every
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
	cs *kc.CommandSet
	// rec keeps the last exchange, so a failure can show what the card said.
	rec *recorder
}

// openCard selects the applet and restores the stored pairing.
func openCard(t Transport, configDir string) (*cardSession, error) {
	if t == nil {
		return nil, errors.New("no card transport")
	}
	rec := &recorder{inner: t}
	cs := kc.NewCommandSet(kio.NewNormalChannel(rec))
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
	// Version-agnostic, for the same reason Enrol pairs that way: a card
	// running applet 4.0 or later opens its channel differently, and the V1
	// call fails there in a way that reads as the wrong card.
	if err := cs.AutoOpenSecureChannel(); err != nil {
		// Usually a stale pairing rather than the wrong card, and the two look
		// identical from here. Initialising a card wipes every pairing slot on
		// it, so a pairing saved before an INIT — or from a different card —
		// opens nothing. Pairing again replaces what is stored, which is the
		// fix and not something anybody would guess from "wrong card".
		return nil, fmt.Errorf("could not open a secure channel. Either this is "+
			"a different card, or the saved pairing is stale — initialising a "+
			"card clears its pairing slots, so a pairing from before that is "+
			"dead. Pair this phone again to replace it: %w", err)
	}
	return &cardSession{cs: cs, rec: rec}, nil
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

// Enrol pairs this phone with the card, once, and returns the authority's
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
func Enrol(t Transport, configDir, pairingPassword, pin string) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	rec := &recorder{inner: t}
	cs := kc.NewCommandSet(kio.NewNormalChannel(rec))
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
	if err := cs.AutoPairWithMode(pairingPassword, kc.P2PairAny); err != nil {
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
		return "", fmt.Errorf("paired, and %w. The pairing is saved, so this "+
			"card does not need pairing again — only the right PIN", pinError(err))
	}
	key, err := cardPublicKey(cs, rec)
	if err != nil {
		return "", err
	}
	// Best effort: the enrolment succeeded whether or not this lands, and
	// failing here would report a failure that did not happen.
	_ = writeCardKey(configDir, key)
	return key, nil
}

// cardPublicKey exports the authority's public half, compressed.
// recorder wraps a transport and keeps the last exchange, so a failure can show
// what the card actually said.
//
// The library parses a signature response one of two ways depending on which
// TLV tag the card used, and the two produce the public key from different
// places — one reads it, the other recovers it. Reasoning about which happened
// from the parsed values alone has already produced one wrong conclusion; the
// bytes settle it.
type recorder struct {
	inner Transport
	last  []byte
	sent  []byte
}

func (r *recorder) Transmit(apdu []byte) ([]byte, error) {
	r.sent = append([]byte(nil), apdu...)
	out, err := r.inner.Transmit(apdu)
	if err == nil {
		r.last = append([]byte(nil), out...)
	}
	return out, err
}

// enrolDigest is what a card signs to prove itself during enrolment.
//
// Fixed and domain-separated: this is not a credential and must never be
// mistaken for one, so it is a string that says what it is rather than a
// random challenge or an empty buffer.
var enrolDigest = sha256.Sum256([]byte("shrooms/keycard/enrol/v1"))

// cardPublicKey reads the authority key by SIGNING with it, not by exporting it.
//
// EXPORT KEY needs conditions a card may not grant — it answered 6985 on a
// freshly initialised card even inside an authenticated session — and it asks
// for a capability that is not needed here. A signature response already
// carries the public key, so signing a fixed digest yields the key AND proves
// the whole path in one exchange: pairing, PIN, on-card signing, and both
// conversions. loam-keycard, extracted from scala, enrols exactly this way.
//
// The signature is verified before the key is believed. A card that returns a
// plausible key and an unverifiable signature is the failure worth catching,
// and it cannot be caught by exporting a key.
func cardPublicKey(cs *kc.CommandSet, rec *recorder) (string, error) {
	sig, err := cs.SignWithPath(enrolDigest[:], Path)
	if err != nil {
		return "", fmt.Errorf("the card would not sign at %s — it may hold no key "+
			"yet, which INIT does not create: %w", Path, err)
	}
	if len(sig.PubKey()) == 0 {
		return "", errors.New("the card signed but returned no public key with it")
	}
	c, err := cred.CompressPoint(sig.PubKey())
	if err != nil {
		return "", err
	}
	compact, err := cred.CompactSig(sig.R(), sig.S())
	if err != nil {
		return "", err
	}
	// keycard-go returns an s that is two bytes short of what the card sent —
	// a slice-aliasing bug in its own signature parser, not anything the card
	// did. Repaired here, and the repair only ever returns a signature that
	// verifies, so a card that is genuinely wrong still fails below.
	if repaired, ok := cred.RepairCardSignature(c, enrolDigest, compact); ok {
		compact = repaired
	}
	if !cred.VerifyDigest(c, enrolDigest, compact) {
		// What it actually returned, because this is the one failure that
		// cannot be reproduced without the card in hand. Every value here is
		// public — a point, r, s, and the recovery byte — so printing them
		// costs nothing and saves a round trip per guess.
		//
		// keycard-go builds the public key one of two ways depending on what
		// the card sends back: a legacy template carries it, and a raw
		// recoverable signature does NOT, so the library recovers it from the
		// signature and the recovery byte. A recovered key that is wrong
		// verifies nothing, and looks exactly like a conversion bug from here.
		raw := ""
		if rec != nil {
			raw = fmt.Sprintf("\nraw response: %x", rec.last)
		}
		return "", fmt.Errorf("the card signed, but the signature does not verify "+
			"against the key it returned.\n\n"+
			"pub %d bytes: %x\ncompressed: %x\nr %d bytes: %x\ns %d bytes: %x\nv: %02x\n"+
			"digest: %x%s",
			len(sig.PubKey()), sig.PubKey(), c,
			len(sig.R()), sig.R(), len(sig.S()), sig.S(), sig.V(), enrolDigest, raw)
	}
	return hex.EncodeToString(c), nil
}

// PublicKey reads the authority key from an already-enrolled card.
//
// Needs the PIN: EXPORT KEY is only allowed inside an authenticated session, and
// without one the card answers 6985.
func PublicKey(t Transport, configDir, pin string) (string, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return "", err
	}
	if err := s.cs.VerifyPIN(pin); err != nil {
		return "", pinError(err)
	}
	return cardPublicKey(s.cs, s.rec)
}

// cardSigner is cred.Signer backed by the card.
//
// One tap, one signature: the session is opened for the operation and closed
// with it, because between two signatures the phone may have been in a pocket
// and the card on a desk.
type cardSigner struct {
	t         Transport
	configDir string
	pin       string
	pub       []byte
}

// NewSigner opens a card as a cred.Signer: one tap, one signature.
//
// Exported because the caller that needs it is in another package now — the
// phone's invite flow, and shortly whatever mints a mesh from a reader.
func NewSigner(t Transport, configDir, pin string) (cred.Signer, error) {
	return newCardSigner(t, configDir, pin)
}

// newCardSigner opens a session far enough to read the public key, so that a
// wrong PIN or an unenrolled card is reported before anything is half-done.
func newCardSigner(t Transport, configDir, pin string) (*cardSigner, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return nil, err
	}
	if err := s.cs.VerifyPIN(pin); err != nil {
		return nil, pinError(err)
	}
	hexPub, err := cardPublicKey(s.cs, s.rec)
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
	sig, err := s.cs.SignWithPath(d[:], Path)
	if err != nil {
		return nil, fmt.Errorf("the card did not sign: %w", err)
	}
	compact, err := cred.CompactSig(sig.R(), sig.S())
	if err != nil {
		return nil, err
	}
	// The same two bytes the library loses on the way out of its parser. Every
	// signature this card makes needs them back, not only the enrolment one —
	// a credential signed with the mangled s is refused by every peer, silently
	// and for a reason nobody would find.
	repaired, ok := cred.RepairCardSignature(c.pub, d, compact)
	if !ok {
		return nil, errors.New("the card's signature could not be reconciled " +
			"with its own key, even allowing for the two bytes keycard-go loses")
	}
	return repaired, nil
}

var _ cred.Signer = (*cardSigner)(nil)

// SelfTest proves the whole signing path against a real card: pairing,
// PIN, the card's signature, and the two conversions on the way back.
//
// It signs a digest and checks the result with the same function a peer uses on
// a credential. That is the point — a card that returns something plausible and
// unverifiable is the failure this is looking for, and it cannot be found
// without hardware. Nothing is published and no credential is issued.
//
// Returns the authority key on success, so the caller can see which card it
// just proved.
func SelfTest(t Transport, configDir, pin string) (string, error) {
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

// pinError says what a refused PIN costs, which is the part that matters.
//
// keycard-go returns the remaining attempt count, and it arrives inside a
// sentence that reads like a detail: "wrong pin. remaining attempts: 2". It is
// not a detail. A Keycard blocks after three wrong PINs and then needs its PUK,
// which somebody who has been typing guesses is unlikely to have to hand — and
// that count is the only warning there is before it happens.
//
// Unblocking is deliberately not offered here: it spends the PUK, and a PUK
// entered wrong ten times destroys the key on the card for good.
func pinError(err error) error {
	var wrong *kc.WrongPINError
	if !errors.As(err, &wrong) {
		return err
	}
	switch n := wrong.RemainingAttempts; {
	case n <= 0:
		return errors.New("wrong PIN, and that was the last attempt — this card " +
			"is now blocked and needs its PUK. keycard-cli and the Keycard app " +
			"can unblock it; this app deliberately cannot")
	case n == 1:
		return errors.New("wrong PIN. ONE attempt left before this card blocks " +
			"and needs its PUK")
	default:
		return fmt.Errorf("wrong PIN. %d attempts left before this card blocks "+
			"and needs its PUK", n)
	}
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
		errors.Is(err, kc.ErrNoAvailablePairingSlots) {
		return fmt.Errorf("this card has no free pairing slots — it has %d and "+
			"they are all taken, by other devices or by earlier attempts from "+
			"this one. Free one with \"forget other devices\" if this phone is "+
			"already paired, or unpair elsewhere. The pairing password is not "+
			"the problem: %w", kc.PairingMaxClientCount, err)
	}
	return fmt.Errorf("pairing refused — wrong pairing password, or the card "+
		"refused the request: %w", err)
}

// UnpairOthers frees every pairing slot except this phone's.
//
// Needs this phone to be paired already, because unpairing happens inside the
// secure channel a pairing opens — a card with no slots left and no pairing
// here cannot be recovered from this app, and needs keycard-cli or another tool
// that still holds one.
//
// Kept separate from enrolment and named for what it does to somebody else's
// pairing, because it is not undoable: every other device that was paired with
// this card stops being able to use it.
func UnpairOthers(t Transport, configDir string) (string, error) {
	s, err := openCard(t, configDir)
	if err != nil {
		return "", err
	}
	if err := s.cs.UnpairOthers(); err != nil {
		return "", fmt.Errorf("could not free the other pairing slots: %w", err)
	}
	return fmt.Sprintf("freed the other slots; this phone is still paired (%d in total)",
		kc.PairingMaxClientCount), nil
}

// Init sets up a blank card: its PIN, PUK and pairing password, and the key it
// will sign with.
//
// [ADR-022](../../docs/adr/022-keycard-for-the-admin-key.md) said this belonged
// in the Keycard app rather than here, because it is irreversible and decides
// what a card IS — and doing that from a VPN's settings screen by accident is
// not a thing anybody should be able to do. That reasoning was about a phone
// screen. On a command line, behind a confirmation, against a card the caller
// has physically inserted, it is the difference between shrooms being usable on
// its own and needing a second tool nobody has installed.
//
// It refuses a card that is already initialised rather than resetting it. That
// is the accident worth preventing here: `init` on the wrong card would destroy
// a working authority, and INIT itself will not overwrite one.
//
// phrase empty generates a new mnemonic on the card and returns it. **It is
// returned because it must be written down**: it is the only way back to this
// key, and a card whose key exists nowhere else takes its mesh with it.
func Init(t Transport, pin, puk, pairingPassword, phrase string) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	cs := kc.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	if info := cs.ApplicationInfo; info != nil && info.Initialized {
		return "", errors.New("this card is already initialised. Initialising again " +
			"would have to wipe it first, and that is `keycard reset` — deliberately " +
			"a separate command, because it destroys the key")
	}
	if err := cs.Init(kc.NewSecrets(pin, puk, pairingPassword)); err != nil {
		return "", fmt.Errorf("could not initialise the card: %w", err)
	}
	// SELECT again. The response from an uninitialised card carries no applet
	// version, and the version-agnostic pairing and channel calls read that to
	// decide which protocol to speak — so pairing against the stale answer
	// succeeds and the channel then fails with 6982, MUTUALLY_AUTHENTICATE, on
	// a card that had just accepted the password it was paired with.
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("initialised, but the card would not answer SELECT again: %w", err)
	}

	// Pair and open a channel: loading a key is a protected command, so the
	// card must be talking to somebody it has agreed a secret with — even one
	// second old.
	if err := cs.AutoPairWithMode(pairingPassword, kc.P2PairAny); err != nil {
		return "", fmt.Errorf("initialised, but could not pair to load a key: %w", pairingError(err))
	}
	if err := cs.AutoOpenSecureChannel(); err != nil {
		return "", fmt.Errorf("initialised and paired, but no secure channel: %w", err)
	}
	if err := cs.VerifyPIN(pin); err != nil {
		return "", fmt.Errorf("initialised, and the PIN it was just given was refused: %w", pinError(err))
	}

	m, err := mnemonicFor(cs, phrase)
	if err != nil {
		return "", err
	}
	if _, err := cs.LoadSeed(m.ToSeed("")); err != nil {
		return "", fmt.Errorf("initialised, but could not load the key: %w", err)
	}
	// The pairing this used is left on the card and not written down: `keycard
	// pair` takes a slot of its own, and one slot spent here would be a slot
	// nobody can account for later.
	_ = cs.Unpair(cs.Pairing().Index())
	return m.ToPhrase(), nil
}

// mnemonicFor takes the phrase given, or has the card make one.
//
// Generated ON THE CARD rather than here, when there is no phrase: the card has
// a hardware random source and this process has whatever the machine offers,
// and a key that protects a mesh should not depend on which of those is better.
func mnemonicFor(cs *kc.CommandSet, phrase string) (*ktypes.Mnemonic, error) {
	if phrase != "" {
		m, err := ktypes.MnemonicFromPhrase(phrase)
		if err != nil {
			return nil, fmt.Errorf("that is not a valid BIP-39 phrase: %w", err)
		}
		return m, nil
	}
	// 4 gives twelve words, which is what every wallet and every piece of paper
	// expects.
	indices, err := cs.GenerateMnemonic(4)
	if err != nil {
		return nil, fmt.Errorf("the card would not generate a mnemonic: %w", err)
	}
	small := make([]int16, len(indices))
	for i, v := range indices {
		small[i] = int16(v)
	}
	m, err := ktypes.MnemonicFromIndices(small)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Reset wipes the card: keys, PIN, PUK and every pairing slot.
//
// Unauthenticated by design — INS 0xFD with two magic bytes and nothing else,
// sent on the raw channel rather than a protected one. That is deliberate on
// the card's part: it is the way back when every pairing slot is taken and no
// device holds one, which is otherwise unrecoverable, because UNPAIR travels
// inside a channel only a pairing can open.
//
// The magic bytes are the whole guard, so this cannot happen by accident. It
// can happen very much on purpose, to anybody holding the card, which is worth
// knowing about a card in a drawer: physical possession is enough to destroy
// what is on it, though not to use it.
//
// **The key is gone afterwards.** A card whose key was generated on it and
// never written down as a mnemonic cannot be recovered, and any mesh minted
// against that key can never admit another device.
func Reset(t Transport) error {
	if t == nil {
		return errors.New("no card transport")
	}
	cs := kc.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	if info := cs.ApplicationInfo; info != nil && !info.HasFactoryResetCapability() {
		return errors.New("this card does not support factory reset, so a full " +
			"set of pairing slots cannot be recovered from")
	}
	return cs.FactoryReset()
}

// Status reports what state a card is in, without pairing or a PIN.
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
//     without one there is nothing at Path to sign with.
//   - how many pairing slots are free? Five is the maximum and zero is what
//     "6a84" means.
//   - which applet version? Below 4.0 pairing uses a password; at 4.0 and above
//     it uses a certificate and the password is not asked for.
//
// Neither INIT nor key generation is done here. Both are one-time acts that
// decide what a card IS, they are irreversible, and doing them from a VPN's
// settings screen by accident is not a thing anybody should be able to do. Use
// keycard-cli or the Keycard app.
func Status(t Transport) (string, error) {
	if t == nil {
		return "", errors.New("no card transport")
	}
	cs := kc.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	info := cs.ApplicationInfo
	if info == nil {
		return "", errors.New("the card answered SELECT with nothing to read")
	}
	return reportOf(info).encode()
}

// reportOf turns what the card said about itself into what the screen needs.
//
// Separated from the exchange so it can be tested without a card or a fake one:
// this is the decision table the whole setup flow branches on — whether to ask
// for a pairing password, whether there is any point asking for a PIN, whether
// freeing the other slots would help — and it is worth pinning that four
// conditions are checked in the order they stop you, since a card can fail more
// than one at once and only the first is worth saying.
func reportOf(info *ktypes.ApplicationInfo) cardReport {
	rep := cardReport{MaxSlots: kc.PairingMaxClientCount, FreeSlots: -1}

	version := "unknown"
	if len(info.Version) >= 2 {
		version = fmt.Sprintf("%d.%d", info.Version[0], info.Version[1])
		// Applet 4.0 and later authenticates with a certificate against the
		// Status CA, and asking for a pairing password there is asking for
		// something that does not exist.
		rep.NeedsPassword = info.Version[0] < 4
	} else {
		rep.NeedsPassword = true
	}
	rep.Applet = version
	rep.Initialised = info.Initialized
	rep.HasKey = len(info.KeyUID) > 0
	if rep.HasKey {
		rep.KeyUID = fmt.Sprintf("%x", info.KeyUID[:min(4, len(info.KeyUID))])
	}
	slots := "unknown"
	if len(info.AvailableSlots) >= 1 {
		free := int(info.AvailableSlots[0])
		rep.FreeSlots = free
		slots = fmt.Sprintf("%d of %d free", free, kc.PairingMaxClientCount)
		if free == 0 {
			slots += " — this is what 6a84 means"
		}
	}

	caps := capabilityList(info)
	rep.Capabilities = caps

	switch {
	case !info.HasSecureChannelCapability():
		// A card that cannot open a secure channel cannot be paired, and the
		// PAIR instruction is not implemented — which is what "6d00" means, an
		// instruction the applet does not have. A Cash card is the usual reason
		// to be holding one of these.
		rep.Problem = "no-secure-channel"
		rep.Summary = fmt.Sprintf("This card cannot open a secure channel, so it "+
			"cannot be paired and cannot sign for a mesh. That is what 6d00 "+
			"means: the applet does not implement PAIR. A Cash card is the "+
			"usual reason to be holding one. Applet %s, capabilities: %s",
			version, caps)
	case !info.Initialized:
		rep.Problem = "not-initialised"
		rep.Summary = fmt.Sprintf("This card has never been initialised, so it "+
			"has no PIN, no PUK and no pairing password — there is nothing to "+
			"pair with yet. Set it up in the Keycard app or keycard-cli first; "+
			"shrooms deliberately will not, because it is irreversible and "+
			"decides what the card is. Applet %s, pairing slots: %s",
			version, slots)
	case !rep.HasKey:
		rep.Problem = "no-key"
		rep.Summary = fmt.Sprintf("This card is initialised but holds no key. "+
			"Pairing would work and there would be nothing to sign with. "+
			"Generate or load a key in the Keycard app or keycard-cli. "+
			"Applet %s, pairing slots: %s", version, slots)
	default:
		if rep.FreeSlots == 0 {
			// Not a dead end: this phone may already hold one of those five
			// slots, in which case it can free the rest. The UI decides,
			// because it knows whether a pairing is stored here.
			rep.Problem = "no-slots"
		}
		rep.Summary = fmt.Sprintf("Applet %s, initialised, holds a key (%s). "+
			"Pairing slots: %s. Capabilities: %s",
			version, rep.KeyUID, slots, caps)
	}
	return rep
}

// cardReport is what Status answers with, as JSON.
//
// It used to answer with a sentence, which read well and left the screen unable
// to do anything with it: whether to ask for a pairing password, whether to
// offer freeing the other slots, whether there is any point asking for a PIN.
// All of that was in the prose and none of it was reachable, so the screen
// showed every button always and left somebody holding a card to work out which
// one applied. Summary is still a sentence, because the failures are things
// nobody should have to decode from a field.
type cardReport struct {
	Applet       string `json:"applet"`
	Initialised  bool   `json:"initialised"`
	HasKey       bool   `json:"hasKey"`
	KeyUID       string `json:"keyUID"`
	FreeSlots    int    `json:"freeSlots"`
	MaxSlots     int    `json:"maxSlots"`
	Capabilities string `json:"capabilities"`
	// NeedsPassword is false for applet 4.0 and later, which pairs with a
	// certificate. Asking for a password there is asking for something the card
	// does not have.
	NeedsPassword bool `json:"needsPassword"`
	// Problem is empty when the card can be enrolled. Otherwise one of
	// no-secure-channel, not-initialised, no-key, no-slots — the four walls, in
	// the order they stop you.
	Problem string `json:"problem"`
	Summary string `json:"summary"`
}

func (r cardReport) encode() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Enrolment says whether this phone is set up with a card, without needing
// one to be present.
//
// The pairing and the key it enrolled with are both on disk, so the settings
// screen can open showing what it is set up with rather than showing five
// buttons and no idea which apply.
func Enrolment(configDir string) string {
	out := struct {
		Paired bool   `json:"paired"`
		Key    string `json:"key"`
	}{}
	if _, err := os.Stat(pairingFile(configDir)); err == nil {
		out.Paired = true
	}
	out.Key = readCardKey(configDir)
	b, err := json.Marshal(out)
	if err != nil {
		return `{"paired":false,"key":""}`
	}
	return string(b)
}

// Files names everything an enrolment writes, in no particular order.
//
// Exported for one reason: `sudo shrooms init --keycard` writes these as root
// into the invoking user's config directory, and the caller is the only layer
// that knows about sudo. Rather than teach this package about SUDO_USER, it
// says which paths it owns and lets the command hand them over.
//
// Both may be absent — a device that has never enrolled has neither.
func Files(configDir string) []string {
	return []string{pairingFile(configDir), cardKeyFile(configDir)}
}

// Forget deletes this phone's pairing, and says what it does not do.
//
// It does not free the slot on the card. The card has no idea this happened —
// it still counts this phone among the five devices it is paired with, and
// pairing again takes another slot. Freeing it needs the card present, which is
// what UnpairOthers is for, run from a device that still holds a pairing.
//
// Worth having anyway: a pairing that no longer opens a channel is worse than
// none, because every operation fails at the same place with an error about the
// wrong card.
func Forget(configDir string) error {
	if err := os.Remove(pairingFile(configDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(cardKeyFile(configDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
		// Listed because its absence is what makes a full card unrecoverable,
		// and its presence is the way out. Omitting it meant a card that could
		// be reset reported four capabilities out of the five it has, and the
		// missing one was the only one that mattered when every pairing slot
		// was gone.
		{"factory-reset", info.HasFactoryResetCapability()},
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
