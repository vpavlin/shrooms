//go:build !pcsc

package keycard

import "errors"

// Reader support is a build-time choice (see pcsc.go).
//
// The error says how to get it rather than that it is missing: somebody hitting
// this has a reader plugged in and a card on it, and "not supported" would be
// both wrong and unhelpful.
var errNoPCSC = errors.New("this build has no smartcard reader support. " +
	"It links libpcsclite through cgo, which the daemon and the container image " +
	"have no use for, so it is off by default.\n\n" +
	"Rebuild with it:\n" +
	"    sudo apt install libpcsclite-dev\n" +
	"    make install TAGS=pcsc\n\n" +
	"Or use a phone, which reaches the same card over NFC")

// OpenReader reports that this build cannot reach a reader.
func OpenReader(string) (Transport, func(), error) { return nil, nil, errNoPCSC }

// Readers reports that this build cannot reach a reader.
func Readers() ([]string, error) { return nil, errNoPCSC }
