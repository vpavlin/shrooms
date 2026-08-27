//go:build linux && cgo

package keycard

import (
	"errors"
	"fmt"
	"strings"
)

// A Keycard reached through a USB reader, for machines with no NFC.
//
// Always built, unlike the -tags pcsc it replaced. The library is opened at
// first use rather than linked (pcsc_load.go), so this costs a machine without
// a reader nothing at all — and the guide no longer has to ask somebody to
// install a Go toolchain to talk to a card.
//
// The transport is the only platform-specific part. Everything above it —
// pairing, the secure channel, the PIN, signing — is the same code the phone
// runs over NFC, which is the whole point of Transport being one method.

// reader is a card in a slot, as a Transport.
type reader struct {
	card *pcscCard
}

func (r *reader) Transmit(apdu []byte) ([]byte, error) { return r.card.transmit(apdu) }

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
	ctx, err := pcscEstablish()
	if err != nil {
		return nil, nil, err
	}
	release := ctx.release

	readers, err := ctx.readers()
	if err != nil {
		release()
		return nil, nil, err
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
	card, err := ctx.connect(picked)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("%q: %w", picked, err)
	}
	return &reader{card: card}, func() {
		card.disconnect()
		release()
	}, nil
}

// Readers lists what is attached, for a caller that wants to say so.
func Readers() ([]string, error) {
	ctx, err := pcscEstablish()
	if err != nil {
		return nil, err
	}
	defer ctx.release()
	return ctx.readers()
}
