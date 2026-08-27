package mobile

import (
	"github.com/vpavlin/shrooms/internal/keycard"
)

// The Keycard surface gomobile binds, and nothing else (ADR-022,
// docs/keycard-on-mobile.md).
//
// The protocol itself — pairing, the secure channel, the PIN, signing at this
// project's derivation path — is in internal/keycard, shared with the desktop.
// It lived here while a phone was the only thing that could reach a card, and
// mobile/ is a separate Go module, so nothing on the desktop could import a
// line of it.
//
// What stays here is the shape gomobile can bind: exported functions over
// strings and []byte, and an interface Kotlin implements. Android supplies one
// thing — a way to move bytes to the card, which is `IsoDep.transceive` — and
// that is the whole of the platform-specific surface.

// CardTransport moves one APDU to the card and returns its response.
//
// Implemented in Kotlin as `IsoDep.transceive(ByteArray): ByteArray`. Declared
// here rather than aliased to keycard.Transport because gomobile binds what it
// finds in this package, and an alias to an internal type is not something it
// can generate a Java interface from. The two are structurally identical, which
// is all Go requires to pass one where the other is wanted.
type CardTransport interface {
	Transmit(apdu []byte) ([]byte, error)
}

// KeycardPath is where a mesh's authority key lives on the card.
const KeycardPath = keycard.Path

// CardDefaultPairingPassword is what a card initialised without a chosen
// pairing password will have.
const CardDefaultPairingPassword = keycard.DefaultPairingPassword

// CardEnrol pairs this phone with the card, once, and returns the authority's
// public key in the form an admin_keys entry takes.
func CardEnrol(t CardTransport, configDir, pairingPassword, pin string) (string, error) {
	return keycard.Enrol(t, configDir, pairingPassword, pin, 0)
}

// CardPublicKey reads the authority key from an already-enrolled card.
func CardPublicKey(t CardTransport, configDir, pin string) (string, error) {
	return keycard.PublicKey(t, configDir, pin, 0)
}

// CardSelfTest signs a fixed digest on the card and verifies it with the same
// function a peer applies to a credential.
func CardSelfTest(t CardTransport, configDir, pin string) (string, error) {
	return keycard.SelfTest(t, configDir, pin, 0)
}

// CardUnpairOthers frees every pairing slot except this phone's.
func CardUnpairOthers(t CardTransport, configDir string) (string, error) {
	return keycard.UnpairOthers(t, configDir)
}

// CardStatus reports what state a card is in, without pairing or a PIN.
func CardStatus(t CardTransport) (string, error) {
	return keycard.Status(t)
}

// CardEnrolment says whether this phone is set up with a card, without needing
// one to be present.
func CardEnrolment(configDir string) string {
	return keycard.Enrolment(configDir)
}

// CardForget deletes this phone's pairing. It does not free the slot on the
// card, which still counts this phone among the five it is paired with.
func CardForget(configDir string) error {
	return keycard.Forget(configDir)
}
