//go:build !linux || !cgo

package keycard

import "errors"

// Reader support exists on Linux, where PC/SC does.
//
// Not a build tag any more: on Linux the library is opened at first use, so
// there is nothing to opt into. This is the genuinely-cannot case — another
// operating system, or a build with cgo switched off.
var errNoPCSC = errors.New("this build cannot reach a smartcard reader: it was " +
	"built without cgo, or for a system with no PC/SC.\n\n" +
	"Use a phone, which reaches the same card over NFC")

// OpenReader reports that this build cannot reach a reader.
func OpenReader(string) (Transport, func(), error) { return nil, nil, errNoPCSC }

// Readers reports that this build cannot reach a reader.
func Readers() ([]string, error) { return nil, errNoPCSC }
