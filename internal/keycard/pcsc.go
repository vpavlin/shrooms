//go:build pcsc

package keycard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ebfe/scard"
)

// A Keycard reached through a USB reader, for machines with no NFC.
//
// Built only with `-tags pcsc`, because it links libpcsclite through cgo and
// most of what this project ships has no business needing a smartcard library:
// the daemon never touches a card, the container image would carry the
// dependency for a feature it cannot use, and anybody building from source
// would need the development headers to compile a VPN. See pcsc_stub.go for
// what a build without it says.
//
// The transport is the only platform-specific part. Everything above it —
// pairing, the secure channel, the PIN, signing — is the same code the phone
// runs over NFC, which is the whole point of CardTransport being one method.

// reader is a card in a slot, as a Transport.
type reader struct {
	card *scard.Card
}

func (r *reader) Transmit(apdu []byte) ([]byte, error) { return r.card.Transmit(apdu) }

// OpenReader connects to a card and returns it as a Transport.
//
// The returned function releases the card and the context, and must be called:
// pcscd keeps an exclusive connection open until it is, so a program that exits
// without releasing leaves the next one unable to reach the reader at all.
//
// An empty name takes the only reader when there is exactly one, and refuses
// when there are several rather than picking. A machine with two readers is a
// machine where the wrong card is in one of them.
func OpenReader(name string) (Transport, func(), error) {
	ctx, err := scard.EstablishContext()
	if err != nil {
		return nil, nil, fmt.Errorf("no PC/SC service: %w — is pcscd running? "+
			"`sudo systemctl start pcscd`", err)
	}
	release := func() { _ = ctx.Release() }

	readers, err := ctx.ListReaders()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("could not list readers: %w", err)
	}
	if len(readers) == 0 {
		release()
		return nil, nil, errors.New("no smartcard reader found — is one plugged in?")
	}

	picked := name
	if picked == "" {
		if len(readers) > 1 {
			release()
			return nil, nil, fmt.Errorf("several readers are attached, so name the one to use "+
				"with --reader:\n  %s", strings.Join(readers, "\n  "))
		}
		picked = readers[0]
	} else {
		found := false
		for _, r := range readers {
			if strings.Contains(strings.ToLower(r), strings.ToLower(picked)) {
				picked, found = r, true
				break
			}
		}
		if !found {
			release()
			return nil, nil, fmt.Errorf("no reader matching %q. Attached:\n  %s",
				name, strings.Join(readers, "\n  "))
		}
	}

	// Exclusive, because a Keycard session is stateful: the secure channel and
	// the verified PIN belong to this connection, and anything else touching
	// the card mid-conversation invalidates both.
	card, err := ctx.Connect(picked, scard.ShareExclusive, scard.ProtocolAny)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("no card in %q: %w — is it on the reader?", picked, err)
	}
	return &reader{card: card}, func() {
		_ = card.Disconnect(scard.ResetCard)
		release()
	}, nil
}

// Readers lists what is attached, for a caller that wants to say so.
func Readers() ([]string, error) {
	ctx, err := scard.EstablishContext()
	if err != nil {
		return nil, err
	}
	defer func() { _ = ctx.Release() }()
	return ctx.ListReaders()
}
